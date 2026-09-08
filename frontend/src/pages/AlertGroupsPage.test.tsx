import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AlertGroupsPage } from './AlertGroupsPage';

// This is the page a responder actually looks at during an incident, and it was
// untested. The bits worth pinning are the ones that act on several incidents at
// once: a bulk button that fires with nothing selected, or that keeps a stale
// selection after acting, silences the wrong incidents.

const get = vi.fn();
const post = vi.fn();

vi.mock('../api/client', () => ({
  api: {
    get: (...args: unknown[]) => get(...args),
    post: (...args: unknown[]) => post(...args),
    put: vi.fn(),
    patch: vi.fn(),
    delete: vi.fn(),
  },
  ApiError: class ApiError extends Error {},
}));

// The mutation hooks show a toast on success; jsdom has no notification system.
vi.mock('@mantine/notifications', () => ({
  notifications: { show: vi.fn(), hide: vi.fn() },
}));

function group(id: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    integration_id: 'int_1',
    title: `Incident ${id}`,
    status: 'open',
    severity: 'critical',
    labels: {},
    alert_count: 1,
    current_step: 0,
    repeat_count: 0,
    created_at: '2026-06-01T00:00:00Z',
    updated_at: '2026-06-01T00:00:00Z',
    last_received_at: '2026-06-01T00:00:00Z',
    ...overrides,
  };
}

const integrations = [{ id: 'int_1', name: 'Prometheus prod', type: 'webhook' }];

// MemoryRouter keeps its history to itself, so the assertion needs the router's
// own view of the address rather than window.location.
function LocationProbe() {
  const location = useLocation();
  return <span data-testid="search">{location.search}</span>;
}

