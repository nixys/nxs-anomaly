import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { EscalationChainsPage } from './EscalationChainsPage';

// A chain is the answer to "who gets woken, in what order, and how long do we
// wait between". This page is where that answer is read, and the two states it
// has to distinguish are a chain that pages somebody and a chain that pages
// nobody — which look identical if the step list is not rendered.

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

function chain(id: string, steps: Record<string, unknown>[] = []) {
  return {
    id,
    name: `Chain ${id}`,
    steps,
    created_at: '2026-06-01T00:00:00Z',
    updated_at: '2026-06-01T00:00:00Z',
  };
}

function renderPage(items: ReturnType<typeof chain>[]) {
  get.mockImplementation((path: string) => {
    if (path.startsWith('/api/v1/escalation-chains')) {
      return Promise.resolve({ items, total: items.length });
    }
    return Promise.resolve({ items: [], total: 0 });
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <EscalationChainsPage />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

beforeEach(() => vi.clearAllMocks());

describe('EscalationChainsPage', () => {
  it('lists chains with how many steps each has', async () => {
    renderPage([
      chain('a', [{ id: 's1', kind: 'NOTIFY_SCHEDULE' }, { id: 's2', kind: 'WAIT' }]),
    ]);

    expect(await screen.findByText('Chain a')).toBeInTheDocument();
    expect(screen.getByText('2 steps')).toBeInTheDocument();
  });

  it('reads the chain as a sentence, in order, because the order is the escalation', async () => {
    renderPage([
      chain('a', [
        { id: 's1', kind: 'NOTIFY_SCHEDULE', schedule_ids: ['sch-1'] },
        { id: 's2', kind: 'WAIT', delay_minutes: 5 },
        { id: 's3', kind: 'NOTIFY_TEAM', team_ids: ['team-1'] },
      ]),
    ]);

    // The card used to print the wire names of the step kinds — the constants
    // from the source — with the one fact that matters missing: who. A chain
    // read out of order is a different chain, so the order still has to be
    // visible, now as the order of the phrases.
    const card = (await screen.findByText('Chain a')).closest('.mantine-Paper-root');
    const text = card?.textContent ?? '';
    expect(text).toContain('page whoever is on call in');
    expect(text).toContain('wait 5 min');
    expect(text).toContain('page the');
    expect(text.indexOf('wait 5 min')).toBeGreaterThan(text.indexOf('page whoever is on call in'));
    expect(text).not.toContain('NOTIFY_SCHEDULE');
  });

  it('says outright when a chain reaches nobody', async () => {
    renderPage([chain('a', [{ id: 's1', kind: 'NOTIFY_USER', user_ids: [] }])]);

    // A chain whose notifying steps name nobody looks exactly like a working one
    // until an incident proves otherwise.
    expect(await screen.findByText(/reaches nobody/i)).toBeInTheDocument();
  });

  it('says a chain with no steps is a draft', async () => {
    renderPage([chain('empty')]);

    // A chain with no steps pages nobody. Rendering it like any other chain
    // would make "attached and silent" look exactly like "attached and working".
    expect(await screen.findByText(/draft chain/i)).toBeInTheDocument();
    expect(screen.getByText('0 steps')).toBeInTheDocument();
  });

  it('shows an empty state on a fresh installation', async () => {
    renderPage([]);

    expect(await screen.findByText(/no escalation chains yet/i)).toBeInTheDocument();
  });

  it('surfaces a failed query instead of an empty list', async () => {
    get.mockRejectedValue(new Error('chains unreachable'));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <MantineProvider>
        <QueryClientProvider client={client}>
          <MemoryRouter>
            <EscalationChainsPage />
          </MemoryRouter>
        </QueryClientProvider>
      </MantineProvider>,
    );

    expect(await screen.findByText(/chains unreachable/i)).toBeInTheDocument();
  });
});
