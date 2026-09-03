import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ScheduleDetailPage } from './ScheduleDetailPage';

// The page is driven entirely by the API client, so the client is the seam:
// stubbing fetch would also test URL building, which belongs to the client's
// own tests.
const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
const del = vi.fn();

vi.mock('../api/client', () => ({
  api: {
    get: (...args: unknown[]) => get(...args),
    post: (...args: unknown[]) => post(...args),
    put: (...args: unknown[]) => put(...args),
    patch: (...args: unknown[]) => put(...args),
    delete: (...args: unknown[]) => del(...args),
  },
  ApiError: class ApiError extends Error {},
}));

const schedule = {
  id: 'sch_1',
  name: 'Primary',
  timezone: 'Europe/Berlin',
  enabled: true,
  notify_on_shift_change: false,
  team_id: null,
  rotation: {
    enabled: true,
    start_at: '2026-06-01T07:00:00Z',
    handoff_interval: 1,
    handoff_unit: 'weeks' as const,
    participant_ids: ['u_a', 'u_b'],
    restriction: null,
  },
  shifts: [],
  overrides: [
    {
      id: 'ovr_1',
      user_id: 'u_b',
      start_at: '2026-06-10T00:00:00Z',
      until: '2026-06-11T00:00:00Z',
      reason: 'conference',
      created_by: { id: 'usr_admin', kind: 'user', name: 'Admin' },
    },
  ],
};

const users = [
  { id: 'u_a', name: 'Alice' },
  { id: 'u_b', name: 'Bob' },
];

const preview = {
  schedule_id: 'sch_1',
  timezone: 'Europe/Berlin',
  from: '2026-06-01T00:00:00Z',
  to: '2026-06-29T00:00:00Z',
  segments: [
    {
      start: '2026-06-01T00:00:00Z',
      end: '2026-06-08T00:00:00Z',
      user_ids: ['u_a'],
      source: 'rotation' as const,
      duration_seconds: 7 * 24 * 3600,
    },
    {
      start: '2026-06-08T00:00:00Z',
      end: '2026-06-09T00:00:00Z',
      user_ids: [],
      source: '' as const,
      duration_seconds: 24 * 3600,
    },
  ],
  gaps: [
    {
      start: '2026-06-08T00:00:00Z',
      end: '2026-06-09T00:00:00Z',
      user_ids: [],
      source: '' as const,
      duration_seconds: 24 * 3600,
    },
  ],
  overlaps: [],
  warnings: ['1 coverage gap(s) in the previewed window'],
  coverage_ratio: 0.96,
  participants: [{ id: 'u_a', name: 'Alice' }],
  unknown_users: ['u_ghost'],
};

const onCall = {
  schedule_id: 'sch_1',
  at: '2026-06-02T00:00:00Z',
  user_ids: ['u_a'],
  users: [{ id: 'u_a', name: 'Alice' }],
  source: 'rotation',
  next: {
    start: '2026-06-08T00:00:00Z',
    end: '2026-06-15T00:00:00Z',
    user_ids: ['u_b'],
    source: 'rotation' as const,
    duration_seconds: 7 * 24 * 3600,
  },
};

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/schedules/sch_1']}>
          <Routes>
            <Route path="/schedules/:id" element={<ScheduleDetailPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  get.mockImplementation((path: string) => {
    if (path === '/api/v1/schedules/sch_1') return Promise.resolve(schedule);
    if (path === '/api/v1/schedules/sch_1/preview') return Promise.resolve(preview);
    if (path === '/api/v1/schedules/sch_1/on-call') return Promise.resolve(onCall);
    if (path === '/api/v1/users') return Promise.resolve({ items: users });
    return Promise.resolve({ items: [] });
  });
});

describe('ScheduleDetailPage', () => {
  it('shows who is on call now and who is next', async () => {
    renderPage();
    // "Alice" also appears in the participant picker, so anchor on the
    // on-call badge and the handoff line instead of the bare name.
    expect(await screen.findByText('via rotation')).toBeInTheDocument();
    expect(screen.getAllByText('Alice').length).toBeGreaterThan(0);
    // Both names also appear in the participant and override pickers, so the
    // assertion is that the handoff line names Bob, not that Bob is unique.
    const next = screen.getByText('Next').closest('div') as HTMLElement;
    expect(within(next).getByText('Bob')).toBeInTheDocument();
    expect(within(next).getByText(/from/)).toBeInTheDocument();
  });

  it('surfaces coverage warnings and unknown participants', async () => {
    renderPage();
    expect(await screen.findByText('Coverage warnings')).toBeInTheDocument();
    expect(screen.getByText('1 coverage gap(s) in the previewed window')).toBeInTheDocument();
    expect(screen.getByText('Unknown participants')).toBeInTheDocument();
    expect(screen.getByText(/u_ghost/)).toBeInTheDocument();
  });

  it('marks uncovered intervals in the four-week preview', async () => {
    renderPage();
    expect(await screen.findByText('gap')).toBeInTheDocument();
    expect(screen.getByText(/^7\s*(day|days)$/i)).toBeInTheDocument();
  });

  it('renders preview times in the schedule timezone', async () => {
    renderPage();
    // 2026-06-01T00:00:00Z is 02:00 in Europe/Berlin, the schedule's zone.
    expect(await screen.findByText(/06\/01\/2026,?\s+02:00\s*AM/i)).toBeInTheDocument();
    expect(screen.getByText(/times in Europe\/Berlin/)).toBeInTheDocument();
  });

  it('saves the rotation with the edited handoff interval', async () => {
    const user = userEvent.setup();
    renderPage();
    const interval = await screen.findByLabelText('Handoff every');
    const save = screen.getByRole('button', { name: 'Save rotation' });
    // The rotation form is seeded from the schedule query by an effect, so the
    // participant list (and with it the Save button) is populated a tick after
    // the inputs first render. Wait for that before editing: otherwise a slow
    // runner can type into a form whose participants are still empty, leaving
    // Save disabled and the click a no-op.
    await waitFor(() => expect(save).toBeEnabled());
    await user.clear(interval);
    await user.type(interval, '2');
    await user.click(save);

    await waitFor(() => expect(put).toHaveBeenCalled());
    const [path, body] = put.mock.calls[0] as [string, { rotation: Record<string, unknown> }];
    expect(path).toBe('/api/v1/schedules/sch_1');
    expect(body.rotation.handoff_interval).toBe(2);
    expect(body.rotation.participant_ids).toEqual(['u_a', 'u_b']);
  });

  it('turns shift-change notifications on through the API', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByLabelText('Notify on handoff'));

    await waitFor(() => expect(put).toHaveBeenCalled());
    const [path, body] = put.mock.calls[0] as [string, { notify_on_shift_change: boolean }];
    expect(path).toBe('/api/v1/schedules/sch_1');
    expect(body.notify_on_shift_change).toBe(true);
  });

  it('deletes an override through the override endpoint', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByLabelText('Delete override'));

    await waitFor(() => expect(del).toHaveBeenCalled());
    expect(del.mock.calls[0][0]).toBe('/api/v1/schedules/sch_1/overrides/ovr_1');
  });
});