function renderPage(groups = [group('grp_1'), group('grp_2')], entries = ['/alert-groups']) {
  get.mockImplementation((path: string) => {
    if (path === '/api/v1/integrations') {
      return Promise.resolve({ items: integrations, total: integrations.length });
    }
    if (path === '/api/v1/alert-groups') {
      return Promise.resolve({ items: groups, total: groups.length });
    }
    return Promise.resolve({ items: [], total: 0 });
  });
  post.mockResolvedValue({ updated: groups.length });

  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={entries}>
          <AlertGroupsPage />
          <LocationProbe />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

beforeEach(() => vi.clearAllMocks());

describe('AlertGroupsPage', () => {
  // A bare /alert-groups is somebody arriving mid-incident, so it opens on the
  // firing queue rather than on everything ever recorded.
  it('opens on the firing queue when the address carries nothing', async () => {
    renderPage();
    await waitFor(() =>
      expect(get).toHaveBeenCalledWith(
        '/api/v1/alert-groups',
        expect.objectContaining({ status: 'open', sort: 'severity' }),
      ),
    );
    expect(screen.getByTestId('search').textContent).toContain('view=firing');
  });

  it('lists the groups it was given', async () => {
    renderPage();
    expect(await screen.findByText('Incident grp_1')).toBeInTheDocument();
    expect(screen.getByText('Incident grp_2')).toBeInTheDocument();
  });

  // Two different emptinesses, two different next moves. Telling somebody with
  // no integrations that "no groups match these filters" sends them looking for
  // a filter they never set.
  it('separates "nothing matched" from "nothing exists yet"', async () => {
    renderPage([], ['/alert-groups?view=all']);
    expect(await screen.findByText(/No alert groups yet/i)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Connect a source/i })).toBeInTheDocument();
  });

  it('offers to clear the filters when the filters are what emptied the list', async () => {
    renderPage([], ['/alert-groups?severity=critical']);
    expect(await screen.findByText(/No alert groups match these filters/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Clear the filters/i })).toBeInTheDocument();
  });

  it('shows no bulk actions at all until something is selected', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText('Incident grp_1');

    // Three permanently disabled buttons taught people to read the strip as
    // decoration; now the strip is the selection, and it only exists when there
    // is one.
    expect(screen.queryByRole('button', { name: 'Acknowledge' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Resolve' })).toBeNull();

    await user.click(within(screen.getAllByRole('row')[1]).getByRole('checkbox'));
    expect(await screen.findByRole('button', { name: 'Acknowledge' })).toBeEnabled();
  });

  it('sends only the selected group ids to the bulk endpoint', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText('Incident grp_1');

    const rows = screen.getAllByRole('row');
    // rows[0] is the header; select the first data row only.
    await user.click(within(rows[1]).getByRole('checkbox'));

    const resolve = screen.getByRole('button', { name: 'Resolve' });
    await waitFor(() => expect(resolve).toBeEnabled());
    await user.click(resolve);

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [path, body] = post.mock.calls[0];
    expect(path).toBe('/api/v1/alert-groups/bulk-resolve');
    expect(body).toEqual({ group_ids: ['grp_1'] });
  });

  it('selects and clears every row from the header checkbox', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText('Incident grp_1');

    const header = screen.getAllByRole('row')[0];
    await user.click(within(header).getByRole('checkbox'));
    expect(await screen.findByText('2 selected')).toBeInTheDocument();

    await user.click(within(screen.getAllByRole('row')[0]).getByRole('checkbox'));
    await waitFor(() => expect(screen.getByText('2 groups')).toBeInTheDocument());
  });

  it('clears the selection after a bulk action so the next one cannot reuse it', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText('Incident grp_1');

    await user.click(within(screen.getAllByRole('row')[0]).getByRole('checkbox'));
    expect(await screen.findByText('2 selected')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Acknowledge' }));

    // The bar goes away with the selection — otherwise the next click would
    // silently act on incidents the responder already handled.
    await waitFor(() => expect(screen.queryByText('2 selected')).toBeNull());
    expect(screen.queryByRole('button', { name: 'Resolve' })).toBeNull();
  });

  it('passes a silence duration through with the bulk call', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText('Incident grp_1');

    await user.click(within(screen.getAllByRole('row')[1]).getByRole('checkbox'));
    await user.click(screen.getByRole('button', { name: /Silence/ }));
    await user.click(await screen.findByText('1 hour'));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [path, body] = post.mock.calls[0];
    expect(path).toBe('/api/v1/alert-groups/bulk-silence');
    expect(body).toEqual({ group_ids: ['grp_1'], duration_minutes: 60 });
  });

  it('re-queries with the chosen status filter', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText('Incident grp_1');

    // Mantine's Select renders a visible input plus a hidden one behind the same
    // label, so the label alone is ambiguous.
    await user.click(screen.getByRole('textbox', { name: 'Status' }));
    await user.click(await screen.findByRole('option', { name: 'acknowledged' }));

    await waitFor(() =>
      expect(get).toHaveBeenCalledWith(
        '/api/v1/alert-groups',
        expect.objectContaining({ status: 'acknowledged' }),
      ),
    );
  });

  it('resolves integration ids to names', async () => {
    renderPage();
    expect(await screen.findAllByText('Prometheus prod')).not.toHaveLength(0);
  });

  // The filter state is the thing a responder hands over mid-incident. Keeping
  // it in component state meant the link they pasted showed the whole queue.
  it('puts the chosen filter in the address bar', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText('Incident grp_1');

    await user.click(screen.getByRole('textbox', { name: 'Status' }));
    await user.click(await screen.findByRole('option', { name: 'acknowledged' }));

    await waitFor(() =>
      expect(screen.getByTestId('search').textContent).toContain('status=acknowledged'),
    );
  });

  it('reads its filters back out of the address bar', async () => {
    renderPage([group('grp_1')], ['/alert-groups?status=resolved&severity=critical']);

    await waitFor(() =>
      expect(get).toHaveBeenCalledWith(
        '/api/v1/alert-groups',
        expect.objectContaining({ status: 'resolved', severity: 'critical' }),
      ),
    );
  });

  // A view is a set of filters with a name, so picking one has to change what is
  // queried — a tab that only highlights itself is a lie.
  it('applies a saved view to the query', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText('Incident grp_1');

    await user.click(screen.getByRole('tab', { name: 'Critical' }));

    await waitFor(() =>
      expect(get).toHaveBeenCalledWith(
        '/api/v1/alert-groups',
        expect.objectContaining({ status: 'open', severity: 'critical' }),
      ),
    );
  });

  // The severity filter offers levels; the badge shows the word the source sent.
  // "high" is the case that used to be uncolourable, unfilterable and sorted
  // below debug.
  it('offers one option per severity level, not per spelling', async () => {
    const user = userEvent.setup();
    renderPage([group('grp_1', { severity: 'high' })]);
    await screen.findByText('Incident grp_1');

    expect(await screen.findByText('high')).toBeInTheDocument();

    await user.click(screen.getByRole('textbox', { name: 'Severity' }));
    expect(await screen.findByRole('option', { name: 'error' })).toBeInTheDocument();
    expect(screen.queryByRole('option', { name: 'high' })).toBeNull();
  });

  it('acknowledges the incident under the keyboard cursor', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText('Incident grp_1');

    // j moves onto the second incident, a acknowledges it — no pointer involved.
    await user.keyboard('j');
    await user.keyboard('a');

    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post.mock.calls[0][0]).toBe('/api/v1/alert-groups/grp_2/acknowledge');
  });
});
