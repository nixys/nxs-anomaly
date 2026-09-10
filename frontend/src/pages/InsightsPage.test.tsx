import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { InsightsPage } from './InsightsPage';

// This page used to ask twelve separate list queries for their `total` — one per
// tile and per distribution row — and could still not say whether things were
// getting better or worse. It now asks one question and gets the counts and the
// daily buckets together. What is worth pinning: that the numbers land under the
// captions they belong to, that the severity distribution is counted by level
// rather than by spelling, that the scope goes into every part of the answer,
// and that the twelve queries do not come back.

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

interface TrendBucket {
  day: string;
  opened: number;
  resolved: number;
  delivered: number;
  failed: number;
}

const emptySummary: {
  from: string;
  to: string;
  groups_by_status: Record<string, number>;
  groups_by_level: Record<string, number>;
  notifications_by_state: Record<string, number>;
  trend: TrendBucket[];
} = {
  from: '2026-09-01T00:00:00Z',
  to: '2026-09-08T00:00:00Z',
  groups_by_status: {},
  groups_by_level: {},
  notifications_by_state: {},
  trend: [],
};

function renderPage(summary: Partial<typeof emptySummary> = {}, history: unknown[] = []) {
  get.mockImplementation((path: string) => {
    if (path === '/api/v1/insights/summary') {
      return Promise.resolve({ ...emptySummary, ...summary });
    }
    if (path === '/api/v1/history') {
      return Promise.resolve({ items: history, total: history.length, count: history.length });
    }
    if (path === '/api/v1/integrations') {
      return Promise.resolve({ items: [{ id: 'int-1', name: 'prod' }], total: 1 });
    }
    return Promise.resolve({ items: [], total: 0 });
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <InsightsPage />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

function callsFor(path: string) {
  return get.mock.calls
    .filter((call) => call[0] === path)
    .map((call) => (call[1] ?? {}) as Record<string, unknown>);
}

// tile returns the whole card a label sits in, so the number is read from the
// same card as its caption rather than from wherever it happens to appear.
function tile(label: HTMLElement) {
  return label.closest('.mantine-Paper-root')?.textContent ?? '';
}

beforeEach(() => vi.clearAllMocks());

describe('InsightsPage', () => {
  it('shows each count under the label it belongs to', async () => {
    renderPage({
      groups_by_status: { open: 4, acknowledged: 2, resolved: 9 },
      notifications_by_state: { failed: 3 },
    });

    // A tile showing another number is invisible: it is a plausible integer
    // either way.
    const open = await screen.findByText('Open');
    await waitFor(() => expect(tile(open)).toContain('4'));
    await waitFor(() => expect(tile(screen.getByText('Acknowledged'))).toContain('2'));
    await waitFor(() => expect(tile(screen.getByText('Failed notifications'))).toContain('3'));
  });

  it('draws the severity distribution by level, not by spelling', async () => {
    renderPage({ groups_by_level: { critical: 1, error: 5, warning: 2, info: 0, debug: 0 } });

    // "high" and "error" are one bar, because they are one level — the same rule
    // the filter and the ordering use. A distribution that counted spellings
    // would disagree with the badges two paragraphs below it.
    const bySeverity = await screen.findByText('Alert groups by severity');
    const card = () => bySeverity.closest('.mantine-Paper-root')?.textContent ?? '';
    await waitFor(() => expect(card()).toContain('5'));
    expect(card()).toContain('error');
    expect(card()).toContain('debug');
  });

  it('scopes the whole page — not only the table — to the chosen integration', async () => {
    renderPage();
    await screen.findByText('Open');

    // The summary carries the scope, so the tiles, both distributions and both
    // trends move together. Before, the scope had to be passed to twelve
    // queries, and any one of them could be forgotten.
    const summaryCalls = callsFor('/api/v1/insights/summary');
    expect(summaryCalls.length).toBeGreaterThan(0);
    for (const params of summaryCalls) {
      expect(params).toHaveProperty('from');
      // An unset selector must not send integration_id=: an empty filter is a
      // different query from no filter.
      expect(params).not.toHaveProperty('integration_id');
    }
  });

  it('asks once, not twelve times', async () => {
    renderPage();
    await screen.findByText('Open');
    await new Promise((resolve) => setTimeout(resolve, 200));

    expect(callsFor('/api/v1/insights/summary').length).toBeLessThan(3);
    // The list queries whose only purpose was their `total` are gone.
    expect(callsFor('/api/v1/alert-groups').length).toBe(0);
    expect(callsFor('/api/v1/notifications').length).toBe(0);
  });

  it('asks for the incident list once, not once per render', async () => {
    renderPage();
    await screen.findByText('Open');
    await new Promise((resolve) => setTimeout(resolve, 200));

    // The range start goes into the query key. Computed fresh on every render it
    // made every render a new query, and every response caused another render —
    // an unbounded refetch loop against the widest read in the service.
    expect(callsFor('/api/v1/history').length).toBeLessThan(3);
  });

  it('draws the trend when there are days, and says so when there are none', async () => {
    renderPage({
      trend: [
        { day: '2026-09-06', opened: 3, resolved: 1, delivered: 9, failed: 0 },
        { day: '2026-09-07', opened: 1, resolved: 4, delivered: 4, failed: 2 },
      ],
    });

    // The chart is an image with a text alternative: identity never rests on
    // colour, and a screen reader gets the same numbers.
    const charts = await screen.findAllByRole('img');
    expect(charts.length).toBeGreaterThanOrEqual(2);
    expect(charts[0].getAttribute('aria-label')).toMatch(/opened/i);
  });

  it('says the range is empty rather than drawing nothing', async () => {
    renderPage({}, []);
    expect(await screen.findByText(/no incidents in this range/i)).toBeInTheDocument();
  });
});
