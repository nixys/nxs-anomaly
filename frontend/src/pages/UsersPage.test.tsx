import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { UsersPage } from './UsersPage';

// This page decides two things that page nobody at 4am: whether a person is on
// duty, and whether they can sign in at all. Both are one click away from each
// other here, and neither had a test.

const get = vi.fn();
const post = vi.fn();
const del = vi.fn();

vi.mock('../api/client', () => ({
  api: {
    get: (...args: unknown[]) => get(...args),
    post: (...args: unknown[]) => post(...args),
    put: vi.fn(),
    patch: vi.fn(),
    delete: (...args: unknown[]) => del(...args),
  },
  ApiError: class ApiError extends Error {},
  getApiKey: () => 'test-key',
}));

vi.mock('@mantine/notifications', () => ({
  notifications: { show: vi.fn(), hide: vi.fn() },
}));

// The identity has to carry permissions: the page reads
// identity?.permissions.admin, so a stand-in without them crashes the row
// render — which is what the first version of this mock did, and is worth
// knowing about the shape rather than working around.
vi.mock('../auth/AuthProvider', () => ({
  useAuth: () => ({
    identity: {
      id: 'usr-me',
      kind: 'user',
      display_name: 'me',
      role: 'admin',
      permissions: { read: true, respond: true, edit: true, admin: true },
      team_scoped: false,
      team_ids: [],
    },
  }),
}));

function user(id: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    name: `Person ${id}`,
    username: id,
    email: `${id}@example.com`,
    role: 'responder',
    priority: 'medium',
    on_duty: false,
    notification_targets: [{ type: 'telegram', target: '4242' }],
    created_at: '2026-06-01T00:00:00Z',
    updated_at: '2026-06-01T00:00:00Z',
    ...overrides,
  };
}

function renderPage(items: ReturnType<typeof user>[] = [user('alice')]) {
  get.mockImplementation((path: string) => {
    if (path.startsWith('/api/v1/users')) {
      return Promise.resolve({ items, total: items.length });
    }
    return Promise.resolve({ items: [], total: 0 });
  });
  post.mockResolvedValue({});
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <UsersPage />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

beforeEach(() => vi.clearAllMocks());

describe('UsersPage', () => {
  it('lists the people the API returned', async () => {
    renderPage([user('alice'), user('bob')]);

    expect(await screen.findByText('Person alice')).toBeInTheDocument();
    expect(screen.getByText('Person bob')).toBeInTheDocument();
  });

  it('shows an empty state rather than a bare table', async () => {
    renderPage([]);

    // A fresh installation lands here first. An empty table with headers reads
    // as "loading forever"; the empty state says the list is genuinely empty.
    expect(await screen.findByText(/no users yet/i)).toBeInTheDocument();
  });

  it('renders the role, because a person without one cannot sign in', async () => {
    renderPage([user('alice', { role: 'admin' }), user('nobody', { role: '' })]);

    expect(await screen.findByText('admin')).toBeInTheDocument();
    // The person with no role must be visibly different — they are a paging
    // target, not an account, and the difference is invisible in the name.
    expect(screen.getByText(/roster only/i)).toBeInTheDocument();
  });

  it('puts somebody on duty through the duty-on endpoint', async () => {
    renderPage([user('alice')]);
    const toggle = await screen.findByLabelText('Toggle duty for Person alice');

    await userEvent.click(toggle);

    // duty-on and duty-off are separate routes: posting the wrong one silently
    // leaves the rota unchanged while the switch appears to have moved.
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/v1/users/alice/duty-on'));
  });

  it('takes somebody off duty through the duty-off endpoint', async () => {
    renderPage([user('alice', { on_duty: true })]);
    const toggle = await screen.findByLabelText('Toggle duty for Person alice');

    await userEvent.click(toggle);

    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/v1/users/alice/duty-off'));
  });

  it('surfaces a failed list instead of showing an empty one', async () => {
    get.mockRejectedValue(new Error('database is down'));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <MantineProvider>
        <QueryClientProvider client={client}>
          <MemoryRouter>
            <UsersPage />
          </MemoryRouter>
        </QueryClientProvider>
      </MantineProvider>,
    );

    // "No users" and "the query failed" look identical on screen if the error
    // is swallowed, and the first reads as a working system with nobody in it.
    expect(await screen.findByText(/database is down/i)).toBeInTheDocument();
  });
});
