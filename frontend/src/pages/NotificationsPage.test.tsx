import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { NotificationsPage } from './NotificationsPage';

// This is where somebody looks after an incident to answer "was anyone actually
// told?". The service is careful to distinguish delivered from skipped from
// failed; that distinction is worth nothing if the page renders them alike.

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

vi.mock('@mantine/notifications', () => ({
  notifications: { show: vi.fn(), hide: vi.fn() },
}));

function notification(id: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    alert_group_id: 'grp-1',
    user_id: 'usr-alice',
    channel: 'telegram',
    target: '4242',
    status: 'delivered',
    retry_count: 0,
    last_error: '',
    created_at: '2026-06-01T10:00:00Z',
    updated_at: '2026-06-01T10:00:01Z',
    ...overrides,
  };
}

function renderPage(items: ReturnType<typeof notification>[]) {
  get.mockImplementation((path: string) => {
    if (path.startsWith('/api/v1/notifications')) {
      return Promise.resolve({ items, total: items.length });
    }
    if (path.startsWith('/api/v1/users')) {
      return Promise.resolve({
        items: [{ id: 'usr-alice', name: 'Alice SRE' }],
        total: 1,
      });
    }
    return Promise.resolve({ items: [], total: 0 });
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <NotificationsPage />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

beforeEach(() => vi.clearAllMocks());

describe('NotificationsPage', () => {
  it('shows the channel and target a notification went to', async () => {
    renderPage([notification('n1')]);

    // Awaited on the target, not the channel: "telegram" is also a filter
    // option, so a channel assertion passes before any row has rendered — which
    // is how the first version of this test passed against an empty table.
    expect(await screen.findByText('4242')).toBeInTheDocument();
    expect(screen.getAllByText('telegram').length).toBeGreaterThan(1);
  });

  it('distinguishes delivered from skipped from failed', async () => {
    renderPage([
      notification('n1', { status: 'delivered' }),
      notification('n2', { status: 'skipped', channel: 'mobile', target: '' }),
      notification('n3', { status: 'failed', channel: 'email', target: 'a@example.com' }),
    ]);

    // Asserted on the rows themselves: every one of these words is also a
    // status filter option, so matching anywhere on the page proves nothing.
    await screen.findByText('a@example.com');
    const statuses = Array.from(document.querySelectorAll('tbody tr')).map(
      (row) => row.querySelector('td')?.textContent ?? '',
    );

    // "skipped" means nobody was told and no retry will help — the outcome the
    // engine went out of its way to stop reporting as delivered. If the page
    // collapses the three, that work is undone at the last step.
    expect(statuses).toEqual(
      expect.arrayContaining([
        expect.stringMatching(/delivered/i),
        expect.stringMatching(/skipped/i),
        expect.stringMatching(/failed/i),
      ]),
    );
  });

  it('shows why a delivery failed, next to the delivery', async () => {
    renderPage([
      notification('n1', { status: 'failed', last_error: 'HTTP 403 chat not found' }),
    ]);

    // The error is the whole reason to open this page after an incident.
    expect(await screen.findByText(/HTTP 403 chat not found/)).toBeInTheDocument();
  });

  it('names the recipient rather than showing their id', async () => {
    renderPage([notification('n1')]);
    await screen.findByText('4242');

    // Scoped to the row: the name is also a filter option, so matching anywhere
    // on the page would pass for a row still showing the raw id.
    const row = document.querySelector('tbody tr');
    expect(row?.textContent).toContain('Alice SRE');
    // The id is meaningless to whoever reads this after an incident, which is
    // why the page resolves it against the user list at all.
    expect(row?.textContent).not.toContain('usr-alice');
  });

  it('says a target is absent instead of leaving the cell blank', async () => {
    renderPage([notification('n1', { status: 'skipped', channel: 'mobile', target: '' })]);

    // An empty cell reads as a rendering bug; the dash says the notification
    // genuinely had nowhere to go, which is the point of a skip.
    expect(await screen.findByText('—')).toBeInTheDocument();
  });

  it('shows an empty state when the filters match nothing', async () => {
    renderPage([]);

    expect(await screen.findByText(/no notifications match/i)).toBeInTheDocument();
  });

  it('surfaces a failed query rather than an empty table', async () => {
    get.mockRejectedValue(new Error('notifications unavailable'));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <MantineProvider>
        <QueryClientProvider client={client}>
          <MemoryRouter>
            <NotificationsPage />
          </MemoryRouter>
        </QueryClientProvider>
      </MantineProvider>,
    );

    // "Nothing was sent" and "we could not ask" are the same picture otherwise,
    // and only one of them is good news.
    expect(await screen.findByText(/notifications unavailable/i)).toBeInTheDocument();
  });
});
