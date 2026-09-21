import { MantineProvider } from '@mantine/core';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { SettingsPage, mobilePairingUri } from './SettingsPage';

// A ChatOps channel has two independent switches — whether it accepts commands
// and whether anything is pushed to it — and a webhook that decides whether the
// second one can work at all. Confusing any of the three is silent: the page
// looks the same, and the consequence only shows up as an alert nobody saw.

const get = vi.fn();
const put = vi.fn();
const pairMobile = vi.fn();
const mobileSessions = vi.fn();
const revokeMobileSession = vi.fn();

vi.mock('../api/client', () => ({
  api: {
    get: (...args: unknown[]) => get(...args),
    post: vi.fn(),
    put: (...args: unknown[]) => put(...args),
    patch: vi.fn(),
    delete: vi.fn(),
  },
  ApiError: class ApiError extends Error {},
  getApiKey: () => null,
  setApiKey: vi.fn(),
  clearApiKey: vi.fn(),
  verifyApiKey: vi.fn(),
  authApi: {
    changePassword: vi.fn(),
    pairMobile: () => pairMobile(),
    mobileSessions: () => mobileSessions(),
    revokeMobileSession: (id: string) => revokeMobileSession(id),
  },
}));

vi.mock('@mantine/notifications', () => ({
  notifications: { show: vi.fn(), hide: vi.fn() },
}));

// The instance tab renders alongside this one and asks who is signed in before
// it decides whether to offer the password form.
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

function channel(id: string, overrides: Record<string, unknown> = {}) {
  return {
    id,
    name: `#${id}`,
    platform: 'telegram',
    chat_id: '-100123',
    webhook_url: '',
    team_id: '',
    user_id: '',
    commands_enabled: true,
    notifications_enabled: true,
    created_at: '2026-06-01T00:00:00Z',
    updated_at: '2026-06-01T00:00:00Z',
    ...overrides,
  };
}

function renderPage(
  channels: ReturnType<typeof channel>[],
  messages: Record<string, unknown>[] = [],
) {
  get.mockImplementation((path: string) => {
    if (path === '/api/v1/chatops/channels') {
      return Promise.resolve({ items: channels, total: channels.length });
    }
    if (path === '/api/v1/chatops/messages') {
      return Promise.resolve({ items: messages, total: messages.length });
    }
    if (path === '/api/v1/teams') {
      return Promise.resolve({ items: [{ id: 'team-1', name: 'Platform' }], total: 1 });
    }
    if (path === '/api/v1/users') {
      return Promise.resolve({ items: [{ id: 'usr-1', name: 'Alice SRE' }], total: 1 });
    }
    return Promise.resolve({ items: [], total: 0 });
  });
  put.mockResolvedValue(channels[0] ?? {});
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MantineProvider>
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <SettingsPage />
        </MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

// openChatops moves off the instance tab, which is what the page opens on.
async function openChatops(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('tab', { name: 'ChatOps' }));
}

beforeEach(() => {
  vi.clearAllMocks();
  mobileSessions.mockResolvedValue({ items: [] });
});

