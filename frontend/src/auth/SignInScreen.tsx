import { useState } from 'react';
import {
  Alert,
  Anchor,
  Button,
  Center,
  Group,
  Paper,
  PasswordInput,
  Stack,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { IconAlertTriangle, IconShieldLock, IconKey } from '@tabler/icons-react';
import { useAuth } from './AuthProvider';
import { authApi } from '../api/client';
import { Logo } from '../components/Logo';
import { useI18n } from '../i18n/I18nProvider';
import { describeError } from '../i18n/errors';

export function SignInScreen() {
  const { signInWithPassword, signInWithApiKey, methods } = useAuth();
  const { t } = useI18n();
  // Password is the primary route. The API key form stays reachable because
  // automation credentials are still how scripts and the Grafana plugin talk to
  // this API, and an operator sometimes needs to use one directly.
  const [mode, setMode] = useState<'password' | 'apiKey'>(
    methods && !methods.password && methods.api_key ? 'apiKey' : 'password',
  );
  const [login, setLogin] = useState('');
  const [password, setPassword] = useState('');
  const [key, setKey] = useState('');
  // A failed single sign-on comes back as a redirect, not as a response we can
  // catch, so the reason arrives in the query string.
  const [error, setError] = useState<string | null>(
    () => new URLSearchParams(window.location.search).get('sso_error'),
  );
  const [busy, setBusy] = useState(false);

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      if (mode === 'password') {
        await signInWithPassword(login.trim(), password);
      } else {
        await signInWithApiKey(key.trim());
      }
    } catch (err) {
      setError(describeError(err, t));
    } finally {
      setBusy(false);
    }
  };

  const switchMode = (next: 'password' | 'apiKey') => {
    setMode(next);
    setError(null);
  };

  return (
    <Center mih="100vh" px="md">
      <Paper withBorder p="xl" radius="md" w={420}>
        <form onSubmit={submit}>
          <Stack gap="lg">
            <Group gap="sm">
              <Logo size={32} />
              <div>
                <Title order={3}>nxs-anomaly</Title>
                <Text size="sm" c="dimmed">
                  {t('signIn.productSubtitle')}
                </Text>
              </div>
            </Group>

            {mode === 'password' ? (
              <>
                <Text size="sm" c="dimmed">
                  {t('signIn.accountHelp')}
                </Text>
                <TextInput
                  label={t('signIn.loginLabel')}
                  value={login}
                  onChange={(event) => setLogin(event.currentTarget.value)}
                  autoComplete="username"
                  autoFocus
                  data-autofocus
                />
                <PasswordInput
                  label={t('signIn.password')}
                  value={password}
                  onChange={(event) => setPassword(event.currentTarget.value)}
                  autoComplete="current-password"
                />
              </>
            ) : (
              <>
                <Text size="sm" c="dimmed">
                  {t('signIn.apiKeyHelp')}
                </Text>
                <PasswordInput
                  label={t('signIn.apiKey')}
                  placeholder="NXS_ANOMALY_API_KEYS"
                  value={key}
                  onChange={(event) => setKey(event.currentTarget.value)}
                  autoFocus
                  data-autofocus
                />
              </>
            )}

            {error && (
              <Alert color="red" icon={<IconAlertTriangle size={16} />}>
                {error}
              </Alert>
            )}

            <Button type="submit" loading={busy} leftSection={<IconShieldLock size={16} />}>
              {t('signIn.submit')}
            </Button>

            {methods?.oidc && (
              <Button
                variant="default"
                leftSection={<IconKey size={16} />}
                // A full navigation, not a fetch: the provider needs a top-level
                // redirect, and the session cookie arrives on the way back.
                onClick={() => {
                  window.location.href = authApi.ssoUrl(
                    window.location.pathname + window.location.search,
                  );
                }}
              >
                {t('signIn.ssoProvider', { provider: methods.oidc_label ?? t('signIn.ssoDefault') })}
              </Button>
            )}

            {/*
              A door this build does not have is shown disabled rather than
              omitted. Somebody arriving from an installation that had SSO would
              otherwise read the missing button as a broken deployment and go
              looking for an operator to fix it; the sentence below sends them
              to the right place instead. `sso_available` is what separates
              "this edition has none" from "nobody configured it here" — the
              latter stays hidden, because there the operator, not the person
              signing in, is the one who can act.
            */}
            {methods && !methods.oidc && !methods.sso_available && (
              <Stack gap={4}>
                <Button variant="default" leftSection={<IconKey size={16} />} disabled>
                  {t('signIn.ssoUnavailable')}
                </Button>
                <Text size="xs" c="dimmed" ta="center">
                  {t('signIn.ssoUnavailableHelp')}
                </Text>
              </Stack>
            )}

            <Anchor
              component="button"
              type="button"
              size="sm"
              c="dimmed"
              onClick={() => switchMode(mode === 'password' ? 'apiKey' : 'password')}
            >
              {mode === 'password' ? t('signIn.switchToApiKey') : t('signIn.switchToAccount')}
            </Anchor>
          </Stack>
        </form>
      </Paper>
    </Center>
  );
}
