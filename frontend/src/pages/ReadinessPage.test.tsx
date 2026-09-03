import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ReadinessPage } from './ReadinessPage';

// This page answers one question — "if an alert arrived right now, would anybody
// actually be paged?" — and the dangerous way for it to be wrong is optimism: a
// deployment that is not ready must not read as ready.

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

// The acknowledgement control asks who is looking before it decides whether to
// offer itself, so the page needs an identity even when there is nothing to
// acknowledge: the hook runs before the early return.
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

function readiness(overrides: Record<string, unknown> = {}) {
  return {
    production_ready: true,
    blockers: 0,
    checks: [
      {
        key: 'database',
        title: 'Database reachable',
        detail: 'The store answered a ping.',
        severity: 'ok',
      },
    ],
    acknowledgement: null,
    ...overrides,
  };
}

function renderPage(data: Record<string, unknown>) {
  get.mockImplementation((path: string) => {
    if (path === '/api/v1/readiness') return Promise.resolve(data);
    return Promise.resolve({ items: [], total: 0 });
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <ReadinessPage />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

beforeEach(() => vi.clearAllMocks());

describe('ReadinessPage', () => {
  it('renders each check with its detail', async () => {
    renderPage(
      readiness({
        checks: [
          { key: 'database', title: 'Database reachable', detail: 'The store answered a ping.', severity: 'ok' },
          { key: 'worker', title: 'Worker is cycling', detail: 'Last cycle 3s ago.', severity: 'ok' },
        ],
      }),
    );

    expect(await screen.findByText('Database reachable')).toBeInTheDocument();
    expect(screen.getByText('Worker is cycling')).toBeInTheDocument();
    // The detail is the part that says *why* a check passed or failed; a title
    // alone turns this page into a row of coloured dots.
    expect(screen.getByText('Last cycle 3s ago.')).toBeInTheDocument();
  });

  it('says plainly when the deployment is not ready', async () => {
    renderPage(
      readiness({
        production_ready: false,
        blockers: 2,
        checks: [
          {
            key: 'responders',
            title: 'Every on-call user is reachable',
            detail: 'Two users have no notification target.',
            severity: 'blocker',
          },
        ],
      }),
    );

    expect(await screen.findByText(/not ready/i)).toBeInTheDocument();
    expect(screen.getByText('blocked')).toBeInTheDocument();
  });

  it('reports a ready deployment as allowed', async () => {
    renderPage(readiness());

    expect(await screen.findByText('allowed')).toBeInTheDocument();
  });

  it('does not claim readiness when the check itself could not run', async () => {
    get.mockRejectedValue(new Error('readiness endpoint unreachable'));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <MantineProvider>
        <QueryClientProvider client={client}>
          <MemoryRouter>
            <ReadinessPage />
          </MemoryRouter>
        </QueryClientProvider>
      </MantineProvider>,
    );

    // A check that could not run is a blocker, not a pass. Swallowing the error
    // here would leave the page showing nothing at all — which reads as calm.
    expect(await screen.findByText(/readiness endpoint unreachable/i)).toBeInTheDocument();
    expect(screen.queryByText('allowed')).not.toBeInTheDocument();
  });
});
