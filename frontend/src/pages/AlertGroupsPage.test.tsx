import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
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

function renderPage(groups = [group('grp_1'), group('grp_2')]) {
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
        <MemoryRouter>
          <AlertGroupsPage />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

beforeEach(() => vi.clearAllMocks());

describe('AlertGroupsPage', () => {
  it('lists the groups it was given', async () => {
    renderPage();
    expect(await screen.findByText('Incident grp_1')).toBeInTheDocument();
    expect(screen.getByText('Incident grp_2')).toBeInTheDocument();
  });

  it('says so plainly when nothing matches the filters', async () => {
    renderPage([]);
    expect(await screen.findByText(/No alert groups match these filters/i)).toBeInTheDocument();
  });

  it('keeps the bulk actions disabled until something is selected', async () => {
    renderPage();
    await screen.findByText('Incident grp_1');

    // A bulk action that fires with an empty selection would either do nothing
    // or, worse, act on everything.
    expect(screen.getByRole('button', { name: 'Acknowledge' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Resolve' })).toBeDisabled();
    expect(screen.getByRole('button', { name: /Silence/ })).toBeDisabled();
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

    // Back to the count, not a stale "2 selected" — otherwise the next click
    // would silently act on incidents the responder already handled.
    await waitFor(() => expect(screen.getByText('2 groups')).toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'Resolve' })).toBeDisabled();
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
});
