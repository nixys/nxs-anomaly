import { MantineProvider } from '@mantine/core';
import { render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { SignInScreen } from './SignInScreen';
import { en } from '../i18n/messages/en';

// The sign-in screen is where the two editions first differ in front of a
// person, so it is where the difference has to be legible. A build without
// single sign-on must not simply draw one fewer button: somebody arriving from
// an installation that had SSO would read the gap as a broken deployment and go
// looking for an operator who cannot help them.

const methods = vi.fn();

vi.mock('./AuthProvider', () => ({
  useAuth: () => ({
    signInWithPassword: vi.fn(),
    signInWithApiKey: vi.fn(),
    methods: methods(),
  }),
}));

function renderScreen() {
  return render(
    <MantineProvider>
      <SignInScreen />
    </MantineProvider>,
  );
}

beforeEach(() => vi.clearAllMocks());

describe('single sign-on across editions', () => {
  it('shows SSO disabled, with the reason, in a build that has none', () => {
    methods.mockReturnValue({
      password: true,
      api_key: false,
      anonymous: false,
      oidc: false,
      sso_available: false,
      edition: 'community',
    });

    renderScreen();

    const button = screen.getByRole('button', { name: en['signIn.ssoUnavailable'] });
    expect(button).toBeDisabled();
    // The sentence is the point: a disabled control with no explanation is
    // indistinguishable from a bug.
    expect(screen.getByText(en['signIn.ssoUnavailableHelp'])).toBeInTheDocument();
    // Password sign-in is untouched — the edition removes a door, not the house.
    expect(screen.getByRole('button', { name: en['signIn.submit'] })).toBeEnabled();
  });

  it('offers a working SSO button when the build has it and it is configured', () => {
    methods.mockReturnValue({
      password: true,
      api_key: false,
      anonymous: false,
      oidc: true,
      oidc_label: 'accounts.example.com',
      sso_available: true,
      edition: 'enterprise',
    });

    renderScreen();

    const button = screen.getByRole('button', { name: /accounts\.example\.com/ });
    expect(button).toBeEnabled();
    expect(screen.queryByText(en['signIn.ssoUnavailableHelp'])).not.toBeInTheDocument();
  });

  it('stays silent when the build has SSO but nobody configured it', () => {
    // Deliberate: this is the operator's problem, not the problem of the person
    // trying to sign in, and telling them about an unset environment variable
    // would send them nowhere useful.
    methods.mockReturnValue({
      password: true,
      api_key: false,
      anonymous: false,
      oidc: false,
      sso_available: true,
      edition: 'enterprise',
    });

    renderScreen();

    expect(
      screen.queryByRole('button', { name: en['signIn.ssoUnavailable'] }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(en['signIn.ssoUnavailableHelp'])).not.toBeInTheDocument();
  });
});
