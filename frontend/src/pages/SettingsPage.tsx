import { useCallback, useEffect, useState } from 'react';
import type { ReactNode } from 'react';
import {
  ActionIcon,
  Alert,
  Badge,
  Button,
  Group,
  Modal,
  Paper,
  PasswordInput,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  Table,
  Tabs,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import { IconDeviceFloppy, IconInfoCircle, IconPlus } from '@tabler/icons-react';
import {
  useAllOf,
  useChatopsChannels,
  useChatopsMessages,
  useCreateChatopsChannel,
  useDeleteChatopsChannel,
  useHealth,
  useUpdateChatopsChannel,
} from '../api/hooks';
import qrcode from 'qrcode-generator';
import {
  authApi,
  getApiKey,
  type MobilePairing,
  type MobileSessionInfo,
  type SessionInfo,
} from '../api/client';
import type { ChatopsChannel, ChatopsMessage } from '../api/types';
import { useAuth } from '../auth/AuthProvider';
import {
  AbsoluteTime,
  ConfirmDeleteButton,
  PageHeader,
  ProvisionedBadge,
  QueryState,
  StatusBadge,
} from '../components/common';
import { useI18n } from '../i18n/I18nProvider';
import { useRoleLabel } from '../i18n/domain';
import { describeError } from '../i18n/errors';
import { EMPTY_VALUE } from '../i18n/format';

// A ChatOps message response is an object, not a string: outbound rows carry
// {text, alert_group_id}, inbound ones the command result ({text, alert_group},
// {text, users}, …). Every shape has `text`; the JSON fallback keeps an
// unexpected one visible instead of rendering nothing.
function chatopsResponseText(response: ChatopsMessage['response']): string {
  if (!response) return '';
  const text = (response as { text?: unknown }).text;
  if (typeof text === 'string') return text;
  return JSON.stringify(response);
}

export function SettingsPage() {
  const { t } = useI18n();
  return (
    <>
      <PageHeader title={t('settings.title')} description={t('settings.description')} />
      <Tabs defaultValue="instance">
        <Tabs.List mb="md">
          <Tabs.Tab value="instance">{t('settings.instance')}</Tabs.Tab>
          <Tabs.Tab value="chatops">ChatOps</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="instance">
          <InstanceTab />
        </Tabs.Panel>
        <Tabs.Panel value="chatops">
          <ChatopsTab />
        </Tabs.Panel>
      </Tabs>
    </>
  );
}

function InstanceTab() {
  const health = useHealth();
  const { authDisabled, signOut, identity } = useAuth();
  const key = getApiKey();
  const { t, fmt } = useI18n();
  const roleLabel = useRoleLabel();

  return (
    <Stack gap="md">
      <Paper withBorder p="lg">
        <Title order={5} mb="md">
          {t('settings.backend')}
        </Title>
        {health.isError ? (
          <Alert color="red">{t('settings.healthFailed')}</Alert>
        ) : (
          <SimpleGrid cols={{ base: 2, sm: 4 }}>
            <Field label={t('common.status')} value={<StatusBadge status={health.data?.status} />} />
            <Field label={t('settings.version')} value={<Text size="sm">{health.data?.version ?? EMPTY_VALUE}</Text>} />
            <Field
              label={t('settings.database')}
              value={<StatusBadge status={health.data?.db_ok ? 'ok' : 'degraded'} />}
            />
            <Field
              label={t('settings.uptime')}
              value={
                <Text size="sm">
                  {health.data ? fmt.duration(health.data.uptime_seconds * 1000) : EMPTY_VALUE}
                </Text>
              }
            />
            <Field
              label={t('settings.workerCycles')}
              value={<Text size="sm">{health.data?.worker_cycles_completed ?? EMPTY_VALUE}</Text>}
            />
            <Field
              label={t('settings.lastCycle')}
              value={<AbsoluteTime value={health.data?.last_worker_cycle_at ?? null} />}
            />
          </SimpleGrid>
        )}
      </Paper>

      <Paper withBorder p="lg">
        <Title order={5} mb="md">
          {t('settings.access')}
        </Title>
        {authDisabled ? (
          <Alert icon={<IconInfoCircle size={16} />} variant="light">
            {t('settings.anonymousWarning')}
          </Alert>
        ) : (
          <Stack align="flex-start">
            <Text size="sm">
              {t('settings.signedInAs', {
                name: identity?.display_name ?? t('common.unknown'),
                role: roleLabel(identity?.role ?? ''),
              })}{' — '}
              {identity?.kind === 'user' ? (
                <>{t('settings.personalSession')}</>
              ) : (
                <>{t('settings.apiKeySession', { suffix: (key ?? '').slice(-4) })}</>
              )}
            </Text>
            <Button variant="light" color="red" onClick={() => void signOut()}>
              {t('shell.signOut')}
            </Button>
          </Stack>
        )}
      </Paper>

      {identity?.kind === 'user' && <SessionsCard />}
      {identity?.kind === 'user' && <PairMobileCard />}
      {identity?.kind === 'user' && <ChangePasswordCard />}
    </Stack>
  );
}

/**
 * Where this account is signed in, and a way to end any of it. Without this,
 * the only response to a forgotten sign-in on a shared machine is a password
 * change, which is a much bigger hammer.
 */
function SessionsCard() {
  const { t } = useI18n();
  const [sessions, setSessions] = useState<SessionInfo[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const { items } = await authApi.sessions();
      setSessions(items);
      setError(null);
    } catch (err) {
      setError(describeError(err, t));
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  const revoke = async (id: string) => {
    setBusy(id);
    try {
      await authApi.revokeSession(id);
      await load();
    } catch (err) {
      setError(describeError(err, t));
    } finally {
      setBusy(null);
    }
  };

  return (
    <Paper withBorder p="lg">
      <Title order={5} mb="md">
        {t('settings.activeSessions')}
      </Title>
      {error && <Alert color="red">{error}</Alert>}
      {sessions && sessions.length > 0 && (
        <Table.ScrollContainer minWidth={600}>
          <Table verticalSpacing="sm">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t('settings.signedIn')}</Table.Th>
                <Table.Th>{t('settings.expires')}</Table.Th>
                <Table.Th w={140}>{t('settings.address')}</Table.Th>
                <Table.Th>{t('settings.browser')}</Table.Th>
                <Table.Th w={110} />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {sessions.map((session) => (
                <Table.Tr key={session.id}>
                  <Table.Td>
                    <Group gap="xs">
                      <AbsoluteTime value={session.created_at} />
                      {session.current && (
                        <Badge size="sm" variant="light">
                          {t('settings.thisOne')}
                        </Badge>
                      )}
                    </Group>
                  </Table.Td>
                  <Table.Td>
                    <AbsoluteTime value={session.expires_at} />
                  </Table.Td>
                  <Table.Td>
                    <Text size="xs" ff="monospace">
                      {session.request_ip || EMPTY_VALUE}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Text size="xs" c="dimmed" lineClamp={1}>
                      {session.user_agent || EMPTY_VALUE}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Button
                      size="compact-sm"
                      variant="light"
                      color="red"
                      loading={busy === session.id}
                      onClick={() => void revoke(session.id)}
                    >
                      {session.current ? t('shell.signOut') : t('settings.revoke')}
                    </Button>
                  </Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      )}
    </Paper>
  );
}

/**
 * The link the mobile app understands: scanned with the app, or with the phone's
 * camera, which hands the custom scheme to the app. The server URL is the
 * configured public one, or else the address this page was opened on — which
 * is the address the phone needs to reach.
 */
export function mobilePairingUri(pairing: MobilePairing, origin: string): string {
  const server = pairing.server_url || origin;
  return `nxs-anomaly://pair?server=${encodeURIComponent(server)}&code=${encodeURIComponent(pairing.code)}`;
}

function qrDataUrl(text: string): string {
  const qr = qrcode(0, 'M');
  qr.addData(text);
  qr.make();
  return qr.createDataURL(6, 2);
}

/**
 * Signs the mobile app in as this person. The code works once and for five
 * minutes; the phone gets this person's role, capped at responder.
 */
function PairMobileCard() {
  const { t } = useI18n();
  const [pairing, setPairing] = useState<MobilePairing | null>(null);
  const [phones, setPhones] = useState<MobileSessionInfo[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [revoking, setRevoking] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const { items } = await authApi.mobileSessions();
      setPhones(items);
    } catch (err) {
      setError(describeError(err, t));
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  const revoke = async (id: string) => {
    setRevoking(id);
    try {
      await authApi.revokeMobileSession(id);
      await load();
    } catch (err) {
      setError(describeError(err, t));
    } finally {
      setRevoking(null);
    }
  };

  // The phone signs in while the dialog is open; closing it is when the new
  // one should appear in the list.
  const close = () => {
    setPairing(null);
    void load();
  };

  const start = async () => {
    setBusy(true);
    try {
      setPairing(await authApi.pairMobile());
      setError(null);
    } catch (err) {
      setError(describeError(err, t));
    } finally {
      setBusy(false);
    }
  };

  const uri = pairing ? mobilePairingUri(pairing, window.location.origin) : '';

  return (
    <Paper withBorder p="lg">
      <Title order={5} mb="xs">
        {t('settings.mobileApp')}
      </Title>
      <Text size="sm" c="dimmed" mb="md">
        {t('settings.mobileAppHelp')}
      </Text>
      {error && <Alert color="red">{error}</Alert>}
      {phones.length > 0 && (
        <Table verticalSpacing="sm" mb="md">
          <Table.Thead>
            <Table.Tr>
              <Table.Th>{t('settings.mobileDevice')}</Table.Th>
              <Table.Th>{t('settings.signedIn')}</Table.Th>
              <Table.Th>{t('settings.expires')}</Table.Th>
              <Table.Th w={110} />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {phones.map((phone) => (
              <Table.Tr key={phone.id}>
                <Table.Td>
                  {phone.device_name || EMPTY_VALUE}{' '}
                  <Text span size="xs" c="dimmed">
                    {phone.platform}
                  </Text>
                </Table.Td>
                <Table.Td>
                  <AbsoluteTime value={phone.created_at} />
                </Table.Td>
                <Table.Td>
                  <AbsoluteTime value={phone.expires_at} />
                </Table.Td>
                <Table.Td>
                  <Button
                    size="compact-sm"
                    variant="light"
                    color="red"
                    loading={revoking === phone.id}
                    onClick={() => void revoke(phone.id)}
                  >
                    {t('settings.revoke')}
                  </Button>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      <Button variant="light" loading={busy} onClick={() => void start()}>
        {t('settings.mobilePair')}
      </Button>
      <Modal opened={pairing !== null} onClose={close} title={t('settings.mobileApp')} centered>
        {pairing && (
          <Stack align="center" gap="sm">
            <img src={qrDataUrl(uri)} alt={t('settings.mobileQrAlt')} width={240} height={240} />
            <Text size="sm">{t('settings.mobileManual')}</Text>
            <Text ff="monospace" fw={700} size="xl" data-testid="pairing-code">
              {pairing.code}
            </Text>
            <Text size="xs" ff="monospace" c="dimmed">
              {pairing.server_url || window.location.origin}
            </Text>
            <Text size="xs" c="dimmed">
              {t('settings.mobileExpires')} <AbsoluteTime value={pairing.expires_at} />
            </Text>
          </Stack>
        )}
      </Modal>
    </Paper>
  );
}

/**
 * Changing a password signs every session out, including this one, so the card
 * says so before the fact rather than surprising the person with a sign-in
 * screen.
 */
function ChangePasswordCard() {
  const { signOut } = useAuth();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { t } = useI18n();

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await authApi.changePassword(current, next);
      await signOut();
    } catch (err) {
      setError(describeError(err, t));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Paper withBorder p="lg">
      <Title order={5} mb="md">
        {t('settings.password')}
      </Title>
      <form onSubmit={submit}>
        <Stack align="flex-start" gap="sm" maw={360}>
          <PasswordInput
            label={t('settings.currentPassword')}
            value={current}
            onChange={(event) => setCurrent(event.currentTarget.value)}
            autoComplete="current-password"
            w="100%"
          />
          <PasswordInput
            label={t('users.newPassword')}
            description={t('users.passwordLength')}
            value={next}
            onChange={(event) => setNext(event.currentTarget.value)}
            autoComplete="new-password"
            w="100%"
          />
          {error && <Alert color="red">{error}</Alert>}
          <Text size="xs" c="dimmed">
            {t('settings.passwordSignout')}
          </Text>
          <Button type="submit" loading={busy} variant="light">
            {t('settings.changePassword')}
          </Button>
        </Stack>
      </form>
    </Paper>
  );
}

function Field({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div>
      <Text size="xs" c="dimmed" tt="uppercase" fw={600} mb={4}>
        {label}
      </Text>
      {value}
    </div>
  );
}

/**
 * The webhook URL is what makes a ChatOps channel a real transport. Edited
 * locally and saved explicitly, so typing a URL does not fire a request per
 * keystroke.
 */
function WebhookCell({
  channel,
  onSave,
  saving,
}: {
  channel: ChatopsChannel;
  onSave: (webhookUrl: string) => void;
  saving: boolean;
}) {
  const { t } = useI18n();
  const [value, setValue] = useState(channel.webhook_url ?? '');
  const dirty = value !== (channel.webhook_url ?? '');
  return (
    <Group gap={4} wrap="nowrap">
      <TextInput
        size="xs"
        placeholder="https://hooks.slack.com/…"
        value={value}
        onChange={(event) => setValue(event.currentTarget.value)}
        aria-label={t('settings.incomingWebhookFor', { name: channel.name })}
        style={{ flex: 1 }}
      />
      <ActionIcon
        variant="subtle"
        aria-label={t('settings.saveWebhookFor', { name: channel.name })}
        disabled={!dirty}
        loading={saving && dirty}
        onClick={() => onSave(value)}
      >
        <IconDeviceFloppy size={16} />
      </ActionIcon>
    </Group>
  );
}

function ChatopsTab() {
  const { t } = useI18n();
  const channels = useChatopsChannels();
  const messages = useChatopsMessages();
  const teams = useAllOf('teams');
  const users = useAllOf('users');
  const update = useUpdateChatopsChannel();
  const remove = useDeleteChatopsChannel();
  const [creating, setCreating] = useState(false);

  const teamName = new Map((teams.data ?? []).map((team) => [team.id, team.name]));
  const userName = new Map((users.data ?? []).map((user) => [user.id, user.name]));

  return (
    <Stack gap="md">
      <Group justify="space-between">
        <Text size="sm" c="dimmed">
          {t('settings.chatopsHelp')}
        </Text>
        <Button leftSection={<IconPlus size={16} />} onClick={() => setCreating(true)}>
          {t('settings.addChannel')}
        </Button>
      </Group>

      <Paper withBorder>
        <QueryState
          query={channels}
          isEmpty={(data) => (data.items?.length ?? 0) === 0}
          emptyLabel={t('settings.noChannels')}
        >
          {(data) => (
            <Table highlightOnHover verticalSpacing="sm">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('common.name')}</Table.Th>
                  <Table.Th w={130}>{t('settings.platform')}</Table.Th>
                  <Table.Th w={230}>{t('settings.incomingWebhook')}</Table.Th>
                  <Table.Th w={200}>{t('settings.boundTo')}</Table.Th>
                  <Table.Th w={140}>{t('settings.commands')}</Table.Th>
                  <Table.Th w={150}>{t('nav.notifications')}</Table.Th>
                  <Table.Th w={60} />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.items.map((channel) => (
                  <Table.Tr key={channel.id}>
                    <Table.Td>
                      <Group gap={6}>
                        <Text size="sm" fw={500}>
                          {channel.name}
                        </Text>
                        <ProvisionedBadge by={channel.provisioned_by} />
                      </Group>
                    </Table.Td>
                    <Table.Td>
                      <Badge variant="light" tt="none">
                        {channel.platform}
                      </Badge>
                    </Table.Td>
                    <Table.Td>
                      <WebhookCell
                        channel={channel}
                        onSave={(webhook_url) =>
                          update.mutate({ id: channel.id, body: { webhook_url } })
                        }
                        saving={update.isPending}
                      />
                    </Table.Td>
                    <Table.Td>
                      <Text size="sm">
                        {channel.team_id
                          ? `team: ${teamName.get(channel.team_id) ?? channel.team_id}`
                          : channel.user_id
                            ? `user: ${userName.get(channel.user_id) ?? channel.user_id}`
                            : EMPTY_VALUE}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <Switch
                        checked={Boolean(channel.commands_enabled)}
                        disabled={Boolean(channel.provisioned_by)}
                        onChange={(event) =>
                          update.mutate({
                            id: channel.id,
                            body: { commands_enabled: event.currentTarget.checked },
                          })
                        }
                        aria-label={t('settings.toggleCommands')}
                      />
                    </Table.Td>
                    <Table.Td>
                      <Switch
                        checked={Boolean(channel.notifications_enabled)}
                        disabled={Boolean(channel.provisioned_by)}
                        onChange={(event) =>
                          update.mutate({
                            id: channel.id,
                            body: { notifications_enabled: event.currentTarget.checked },
                          })
                        }
                        aria-label={t('settings.toggleNotifications')}
                      />
                    </Table.Td>
                    <Table.Td>
                      <ConfirmDeleteButton
                        label={channel.name}
                        loading={remove.isPending}
                        disabled={Boolean(channel.provisioned_by)}
                        disabledReason={t('common.provisionedHint', { tool: channel.provisioned_by ?? '' })}
                        onConfirm={() => remove.mutate(channel.id)}
                      />
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          )}
        </QueryState>
      </Paper>

      <Paper withBorder p="lg">
        <Title order={5} mb="md">
          {t('settings.recentMessages')}
        </Title>
        <QueryState
          query={messages}
          isEmpty={(data) => (data.items?.length ?? 0) === 0}
          emptyLabel={t('settings.noMessages')}
        >
          {(data) => (
            <Table>
              <Table.Thead>
                <Table.Tr>
                  <Table.Th w={110}>{t('settings.direction')}</Table.Th>
                  <Table.Th w={150}>{t('settings.actor')}</Table.Th>
                  <Table.Th>{t('settings.commandResponse')}</Table.Th>
                  <Table.Th w={180}>{t('settings.when')}</Table.Th>
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.items.map((message) => (
                  <Table.Tr key={message.id}>
                    <Table.Td>
                      <Badge variant="default" tt="none">
                        {message.direction}
                      </Badge>
                    </Table.Td>
                    <Table.Td>
                      <Text size="sm">{message.actor || EMPTY_VALUE}</Text>
                    </Table.Td>
                    <Table.Td>
                      <Stack gap={2}>
                        {message.command && (
                          <Text size="sm" ff="monospace">
                            {message.command}
                          </Text>
                        )}
                        {chatopsResponseText(message.response) && (
                          <Text size="xs" c="dimmed" lineClamp={2}>
                            {chatopsResponseText(message.response)}
                          </Text>
                        )}
                      </Stack>
                    </Table.Td>
                    <Table.Td>
                      <AbsoluteTime value={message.created_at} />
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          )}
        </QueryState>
      </Paper>

      <ChannelModal opened={creating} onClose={() => setCreating(false)} />
    </Stack>
  );
}

function ChannelModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const { t } = useI18n();
  const create = useCreateChatopsChannel();
  const teams = useAllOf('teams');
  const users = useAllOf('users');
  const [name, setName] = useState('');
  const [platform, setPlatform] = useState('telegram');
  const [teamId, setTeamId] = useState<string | null>(null);
  const [userId, setUserId] = useState<string | null>(null);

  return (
    <Modal opened={opened} onClose={onClose} title={t('settings.addChatops')} centered>
      <Stack>
        <TextInput
          label={t('common.name')}
          required
          value={name}
          onChange={(event) => setName(event.currentTarget.value)}
        />
        <Select
          label={t('settings.platform')}
          data={['telegram', 'slack', 'mattermost']}
          value={platform}
          onChange={(value) => setPlatform(value ?? 'telegram')}
          allowDeselect={false}
        />
        <Select
          label={t('common.team')}
          placeholder={t('common.none')}
          clearable
          searchable
          data={(teams.data ?? []).map((team) => ({ value: team.id, label: team.name }))}
          value={teamId}
          onChange={setTeamId}
        />
        <Select
          label={t('common.user')}
          placeholder={t('common.none')}
          clearable
          searchable
          data={(users.data ?? []).map((user) => ({ value: user.id, label: user.name }))}
          value={userId}
          onChange={setUserId}
        />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            loading={create.isPending}
            disabled={!name.trim()}
            onClick={() =>
              create.mutate(
                {
                  name,
                  platform,
                  team_id: teamId ?? '',
                  user_id: userId ?? '',
                  commands_enabled: true,
                  notifications_enabled: true,
                },
                {
                  onSuccess: () => {
                    setName('');
                    onClose();
                  },
                },
              )
            }
          >
            {t('common.create')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
