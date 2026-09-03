import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { InsightsPage } from './InsightsPage';

// Every number on this page is a count the API was asked for separately, which
// makes two things worth pinning: that a count is asked for with the filter it
// claims to show, and that the integration selector scopes all of them rather
// than only the table underneath.

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

// counts maps a params object to the total this fake reports, so a test can say
// "there are 4 open groups" without caring about call order.
function renderPage(totals: Record<string, number> = {}, history: unknown[] = []) {
  get.mockImplementation((path: string, params?: Record<string, unknown>) => {
    if (path === '/api/v1/history') {
      return Promise.resolve({ items: history, total: history.length, count: history.length });
    }
    if (path === '/api/v1/integrations') {
      return Promise.resolve({ items: [{ id: 'int-1', name: 'prod' }], total: 1 });
    }
    const key = `${path}|${params?.status ?? params?.severity ?? ''}`;
    return Promise.resolve({ items: [], total: totals[key] ?? 0 });
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

// callsFor returns the params of every request made to a path.
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
  it('shows each count under the label it was asked for', async () => {
    renderPage({
      '/api/v1/alert-groups|open': 4,
      '/api/v1/alert-groups|acknowledged': 2,
    });

    // A tile showing the wrong query's number is invisible: it is a plausible
    // integer either way.
    const open = await screen.findByText('Open');
    await waitFor(() => expect(tile(open)).toContain('4'));
    expect(tile(screen.getByText('Acknowledged'))).toContain('2');
  });

  it('counts skipped notifications, which are not a rounding error', async () => {
    renderPage();

    await waitFor(() => expect(callsFor('/api/v1/notifications').length).toBeGreaterThan(0));
    const statuses = callsFor('/api/v1/notifications').map((p) => p.status);

    // Leaving skipped out of the distribution made a deployment where nothing
    // could be delivered look calm — the comment in the page says so, and this
    // is what keeps it true.
    expect(statuses).toContain('skipped');
    expect(statuses).toContain('failed');
    expect(statuses).toContain('delivered');
  });

  it('asks for the whole severity distribution, not a sample of it', async () => {
    renderPage();

    await waitFor(() => expect(callsFor('/api/v1/alert-groups').length).toBeGreaterThan(0));
    const severities = callsFor('/api/v1/alert-groups').map((p) => p.severity);

    for (const severity of ['critical', 'error', 'warning', 'info']) {
      expect(severities).toContain(severity);
    }
  });

  it('sends no integration filter until one is chosen', async () => {
    renderPage();

    await waitFor(() => expect(callsFor('/api/v1/alert-groups').length).toBeGreaterThan(0));

    // An unset selector must not send integration_id= — an empty filter is not
    // the same query as no filter, and the API would read it as an integration
    // whose id is the empty string.
    for (const params of callsFor('/api/v1/alert-groups')) {
      expect(params).not.toHaveProperty('integration_id');
    }
  });

  it('asks for the incident list once, not once per render', async () => {
    renderPage();
    await screen.findByText('Open');
    await new Promise((resolve) => setTimeout(resolve, 200));

    // The range start goes into the query key. Computed fresh on every render it
    // made every render a new query, and every response caused another render —
    // an unbounded refetch loop against the widest read in the service, alert
    // groups with their notifications and delivery attempts inlined. It read as
    // a working page, which is why it survived until a test counted the calls.
    expect(callsFor('/api/v1/history').length).toBeLessThan(3);
  });

  it('says the range is empty rather than drawing nothing', async () => {
    renderPage({}, []);

    expect(await screen.findByText(/no incidents in this range/i)).toBeInTheDocument();
  });
});
