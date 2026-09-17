import { useState } from 'react';
import {
  ActionIcon,
  Alert,
  Badge,
  Button,
  Group,
  Menu,
  Modal,
  NumberInput,
  Paper,
  PasswordInput,
  Select,
  Stack,
  Switch,
  Table,
  Tabs,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import {
  IconBell,
  IconKey,
  IconPencil,
  IconPlus,
  IconSend,
  IconTrash,
  IconUserPlus,
} from '@tabler/icons-react';
import {
  useCreate,
  useDelete,
  useList,
  useToggleDuty,
  useUpdateUser,
} from '../api/hooks';
import { api } from '../api/client';
import { useAuth } from '../auth/AuthProvider';
import {
  NOTIFICATION_TARGET_TYPES,
  POLICY_CHANNELS,
  POLICY_NAMES,
  PRIORITIES,
  ROLE_OPTIONS,
  type NotificationPolicies,
  type NotificationTarget,
  type NotificationTargetType,
  type PolicyChannel,
  type PolicyName,
  type PolicyStep,
  type Priority,
  type Role,
  type User,
} from '../api/types';
import { ConfirmDeleteButton, PageHeader, ProvisionedBadge, QueryState, RowActions} from '../components/common';
import {
  EMPTY_STEP,
  cleanPolicies,
  policyStepIssue,
  type Profile,
} from './notification-policy-utils';
import { useI18n } from '../i18n/I18nProvider';
import { useChannelLabel, usePriorityLabel, useRoleLabel, useStatusLabel } from '../i18n/domain';
import { describeError } from '../i18n/errors';
import { EMPTY_VALUE } from '../i18n/format';

export function UsersPage() {
  const { t } = useI18n();
  const priorityLabel = usePriorityLabel();
  const roleLabel = useRoleLabel();
  const channelLabel = useChannelLabel();
  const users = useList('users', { limit: 500 });
  const remove = useDelete('users');
  const toggleDuty = useToggleDuty();
  const [editing, setEditing] = useState<User | null>(null);
  const [creating, setCreating] = useState(false);
  const [passwordFor, setPasswordFor] = useState<User | null>(null);
  const { identity } = useAuth();

  return (
    <>
      <PageHeader
        title={t('users.title')}
        description={t('users.description')}
        actions={
          <Button leftSection={<IconUserPlus size={16} />} onClick={() => setCreating(true)}>
            {t('users.add')}
          </Button>
        }
      />

      <Paper withBorder>
        <QueryState
          query={users}
          isEmpty={(data) => data.items.length === 0}
          emptyLabel={t('users.empty')}
        >
          {(data) => (
            <Table.ScrollContainer minWidth={900}>
              <Table highlightOnHover verticalSpacing="sm">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t('common.name')}</Table.Th>
                    <Table.Th w={160}>{t('users.contacts')}</Table.Th>
                    <Table.Th w={110}>{t('users.priority')}</Table.Th>
                    <Table.Th>{t('users.targets')}</Table.Th>
                    <Table.Th w={120}>{t('common.timezone')}</Table.Th>
                    <Table.Th w={110}>{t('users.access')}</Table.Th>
                    <Table.Th w={100}>{t('users.onDuty')}</Table.Th>
                    <Table.Th w={120} />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {data.items.map((user) => (
                    <Table.Tr key={user.id}>
                      <Table.Td>
                        <Stack gap={0}>
                          <Group gap={6}>
                            <Text size="sm" fw={500}>
                              {user.name}
                            </Text>
                            <ProvisionedBadge by={user.provisioned_by} />
                          </Group>
                          <Text size="xs" c="dimmed">
                            @{user.username}
                          </Text>
                        </Stack>
                      </Table.Td>
                      <Table.Td>
                        <Stack gap={0}>
                          {user.email && (
                            <Text size="xs">{user.email}</Text>
                          )}
                          {user.phone && (
                            <Text size="xs" c="dimmed">
                              {user.phone}
                            </Text>
                          )}
                          {!user.email && !user.phone && <Text c="dimmed">{EMPTY_VALUE}</Text>}
                        </Stack>
                      </Table.Td>
                      <Table.Td>
                        <Badge
                          variant="light"
                          tt="none"
                          color={
                            user.priority === 'high'
                              ? 'red'
                              : user.priority === 'medium'
                                ? 'yellow'
                                : 'gray'
                          }
                        >
                          {priorityLabel(user.priority)}
                        </Badge>
                      </Table.Td>
                      <Table.Td>
                        <Group gap={4}>
                          {(user.notification_targets ?? []).map((target, index) => (
                            <Badge key={index} variant="default" tt="none" style={{ fontWeight: 400 }}>
                              {channelLabel(target.type)}
                              {target.target ? `: ${target.target}` : ''}
                            </Badge>
                          ))}
                        </Group>
                      </Table.Td>
                      <Table.Td>
                        <Text size="sm">{user.timezone}</Text>
                      </Table.Td>
                      <Table.Td>
                        {user.role ? (
                          <Badge variant="light" tt="none" color={ROLE_COLORS[user.role]}>
                            {roleLabel(user.role)}
                          </Badge>
                        ) : (
                          <Text size="xs" c="dimmed">
                            {t('users.rosterOnly')}
                          </Text>
                        )}
                      </Table.Td>
                      <Table.Td>
                        <Switch
                          checked={Boolean(user.on_duty)}
                          onChange={(event) =>
                            toggleDuty.mutate({ id: user.id, onDuty: event.currentTarget.checked })
                          }
                          aria-label={t('users.toggleDuty', { name: user.name })}
                        />
                      </Table.Td>
                      <Table.Td>
                        {/* Editing is the everyday action and stays an icon;
                            setting a password and deleting live behind the menu,
                            where a slip of the pointer cannot reach them. */}
                        <RowActions
                          label={t('common.actionsFor', { name: user.name })}
                          menu={
                            <>
                              {identity?.permissions.admin && (
                                <Menu.Item
                                  leftSection={<IconKey size={14} />}
                                  onClick={() => setPasswordFor(user)}
                                >
                                  {t('users.setPassword')}
                                </Menu.Item>
                              )}
                              <ConfirmDeleteButton
                                asMenuItem
                                label={user.name}
                                loading={remove.isPending}
                                disabled={Boolean(user.provisioned_by)}
                                disabledReason={t('common.provisionedHint', {
                                  tool: user.provisioned_by ?? '',
                                })}
                                onConfirm={() => remove.mutate(user.id)}
                              />
                            </>
                          }
                        >
                          <ActionIcon
                            variant="subtle"
                            onClick={() => setEditing(user)}
                            disabled={Boolean(user.provisioned_by)}
                            aria-label={t('users.edit', { name: user.name })}
                          >
                            <IconPencil size={16} />
                          </ActionIcon>
                        </RowActions>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          )}
        </QueryState>
      </Paper>

      <UserModal opened={creating} onClose={() => setCreating(false)} />
      <UserModal opened={editing !== null} user={editing} onClose={() => setEditing(null)} />
      <PasswordModal
        opened={passwordFor !== null}
        user={passwordFor}
        onClose={() => setPasswordFor(null)}
      />
    </>
  );
}

const ROLE_COLORS: Record<Exclude<Role, ''>, string> = {
  viewer: 'gray',
  responder: 'blue',
  editor: 'violet',
  admin: 'red',
};

/**
 * Administrative password management. Setting or removing a password signs the
 * target out of every session, which the dialog states rather than leaving to
 * be discovered.
 */
function PasswordModal({
  opened,
  user,
  onClose,
}: {
  opened: boolean;
  user: User | null;
  onClose: () => void;
}) {
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { t } = useI18n();

  const run = async (action: 'set' | 'remove') => {
    if (!user) return;
    setBusy(true);
    setError(null);
    try {
      if (action === 'set') await api.put(`/api/v1/users/${user.id}/password`, { password });
      else await api.delete(`/api/v1/users/${user.id}/password`);
      setPassword('');
      onClose();
    } catch (err) {
      setError(describeError(err, t));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal opened={opened} onClose={onClose} title={t('users.passwordFor', { name: user?.name ?? '' })} centered>
      <Stack>
        {user && !user.role && (
          <Text size="sm" c="dimmed">
            {t('users.noRolePassword')}
          </Text>
        )}
        <PasswordInput
          label={t('users.newPassword')}
          description={t('users.passwordLength')}
          value={password}
          onChange={(event) => setPassword(event.currentTarget.value)}
          autoComplete="new-password"
        />
        {error && <Alert color="red">{error}</Alert>}
        <Text size="xs" c="dimmed">
          {t('users.passwordSignsOut')}
        </Text>
        <Group justify="space-between">
          <Button
            variant="light"
            color="red"
            loading={busy}
            onClick={() => void run('remove')}
          >
            {t('users.removePassword')}
          </Button>
          <Group>
            <Button variant="default" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button loading={busy} disabled={!password} onClick={() => void run('set')}>
              {t('users.setPassword')}
            </Button>
          </Group>
        </Group>
      </Stack>
    </Modal>
  );
}

const EMPTY_TARGET: NotificationTarget = { type: 'log', target: '' };

function UserModal({
  opened,
  user,
  onClose,
}: {
  opened: boolean;
  user?: User | null;
  onClose: () => void;
}) {
  const create = useCreate('users');
  const update = useUpdateUser();
  const isEdit = Boolean(user);
  const { t } = useI18n();
  const priorityLabel = usePriorityLabel();
  const roleLabel = useRoleLabel();
  const channelLabel = useChannelLabel();

  const [form, setForm] = useState(() => initialForm(user));
  const [targets, setTargets] = useState<NotificationTarget[]>(
    () => user?.notification_targets ?? [EMPTY_TARGET],
  );
  const [policies, setPolicies] = useState<NotificationPolicies>(() => user?.notification_policies ?? {});
  const [key, setKey] = useState(user?.id ?? 'new');

  // Re-seed the form when a different user is opened in the same modal instance.
  if (opened && (user?.id ?? 'new') !== key) {
    setKey(user?.id ?? 'new');
    setForm(initialForm(user));
    setTargets(user?.notification_targets ?? [EMPTY_TARGET]);
    setPolicies(user?.notification_policies ?? {});
  }

  const cleanedPolicies = cleanPolicies(policies);
  const hasPolicies = Object.keys(cleanedPolicies).length > 0;

  const submit = () => {
    const body = {
      ...form,
      notification_targets: targets.filter((target) => target.type),
      // Send null when there are no steps so the user stays on the legacy path;
      // the backend sanitizer treats an empty policy the same way.
      notification_policies: hasPolicies ? cleanedPolicies : null,
    };
    const options = { onSuccess: onClose };
    if (user) update.mutate({ id: user.id, body }, options);
    else create.mutate(body, options);
  };

  return (
    <Modal
      opened={opened}
      onClose={onClose}
      title={isEdit ? t('users.edit', { name: user?.name ?? '' }) : t('users.add')}
      size="lg"
      centered
    >
      <Stack>
        <TextInput
          label={t('common.name')}
          required
          value={form.name}
          onChange={(event) => setForm({ ...form, name: event.currentTarget.value })}
        />
        {!isEdit && (
          <TextInput
            label={t('users.username')}
            description={t('users.usernameDescription')}
            value={form.username}
            onChange={(event) => setForm({ ...form, username: event.currentTarget.value })}
          />
        )}
        <Group grow>
          <TextInput
            label={t('users.email')}
            value={form.email}
            onChange={(event) => setForm({ ...form, email: event.currentTarget.value })}
          />
          <TextInput
            label={t('users.phone')}
            value={form.phone}
            onChange={(event) => setForm({ ...form, phone: event.currentTarget.value })}
          />
        </Group>
        {/* The chat account ids are what attribute an acknowledge from a chat to
            this person rather than to the bot, and what let duty and priority be
            run from that chat at all. */}
        <Group grow>
          <TextInput
            label={t('users.telegramId')}
            description={t('users.chatIdHint')}
            value={form.telegram_id}
            onChange={(event) => setForm({ ...form, telegram_id: event.currentTarget.value })}
          />
          <TextInput
            label={t('users.slackId')}
            value={form.slack_id}
            onChange={(event) => setForm({ ...form, slack_id: event.currentTarget.value })}
          />
          <TextInput
            label={t('users.mattermostId')}
            value={form.mattermost_id}
            onChange={(event) => setForm({ ...form, mattermost_id: event.currentTarget.value })}
          />
        </Group>
        <Group grow>
          <TextInput
            label={t('common.timezone')}
            value={form.timezone}
            onChange={(event) => setForm({ ...form, timezone: event.currentTarget.value })}
          />
          <Select
            label={t('users.priority')}
            data={PRIORITIES.map((value) => ({ value, label: priorityLabel(value) }))}
            value={form.priority}
            onChange={(value) => setForm({ ...form, priority: (value ?? 'medium') as Priority })}
            allowDeselect={false}
          />
        </Group>

        <Select
          label={t('users.role')}
          description={t('users.roleDescription')}
          data={ROLE_OPTIONS.map((option) => ({
            value: option.value,
            label: roleLabel(option.value),
          }))}
          value={form.role}
          onChange={(value) => setForm({ ...form, role: (value ?? '') as Role })}
          allowDeselect={false}
        />

        <Stack gap="xs">
          <Group justify="space-between">
            <Text size="sm" fw={500}>
              {t('users.notificationTargets')}
            </Text>
            <Button
              size="compact-sm"
              variant="light"
              leftSection={<IconPlus size={14} />}
              onClick={() => setTargets([...targets, { ...EMPTY_TARGET }])}
            >
              {t('users.addTarget')}
            </Button>
          </Group>
          {targets.map((target, index) => (
            <Group key={index} gap="xs" wrap="nowrap">
              <Select
                data={NOTIFICATION_TARGET_TYPES.map((value) => ({ value, label: channelLabel(value) }))}
                value={target.type}
                onChange={(value) =>
                  setTargets(
                    targets.map((item, i) =>
                      i === index
                        ? { ...item, type: (value ?? 'log') as NotificationTargetType }
                        : item,
                    ),
                  )
                }
                w={150}
                allowDeselect={false}
              />
              <TextInput
                placeholder={t('users.targetPlaceholder')}
                value={target.target}
                onChange={(event) =>
                  setTargets(
                    targets.map((item, i) =>
                      i === index ? { ...item, target: event.currentTarget.value } : item,
                    ),
                  )
                }
                style={{ flex: 1 }}
              />
              <ActionIcon
                color="red"
                variant="subtle"
                disabled={targets.length === 1}
                onClick={() => setTargets(targets.filter((_, i) => i !== index))}
                aria-label={t('users.removeTarget')}
              >
                <IconTrash size={16} />
              </ActionIcon>
            </Group>
          ))}
        </Stack>

        <PolicyEditor
          policies={policies}
          onChange={setPolicies}
          profile={{ email: form.email, telegram_id: form.telegram_id, phone: form.phone }}
          userId={isEdit ? user?.id : undefined}
        />

        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            onClick={submit}
            loading={create.isPending || update.isPending}
            disabled={!form.name.trim()}
          >
            {isEdit ? t('common.save') : t('common.create')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/**
 * Editor for a user's personal notify/wait/fallback policies. Each policy is an
 * ordered list of steps; the wait on a step is how long to hold before falling
 * back to the next one. Steps that cannot be delivered (a webhook with no URL, a
 * telegram step for a user with no Telegram ID) are flagged inline rather than
 * failing silently at page time.
 */
function PolicyEditor({
  policies,
  onChange,
  profile,
  userId,
}: {
  policies: NotificationPolicies;
  onChange: (next: NotificationPolicies) => void;
  profile: Profile;
  userId?: string;
}) {
  const [active, setActive] = useState<PolicyName>('default');
  const { t } = useI18n();
  const channelLabel = useChannelLabel();
  const steps = policies[active] ?? [];

  const setSteps = (next: PolicyStep[]) => onChange({ ...policies, [active]: next });
  const update = (index: number, patch: Partial<PolicyStep>) =>
    setSteps(steps.map((s, i) => (i === index ? { ...s, ...patch } : s)));

  return (
    <Stack gap="xs">
      <Group justify="space-between">
        <Group gap={6}>
          <IconBell size={16} />
          <Text size="sm" fw={500}>
            {t('users.notificationPolicy')}
          </Text>
        </Group>
        <Text size="xs" c="dimmed">
          {t('users.policyOptional')}
        </Text>
      </Group>
      <Tabs value={active} onChange={(v) => setActive((v ?? 'default') as PolicyName)}>
        <Tabs.List>
          {POLICY_NAMES.map((name) => (
            <Tabs.Tab key={name} value={name} style={{ textTransform: 'capitalize' }}>
              {name === 'default' ? t('users.policyDefault') : name === 'important' ? t('users.policyImportant') : name}
              {(policies[name]?.length ?? 0) > 0 ? ` (${policies[name]!.length})` : ''}
            </Tabs.Tab>
          ))}
        </Tabs.List>
      </Tabs>

      {steps.length === 0 && (
        <Text size="xs" c="dimmed">
          {t('users.policyNoSteps')}
        </Text>
      )}

      {steps.map((step, index) => {
        const issue = policyStepIssue(step, profile);
        const isLast = index === steps.length - 1;
        return (
          <Stack key={index} gap={2}>
            <Group gap="xs" wrap="nowrap" align="flex-start">
              <Select
                data={(POLICY_CHANNELS as unknown as string[]).map((value) => ({ value, label: channelLabel(value) }))}
                value={step.channel}
                onChange={(v) => update(index, { channel: (v ?? 'telegram') as PolicyChannel })}
                w={130}
                allowDeselect={false}
                aria-label={t('users.stepChannel', { number: index + 1 })}
              />
              <TextInput
                placeholder={t('users.targetOverride')}
                value={step.target}
                onChange={(e) => update(index, { target: e.currentTarget.value })}
                style={{ flex: 1 }}
                error={issue ?? undefined}
              />
              <Tooltip label={isLast ? t('users.lastStepNoWait') : t('users.waitBeforeNext')}>
                <NumberInput
                  value={step.wait_minutes}
                  onChange={(v) => update(index, { wait_minutes: typeof v === 'number' ? v : 0 })}
                  min={0}
                  w={110}
                  suffix=" min"
                  disabled={isLast}
                  aria-label={t('users.stepWait', { number: index + 1 })}
                />
              </Tooltip>
              {userId && (
                <TestChannelButton userId={userId} channel={step.channel} disabled={Boolean(issue)} />
              )}
              <ActionIcon
                color="red"
                variant="subtle"
                onClick={() => setSteps(steps.filter((_, i) => i !== index))}
                aria-label={t('users.removeStep', { number: index + 1 })}
              >
                <IconTrash size={16} />
              </ActionIcon>
            </Group>
          </Stack>
        );
      })}

      <Group>
        <Button
          size="compact-sm"
          variant="light"
          leftSection={<IconPlus size={14} />}
          onClick={() => setSteps([...steps, { ...EMPTY_STEP }])}
        >
          {t('users.addStep')}
        </Button>
      </Group>
    </Stack>
  );
}

/**
 * Sends one test notification through a channel and reports the provider verdict.
 * Only shown for an existing user, since the delivery hits the real adapter.
 */
function TestChannelButton({
  userId,
  channel,
  disabled,
}: {
  userId: string;
  channel: PolicyChannel;
  disabled: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const { t } = useI18n();
  const channelLabel = useChannelLabel();
  const statusLabel = useStatusLabel();
  const send = async () => {
    setBusy(true);
    try {
      const res = await api.post<{ status: string; provider_status?: string; error?: string; detail?: string }>(
        `/api/v1/users/${userId}/test-notification`,
        { channel },
      );
      const ok = res.status === 'delivered';
      notifications.show({
        color: ok ? 'green' : res.status === 'skipped' ? 'yellow' : 'red',
        title: t('users.testResult', { channel: channelLabel(channel), status: statusLabel(res.status) }),
        message: res.error || res.detail || res.provider_status || t('users.noProviderDetail'),
      });
    } catch (err) {
      notifications.show({
        color: 'red',
        title: t('users.testFailed', { channel: channelLabel(channel) }),
        message: describeError(err, t),
      });
    } finally {
      setBusy(false);
    }
  };
  return (
    <Tooltip label={disabled ? t('users.fixTarget') : t('users.sendTest', { channel: channelLabel(channel) })}>
      <ActionIcon
        variant="subtle"
        color="blue"
        loading={busy}
        disabled={disabled}
        onClick={() => void send()}
        aria-label={t('users.sendTest', { channel: channelLabel(channel) })}
      >
        <IconSend size={16} />
      </ActionIcon>
    </Tooltip>
  );
}

function initialForm(user?: User | null) {
  return {
    name: user?.name ?? '',
    username: user?.username ?? '',
    email: user?.email ?? '',
    phone: user?.phone ?? '',
    telegram_id: user?.telegram_id ?? '',
    slack_id: user?.slack_id ?? '',
    mattermost_id: user?.mattermost_id ?? '',
    timezone: user?.timezone ?? 'UTC',
    priority: user?.priority ?? 'medium',
    role: user?.role ?? ('' as Role),
  };
}
