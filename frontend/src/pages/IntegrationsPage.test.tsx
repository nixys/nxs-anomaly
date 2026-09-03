import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { IntegrationsPage, ingestUrl } from './IntegrationsPage';

// The ingestion URL shown on this page is what an operator pastes into
// Alertmanager. If it is wrong, alerts never arrive and nothing in the system
// reports a problem — there is no failed delivery to see, because there was no
// alert. That makes ingestUrl worth testing on its own.

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
}));

vi.mock('@mantine/notifications', () => ({
  notifications: { show: vi.fn(), hide: vi.fn() },
}));

function integration(id: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    name: `Integration ${id}`,
    type: 'webhook',
    key: `key_${id}`,
    routes: [{ id: 'r1', is_default: true }],
    team_id: '',
    created_at: '2026-06-01T00:00:00Z',
    updated_at: '2026-06-01T00:00:00Z',
    ...overrides,
  };
}

function renderPage(items = [integration('a')]) {
  get.mockImplementation((path: string) => {
    if (path === '/api/v1/integrations') return Promise.resolve({ items, total: items.length });
    return Promise.resolve({ items: [], total: 0 });
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <IntegrationsPage />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

beforeEach(() => vi.clearAllMocks());

describe('ingestUrl', () => {
  it('builds the per-source path the API actually routes', () => {
    // The route is /integrations/v1/<source>/<key>; dropping the source segment
    // yields a 404 that looks like a bad key.
    expect(ingestUrl(integration('a') as never)).toBe(
      `${window.location.origin}/integrations/v1/webhook/key_a`,
    );
  });

  it('keeps a known non-webhook source in the path', () => {
    expect(ingestUrl(integration('b', { type: 'alertmanager' }) as never)).toBe(
      `${window.location.origin}/integrations/v1/alertmanager/key_b`,
    );
  });

  it('falls back to webhook for a source the API does not route', () => {
    // A stored type the frontend does not know about must not produce a URL that
    // 404s; webhook is the generic endpoint.
    expect(ingestUrl(integration('c', { type: 'something-else' }) as never)).toBe(
      `${window.location.origin}/integrations/v1/webhook/key_c`,
    );
  });
});

describe('IntegrationsPage', () => {
  it('lists integrations with their ingestion URL', async () => {
    renderPage();
    expect(await screen.findByText('Integration a')).toBeInTheDocument();
    expect(
      screen.getByText(`${window.location.origin}/integrations/v1/webhook/key_a`),
    ).toBeInTheDocument();
  });

  it('shows an empty state rather than a bare table', async () => {
    renderPage([]);
    // Any empty-state copy is fine; a blank table with headers is not.
    await waitFor(() => expect(screen.queryByText('Integration a')).not.toBeInTheDocument());
    expect(screen.getByRole('button', { name: /Add integration/i })).toBeInTheDocument();
  });

  it('deletes only after the confirmation step', async () => {
    const user = userEvent.setup();
    del.mockResolvedValue({});
    renderPage();
    await screen.findByText('Integration a');

    await user.click(screen.getByRole('button', { name: 'Delete Integration a' }));

    // The first click only opens the confirmation — an integration is the thing
    // alerts arrive through, and removing one silently stops ingestion.
    expect(del).not.toHaveBeenCalled();
    expect(await screen.findByText('Confirm deletion')).toBeInTheDocument();

    // A plain string `name` is already an exact match, which is what keeps this
    // from picking up the "Delete Integration a" row button.
    await user.click(screen.getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(del).toHaveBeenCalledWith('/api/v1/integrations/a'));
  });

  it('opens the create form', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByText('Integration a');

    await user.click(screen.getByRole('button', { name: /Add integration/i }));

    expect(await screen.findByRole('textbox', { name: /Name/i })).toBeInTheDocument();
  });
});
