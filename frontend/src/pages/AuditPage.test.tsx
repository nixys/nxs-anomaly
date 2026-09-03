import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AuditPage } from './AuditPage';

// The audit trail is append-only in the database and read here. Its value is
// that it answers "who did this, and as what" — so the actor, the action and the
// request id are the parts worth pinning, and a failed read must not look like
// a quiet period.

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

function event(id: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    occurred_at: '2026-06-01T10:00:00Z',
    actor_id: 'usr-alice',
    actor_name: 'Alice SRE',
    actor_kind: 'user',
    actor_role: 'admin',
    action: 'acknowledge',
    entity_type: 'alert_group',
    entity_id: 'grp-1',
    request_id: 'req-42',
    request_ip: '10.0.0.1',
    data: { fields: ['status'] },
    ...overrides,
  };
}

function renderPage(items: ReturnType<typeof event>[]) {
  get.mockImplementation((path: string) => {
    if (path === '/api/v1/audit') return Promise.resolve({ items, total: items.length });
    return Promise.resolve({ items: [], total: 0 });
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <AuditPage />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

// rows returns the table body's rows as text, so an assertion cannot be
// satisfied by a filter control that happens to carry the same word.
async function rows() {
  await waitFor(() => expect(document.querySelectorAll('tbody tr').length).toBeGreaterThan(0));
  return Array.from(document.querySelectorAll('tbody tr')).map((r) => r.textContent ?? '');
}

beforeEach(() => vi.clearAllMocks());

describe('AuditPage', () => {
  it('names the actor and what kind of principal they were', async () => {
    renderPage([event('e1')]);

    const [first] = await rows();
    // "user" versus "service" is the difference between a person and an API
    // key doing something, and the trail exists to keep them apart.
    expect(first).toContain('Alice SRE');
    expect(first).toContain('user');
    expect(first).toContain('admin');
  });

  it('shows the action and the entity it touched', async () => {
    renderPage([event('e1')]);

    const [first] = await rows();
    expect(first).toContain('acknowledge');
    expect(first).toContain('alert_group');
    expect(first).toContain('grp-1');
  });

  it('carries the request id, which is how one request is reassembled', async () => {
    renderPage([event('e1')]);

    const [first] = await rows();
    // The same id is on the access log line and in X-Request-ID; without it
    // here, "everything one request did" stops being a single query.
    expect(first).toContain('req-42');
  });

  it('marks an actor with no role rather than leaving it blank', async () => {
    renderPage([event('e1', { actor_role: '', actor_name: '' })]);

    const [first] = await rows();
    expect(first).toContain('no role');
  });

  it('says the trail is empty rather than showing a bare table', async () => {
    renderPage([]);

    expect(await screen.findByText(/no audit events yet/i)).toBeInTheDocument();
  });

  it('reports a failed read instead of an empty trail', async () => {
    get.mockRejectedValue(new Error('audit query timed out'));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <MantineProvider>
        <QueryClientProvider client={client}>
          <MemoryRouter>
            <AuditPage />
          </MemoryRouter>
        </QueryClientProvider>
      </MantineProvider>,
    );

    // An empty trail and an unreadable one look the same, and only one of them
    // means nothing happened.
    expect(await screen.findByText(/audit query timed out/i)).toBeInTheDocument();
  });
});