describe('SettingsPage ChatOps tab', () => {
  it('toggles commands without touching notifications', async () => {
    const user = userEvent.setup();
    renderPage([channel('ops', { notifications_enabled: true, commands_enabled: true })]);
    await openChatops(user);

    await user.click(await screen.findByLabelText('Toggle commands'));

    // The two switches mean different things: one silences the channel as a
    // destination, the other stops responders driving the service from chat.
    // Sending the wrong field would turn a channel off in the way nobody asked
    // for, and both switches look identical afterwards.
    await waitFor(() => expect(put).toHaveBeenCalled());
    const [path, body] = put.mock.calls[0];
    expect(path).toBe('/api/v1/chatops/channels/ops');
    expect(body).toEqual({ commands_enabled: false });
  });

  it('toggles notifications without touching commands', async () => {
    const user = userEvent.setup();
    renderPage([channel('ops')]);
    await openChatops(user);

    await user.click(await screen.findByLabelText('Toggle notifications'));

    await waitFor(() => expect(put).toHaveBeenCalled());
    expect(put.mock.calls[0][1]).toEqual({ notifications_enabled: false });
  });

  it('saves a webhook only when asked, not on every keystroke', async () => {
    const user = userEvent.setup();
    renderPage([channel('ops')]);
    await openChatops(user);

    const input = await screen.findByLabelText('Incoming webhook for #ops');
    await user.type(input, 'https://hooks.example/abc');

    // Typing a URL character by character would otherwise write a series of
    // broken prefixes to the channel, each one a valid save as far as the API
    // is concerned.
    expect(put).not.toHaveBeenCalled();

    await user.click(screen.getByLabelText('Save webhook for #ops'));
    await waitFor(() => expect(put).toHaveBeenCalled());
    expect(put.mock.calls[0][1]).toEqual({ webhook_url: 'https://hooks.example/abc' });
  });

  it('offers no save until the webhook is actually changed', async () => {
    const user = userEvent.setup();
    renderPage([channel('ops', { webhook_url: 'https://hooks.example/abc' })]);
    await openChatops(user);

    // A live save button on an untouched row invites a no-op write to every
    // channel on the page.
    expect(await screen.findByLabelText('Save webhook for #ops')).toBeDisabled();
  });

  it('names the team or user a channel is bound to', async () => {
    const user = userEvent.setup();
    renderPage([
      channel('team-chan', { team_id: 'team-1' }),
      channel('dm', { user_id: 'usr-1' }),
      channel('global'),
    ]);
    await openChatops(user);

    await screen.findByText('#team-chan');
    const rows = Array.from(document.querySelectorAll('tbody tr')).map((r) => r.textContent ?? '');

    // Who a channel is bound to decides who its commands run as; a raw id here
    // makes that unreadable at the moment it matters.
    expect(rows[0]).toContain('team: Platform');
    expect(rows[1]).toContain('user: Alice SRE');
    // An unbound channel is not an error — it is the ordinary shared channel.
    expect(rows[2]).toContain('—');
  });

  it('shows the command and the answer it got', async () => {
    const user = userEvent.setup();
    renderPage(
      [channel('ops')],
      [
        {
          id: 'm1',
          direction: 'inbound',
          actor: 'alice',
          command: '/duty take',
          response: { text: 'Alice SRE is on duty' },
          created_at: '2026-06-01T10:00:00Z',
        },
      ],
    );
    await openChatops(user);

    // The log is how a responder proves a command was accepted; a command with
    // no visible answer is indistinguishable from one the bot ignored.
    expect(await screen.findByText('/duty take')).toBeInTheDocument();
    expect(screen.getByText('Alice SRE is on duty')).toBeInTheDocument();
  });

  it('says no channels are configured rather than showing an empty table', async () => {
    const user = userEvent.setup();
    renderPage([]);
    await openChatops(user);

    expect(await screen.findByText(/no chatops channels configured/i)).toBeInTheDocument();
  });

  it('surfaces a failed channel read instead of an empty list', async () => {
    const user = userEvent.setup();
    // Rejected before rendering: the channel list is fetched on mount, well
    // before the tab is opened.
    get.mockRejectedValue(new Error('chatops channels unavailable'));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <MantineProvider>
        <QueryClientProvider client={client}>
          <MemoryRouter>
            <SettingsPage />
          </MemoryRouter>
        </QueryClientProvider>
      </MantineProvider>,
    );
    await openChatops(user);

    // No channels and no answer are the same picture, and only one of them
    // means ChatOps is genuinely unconfigured.
    // Both panels on the tab report it — the channel list and the message log
    // are separate reads of the same unreachable service.
    expect((await screen.findAllByText(/chatops channels unavailable/i)).length).toBeGreaterThan(0);
  });
});

describe('SettingsPage mobile pairing', () => {
  it('shows a one-time code and a QR code for the app', async () => {
    const user = userEvent.setup();
    pairMobile.mockResolvedValue({
      code: 'ABCDE-FGH12',
      expires_at: '2026-09-21T12:05:00+00:00',
      server_url: '',
    });
    renderPage([]);
    await user.click(await screen.findByRole('button', { name: /connect a phone/i }));

    expect(await screen.findByTestId('pairing-code')).toHaveTextContent('ABCDE-FGH12');
    expect(screen.getByAltText(/qr code/i)).toHaveAttribute('src', expect.stringMatching(/^data:image\//));
  });

  it('puts the server the phone must reach into the link', () => {
    const pairing = { code: 'ABCDE-FGH12', expires_at: '', server_url: '' };
    // No public URL configured: the address this page was opened on.
    expect(mobilePairingUri(pairing, 'https://oncall.example')).toBe(
      'nxs-anomaly://pair?server=https%3A%2F%2Foncall.example&code=ABCDE-FGH12',
    );
    // A configured public URL wins over the origin, which may be internal.
    expect(mobilePairingUri({ ...pairing, server_url: 'https://public.example' }, 'http://10.0.0.5')).toContain(
      'server=https%3A%2F%2Fpublic.example',
    );
  });

  it('lists signed-in phones and signs one out', async () => {
    const user = userEvent.setup();
    const phone = {
      id: 'msess-1',
      device_name: 'Pixel 8',
      platform: 'android',
      created_at: '2026-09-21T10:00:00+00:00',
      expires_at: '2026-10-21T10:00:00+00:00',
    };
    mobileSessions.mockResolvedValueOnce({ items: [phone] }).mockResolvedValue({ items: [] });
    revokeMobileSession.mockResolvedValue(undefined);
    renderPage([]);

    expect(await screen.findByText('Pixel 8')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Revoke' }));
    await waitFor(() => expect(revokeMobileSession).toHaveBeenCalledWith('msess-1'));
    await waitFor(() => expect(screen.queryByText('Pixel 8')).not.toBeInTheDocument());
  });
});
