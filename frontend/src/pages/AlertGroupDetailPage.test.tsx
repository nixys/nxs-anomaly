import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AlertGroupDetailPage } from './AlertGroupDetailPage';

const get = vi.fn();

vi.mock('../api/client', () => ({
  api: {
    get: (...args: unknown[]) => get(...args),
    post: vi.fn(),
    put: vi.fn(),
    patch: vi.fn(),
    delete: vi.fn(),
  },
  ApiError: class ApiError extends Error {},
}));

const group = {
  id: 'grp_1',
  integration_id: 'int_1',
  title: 'Disk full',
  status: 'open',
  severity: 'critical',
  labels: {},
  log: [],
  current_step: 0,
  repeat_count: 0,
  created_at: '2026-06-01T00:00:00Z',
  updated_at: '2026-06-01T00:00:00Z',
};

function notification(id: string, status: string, channel: string, providerStatus?: string) {
  return {
    id,
    alert_group_id: 'grp_1',
    user_id: 'u_a',
    channel,
    target: 'x',
    status,
    reason: 'escalation',
    idempotency_key: id,
    retry_count: 0,
    next_retry_at: null,
    last_error: null,
    batch_id: null,
    batch_key: null,
    provider_status: providerStatus ?? null,
    created_at: '2026-06-01T00:00:00Z',
    updated_at: '2026-06-01T00:00:00Z',
  };
}

function renderPage(notifications: ReturnType<typeof notification>[]) {
  get.mockImplementation((path: string) => {
    if (path === '/api/v1/alert-groups/grp_1') return Promise.resolve(group);
    if (path === '/api/v1/notifications') {
      return Promise.resolve({ items: notifications, total: notifications.length });
    }
    return Promise.resolve({ items: [], total: 0 });
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/alert-groups/grp_1']}>
          <Routes>
            <Route path="/alert-groups/:id" element={<AlertGroupDetailPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

beforeEach(() => vi.clearAllMocks());

describe('AlertGroupDetailPage delivery health', () => {
  it('says nothing when every notification reached someone', async () => {
    renderPage([notification('n1', 'delivered', 'telegram')]);
    expect(await screen.findByText('Disk full')).toBeInTheDocument();
    expect(screen.queryByText(/reached nobody/i)).not.toBeInTheDocument();
  });

  it('warns the responder about permanently failed notifications', async () => {
    renderPage([notification('n1', 'failed', 'telegram')]);
    expect(await screen.findByText(/reached nobody/i)).toBeInTheDocument();
    // The channel is named inside the banner; "telegram" also appears in the
    // notifications tab, so scope the assertion to the banner text itself.
    const line = screen.getByText(/permanently failed after retries/i);
    expect(line.textContent).toMatch(/telegram/);
  });

  it('separates a skipped channel from a failure', async () => {
    renderPage([notification('n1', 'skipped', 'mobile', 'not_configured')]);
    const banner = await screen.findByText(/no transport configured here/i);
    expect(banner).toBeInTheDocument();
    // A skip is not a failure: the wording must not claim retries happened.
    expect(screen.queryByText(/permanently failed after retries/i)).not.toBeInTheDocument();
    expect(screen.getByText(/nothing will be retried/i)).toBeInTheDocument();
  });

  it('reports both kinds at once', async () => {
    renderPage([
      notification('n1', 'failed', 'webhook'),
      notification('n2', 'skipped', 'chatops', 'not_configured'),
    ]);
    expect(await screen.findByText(/permanently failed after retries/i)).toBeInTheDocument();
    expect(screen.getByText(/no transport configured here/i)).toBeInTheDocument();
  });
});
