import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import { ReportsPage } from './ReportsPage';

// The page shows what run-report stored. The text in the dialog must be the
// stored summary_text verbatim — the same text e-mail and ChatOps received.

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

function report(id: string, teamId: string | null, late: number, matured: number) {
  return {
    id,
    team_id: teamId,
    period_start: '2026-11-02T00:00:00Z',
    period_end: '2026-11-09T00:00:00Z',
    generated_at: '2026-11-09T06:00:00Z',
    format: 'text',
    ack: { matured, on_time: matured - late, late, pending: 0, late_share: matured ? late / matured : 0 },
    late_episodes: [],
    channels: [],
    summary_text: `Digest ${id}\nLate acknowledgements: ${late}`,
  };
}

function renderPage(items: ReturnType<typeof report>[]) {
  get.mockImplementation((path: string) => {
    if (path === '/api/v1/reports') return Promise.resolve({ items, total: items.length });
    if (path === '/api/v1/teams') {
      return Promise.resolve({ items: [{ id: 'team-1', name: 'Payments' }], total: 1 });
    }
    return Promise.resolve({ items: [], total: 0 });
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider env="test">
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <ReportsPage />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

describe('ReportsPage', () => {
  it('asks for the newest periods first', async () => {
    renderPage([]);
    await screen.findByText(/no reports yet/i);
    expect(get).toHaveBeenCalledWith(
      '/api/v1/reports',
      expect.objectContaining({ sort: 'period_start', order: 'desc' }),
    );
  });

  it('names the team, marks the installation-wide report, and opens the stored text', async () => {
    renderPage([report('r-1', 'team-1', 3, 10), report('r-2', null, 0, 4)]);
    expect(await screen.findByText('Payments')).toBeInTheDocument();
    expect(screen.getByText('Whole installation')).toBeInTheDocument();

    await userEvent.click(screen.getByText('Payments'));
    expect(await screen.findByText(/Digest r-1/)).toBeInTheDocument();
    expect(screen.getByText(/Late acknowledgements: 3/)).toBeInTheDocument();
  });
});
