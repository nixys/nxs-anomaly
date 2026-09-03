import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { MaintenancePage } from './MaintenancePage';

// A window suppresses pages. The page's job is to make it obvious which windows
// are suppressing something right now and what they cover, because "the alert
// never arrived" and "the alert was silenced on purpose" look identical from
// everywhere else in the product.

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

vi.mock('@mantine/notifications', () => ({
  notifications: { show: vi.fn(), hide: vi.fn() },
}));

function windowAt(id: string, fromHours: number, toHours: number, overrides = {}) {
  const now = Date.now();
  return {
    id,
    name: `window ${id}`,
    reason: '',
    team_id: null,
    integration_ids: ['int-1'],
    starts_at: new Date(now + fromHours * 3600_000).toISOString(),
    ends_at: new Date(now + toHours * 3600_000).toISOString(),
    created_at: '2026-06-01T00:00:00Z',
    updated_at: '2026-06-01T00:00:00Z',
    ...overrides,
  };
}

function renderPage(items: ReturnType<typeof windowAt>[]) {
  get.mockImplementation((path: string) => {
    if (path === '/api/v1/maintenance-windows') {
      return Promise.resolve({ items, total: items.length });
    }
    if (path === '/api/v1/integrations') {
      return Promise.resolve({
        items: [
          { id: 'int-1', name: 'prod exporter' },
          { id: 'int-2', name: 'staging' },
        ],
        total: 2,
      });
    }
    return Promise.resolve({ items: [], total: 0 });
  });
  post.mockResolvedValue(windowAt('new', 0, 1));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <MaintenancePage />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

async function rows() {
  await waitFor(() => expect(document.querySelectorAll('tbody tr').length).toBeGreaterThan(0));
  return Array.from(document.querySelectorAll('tbody tr')).map((r) => r.textContent ?? '');
}

beforeEach(() => vi.clearAllMocks());

describe('MaintenancePage', () => {
  it('separates a window suppressing pages now from one that is not', async () => {
    renderPage([
      windowAt('now', -1, 1),
      windowAt('later', 24, 26),
      windowAt('done', -48, -24),
    ]);

    const [active, planned, finished] = await rows();
    // This is the only question anybody has when an alert did not arrive, and
    // three timestamps do not answer it at a glance.
    expect(active).toContain('active');
    expect(planned).toContain('planned');
    expect(finished).toContain('finished');
  });

  it('names the integrations a window covers', async () => {
    renderPage([windowAt('now', -1, 1, { integration_ids: ['int-1', 'int-2'] })]);

    const [first] = await rows();
    // Raw ids here would make the blast radius unreadable at the moment it
    // matters — which is while the window is open and something is quiet.
    expect(first).toContain('prod exporter');
    expect(first).toContain('staging');
  });

  it('says nothing is planned rather than showing an empty table', async () => {
    renderPage([]);

    expect(await screen.findByText(/no maintenance planned/i)).toBeInTheDocument();
  });

  it('will not plan a window that covers no integration', async () => {
    const user = userEvent.setup();
    renderPage([]);
    await user.click(await screen.findByRole('button', { name: /plan window/i }));

    await user.type(await screen.findByLabelText('Name'), 'Database failover');
    await user.type(screen.getByLabelText('From'), '2026-09-01T01:00');
    await user.type(screen.getByLabelText('Until'), '2026-09-01T03:00');

    // "Covers nothing" and "covers everything" are one typo apart, and the
    // second one silences the deployment with no symptom but silence.
    const buttons = screen.getAllByRole('button', { name: /plan window/i });
    expect(buttons[buttons.length - 1]).toBeDisabled();
  });

  it('sends the window in UTC, whatever the browser is set to', async () => {
    const user = userEvent.setup();
    renderPage([]);
    await user.click(await screen.findByRole('button', { name: /plan window/i }));

    await user.type(await screen.findByLabelText('Name'), 'Database failover');
    await user.type(screen.getByLabelText('From'), '2026-09-01T01:00');
    await user.type(screen.getByLabelText('Until'), '2026-09-01T03:00');
    await user.click(screen.getByRole('textbox', { name: 'Integrations' }));
    await user.click(await screen.findByText('prod exporter'));

    const buttons = screen.getAllByRole('button', { name: /plan window/i });
    const submit = buttons[buttons.length - 1];
    await waitFor(() => expect(submit).not.toBeDisabled());
    await user.click(submit);

    await waitFor(() => expect(post).toHaveBeenCalled());
    const body = post.mock.calls[0][1] as Record<string, unknown>;
    // datetime-local carries no zone. Sending the literal string would mean a
    // window typed as 01:00 local suppresses an hour that is not the hour the
    // maintenance happens in — for everyone west or east of whoever typed it.
    expect(String(body.starts_at)).toMatch(/Z$/);
    expect(body.integration_ids).toEqual(['int-1']);
  });
});
