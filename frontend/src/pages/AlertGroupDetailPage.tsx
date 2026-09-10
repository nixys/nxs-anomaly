import {
  Alert,
  Anchor,
  Button,
  Card,
  Collapse,
  Group,
  Menu,
  Paper,
  SimpleGrid,
  Skeleton,
  Stack,
  Table,
  Tabs,
  Text,
  Timeline,
} from '@mantine/core';
import { useDisclosure, useHotkeys } from '@mantine/hooks';
import {
  IconAlertTriangle,
  IconArrowLeft,
  IconBellOff,
  IconChevronDown,
} from '@tabler/icons-react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useAllOf, useGroupAction, useItem, useList, useSilenceGroup } from '../api/hooks';
import type { ReactNode } from 'react';
import type { AlertGroup, Notification } from '../api/types';
import {
  AbsoluteTime,
  IncidentAge,
  JsonBlock,
  Labels,
  MonoId,
  PageHeader,
  QueryState,
  RelativeTime,
  SeverityBadge,
  StatusBadge,
} from '../components/common';
import { SILENCE_OPTIONS } from './AlertGroupsPage';
import { useI18n } from '../i18n/I18nProvider';
import type { StringKey } from '../i18n/I18nProvider';
import { useChannelLabel } from '../i18n/domain';
import { EMPTY_VALUE } from '../i18n/format';

export function AlertGroupDetailPage() {
  const { t } = useI18n();
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const group = useItem('alert-groups', id);
  const alerts = useList('alerts', { alert_group_id: id, limit: 200 }, { enabled: Boolean(id) });
  const notifications = useList(
    'notifications',
    { alert_group_id: id, limit: 200 },
    { enabled: Boolean(id) },
  );
  const action = useGroupAction();
  const silence = useSilenceGroup();

  // The same letters as the queue. Somebody who learned `a` and `r` on the list
  // should not have to reach for the pointer the moment they open one incident —
  // and this is the screen they open when a page woke them.
  useHotkeys([
    ['a', () => id && action.mutate({ id, action: 'acknowledge' })],
    ['r', () => id && action.mutate({ id, action: 'resolve' })],
    ['s', () => id && silence.mutate({ id, durationMinutes: 60 })],
    ['Escape', () => navigate('/alert-groups')],
  ]);

  return (
    <>
      <PageHeader
        title={t('group.title')}
        description={id}
        actions={
          <>
            <Button
              component={Link}
              to="/alert-groups"
              variant="default"
              leftSection={<IconArrowLeft size={16} />}
            >
              {t('common.back')}
            </Button>
            {id && (
              <>
                <Button
                  variant="light"
                  onClick={() =>
                    action.mutate({
                      id,
                      action: group.data?.status === 'acknowledged' ? 'unacknowledge' : 'acknowledge',
                    })
                  }
                >
                  {group.data?.status === 'acknowledged'
                    ? t('groups.unacknowledge')
                    : t('groups.acknowledge')}
                </Button>
                <Button
                  color="teal"
                  variant="light"
                  onClick={() =>
                    action.mutate({
                      id,
                      action: group.data?.status === 'resolved' ? 'unresolve' : 'resolve',
                    })
                  }
                >
                  {group.data?.status === 'resolved' ? t('groups.unresolve') : t('groups.resolve')}
                </Button>
                <Menu withinPortal position="bottom-end">
                  <Menu.Target>
                    <Button
                      variant="light"
                      color="gray"
                      rightSection={<IconChevronDown size={14} />}
                      leftSection={<IconBellOff size={16} />}
                    >
                      {t('groups.silence')}
                    </Button>
                  </Menu.Target>
                  <Menu.Dropdown>
                    {SILENCE_OPTIONS.map((option) => (
                      <Menu.Item
                        key={option.minutes}
                        onClick={() => silence.mutate({ id, durationMinutes: option.minutes })}
                      >
                        {t(option.labelKey)}
                      </Menu.Item>
                    ))}
                  </Menu.Dropdown>
                </Menu>
              </>
            )}
          </>
        }
      />

      <QueryState query={group}>
        {(data) => (
          <Stack gap="md">
            <DeliveryHealthBanner notifications={notifications.data?.items ?? []} />
            <Summary group={data} />

            <Tabs defaultValue="timeline">
              <Tabs.List mb="md">
                <Tabs.Tab value="timeline">{t('group.timeline')}</Tabs.Tab>
                <Tabs.Tab value="alerts">
                  {t('group.alertsTab', { count: alerts.data?.total ?? 0 })}
                </Tabs.Tab>
                <Tabs.Tab value="notifications">
                  {t('group.notificationsTab', { count: notifications.data?.total ?? 0 })}
                </Tabs.Tab>
                <Tabs.Tab value="raw">{t('common.raw')}</Tabs.Tab>
              </Tabs.List>

              <Tabs.Panel value="timeline">
                <Paper withBorder p="lg">
                  <GroupTimeline group={data} />
                </Paper>
              </Tabs.Panel>

              <Tabs.Panel value="alerts">
                <Paper withBorder>
                  <QueryState
                    query={alerts}
                    isEmpty={(page) => page.items.length === 0}
                    emptyLabel={t('group.noAlerts')}
                  >
                    {(page) => (
                      <Table.ScrollContainer minWidth={800}>
                        <Table highlightOnHover verticalSpacing="sm">
                          <Table.Thead>
                            <Table.Tr>
                              <Table.Th>{t('common.title')}</Table.Th>
                              <Table.Th w={110}>{t('common.status')}</Table.Th>
                              <Table.Th w={110}>{t('common.severity')}</Table.Th>
                              <Table.Th w={160}>{t('common.source')}</Table.Th>
                              <Table.Th w={180}>{t('common.received')}</Table.Th>
                            </Table.Tr>
                          </Table.Thead>
                          <Table.Tbody>
                            {page.items.map((alert) => (
                              <Table.Tr key={alert.id}>
                                <Table.Td>
                                  <Stack gap={4}>
                                    <Text size="sm">{alert.title || alert.id}</Text>
                                    {alert.message && (
                                      <Text size="xs" c="dimmed" lineClamp={2}>
                                        {alert.message}
                                      </Text>
                                    )}
                                    <Labels labels={alert.labels} />
                                  </Stack>
                                </Table.Td>
                                <Table.Td>
                                  <StatusBadge status={alert.status} />
                                </Table.Td>
                                <Table.Td>
                                  <SeverityBadge severity={alert.severity} />
                                </Table.Td>
                                <Table.Td>
                                  {alert.external_url ? (
                                    <Anchor href={alert.external_url} target="_blank" size="sm">
                                      {alert.source || t('alerts.sourceFallback')}
                                    </Anchor>
                                  ) : (
                                    <Text size="sm">{alert.source || EMPTY_VALUE}</Text>
                                  )}
                                </Table.Td>
                                <Table.Td>
                                  <AbsoluteTime value={alert.received_at} />
                                </Table.Td>
                              </Table.Tr>
                            ))}
                          </Table.Tbody>
                        </Table>
                      </Table.ScrollContainer>
                    )}
                  </QueryState>
                </Paper>
              </Tabs.Panel>

              <Tabs.Panel value="notifications">
                <Paper withBorder>
                  <QueryState
                    query={notifications}
                    isEmpty={(page) => page.items.length === 0}
                    emptyLabel={t('group.noNotifications')}
                  >
                    {(page) => (
                      <Stack gap={0}>
                        {page.items.map((notification) => (
                          <NotificationRow key={notification.id} notification={notification} />
                        ))}
                      </Stack>
                    )}
                  </QueryState>
                </Paper>
              </Tabs.Panel>

              <Tabs.Panel value="raw">
                <Paper withBorder p="md">
                  <JsonBlock value={data} />
                </Paper>
              </Tabs.Panel>
            </Tabs>
          </Stack>
        )}
      </QueryState>
    </>
  );
}

function Summary({ group }: { group: AlertGroup }) {
  const { t } = useI18n();
  const [showAll, details] = useDisclosure(false);
  // Both are small reference collections the app already caches, so naming the
  // integration and the chain costs no extra round trip on this page.
  const integrations = useAllOf('integrations');
  const chains = useAllOf('escalation-chains');
  const integration = (integrations.data ?? []).find((item) => item.id === group.integration_id);
  const chain = (chains.data ?? []).find((item) => item.id === group.escalation_chain_id);
  // While the reference list is still loading, show nothing rather than the id:
  // an identifier that turns into a name a moment later reads as a glitch, and
  // it is the id this page exists to stop showing.
  const naming = integrations.isPending || chains.isPending;

  // What a responder needs before deciding anything: how bad, how long, who is
  // being called and when the next call goes out. Everything else is evidence
  // for later and lives behind "all fields".
  const secondary: Array<[string, ReactNode]> = [
    [t('groups.alerts'), <Text size="sm">{group.alert_count ?? group.alert_ids?.length ?? 0}</Text>],
    [t('group.dedupeKey'), <MonoId id={group.dedupe_key} />],
    [t('group.currentStep'), <Text size="sm">{group.current_step ?? 0}</Text>],
    [t('groups.lastAlert'), <AbsoluteTime value={group.last_received_at} />],
    [t('group.acknowledgedAt'), <AbsoluteTime value={group.acknowledged_at} />],
    [t('group.resolvedAt'), <AbsoluteTime value={group.resolved_at} />],
    [t('group.silencedUntil'), <AbsoluteTime value={group.silenced_until ?? null} />],
    [t('group.groupId'), <MonoId id={group.id} />],
  ];

  return (
    <Card withBorder p="lg">
      <Stack gap="md">
        <div>
          <Group gap="sm" align="center" wrap="wrap">
            <SeverityBadge severity={group.severity} />
            <StatusBadge status={group.status} />
            <Text fw={600} size="lg">
              {group.title || group.dedupe_key || group.id}
            </Text>
          </Group>
          <Group mt="xs">
            <Labels labels={group.labels} />
          </Group>
        </div>

        <SimpleGrid cols={{ base: 1, sm: 2, lg: 4 }} spacing="md">
          <Field label={t('groups.age')}>
            <IncidentAge createdAt={group.created_at} resolvedAt={group.resolved_at} />
          </Field>
          <Field label={t('common.integration')}>
            {integration ? (
              <Anchor component={Link} to={`/integrations/${integration.id}`} size="sm">
                {integration.name}
              </Anchor>
            ) : naming ? (
              <Skeleton height={16} width={120} />
            ) : (
              <MonoId id={group.integration_id} />
            )}
          </Field>
          <Field label={t('group.escalationChain')}>
            {chain ? (
              <Anchor component={Link} to="/escalation-chains" size="sm">
                {chain.name}
              </Anchor>
            ) : naming ? (
              <Skeleton height={16} width={120} />
            ) : (
              <MonoId id={group.escalation_chain_id} />
            )}
          </Field>
          <Field label={t('group.nextEscalation')}>
            {group.next_run_at ? (
              <Text size="sm">
                {t('group.nextEscalationAt', {
                  step: String((group.current_step ?? 0) + 1),
                })}{' '}
                <RelativeTime value={group.next_run_at} />
              </Text>
            ) : (
              <Text size="sm" c="dimmed">
                {t('group.noNextEscalation')}
              </Text>
            )}
          </Field>
        </SimpleGrid>

        <div>
          <Button variant="subtle" size="compact-sm" onClick={details.toggle} px={0}>
            {showAll ? t('group.hideAllFields') : t('group.showAllFields')}
          </Button>
          <Collapse in={showAll}>
            <SimpleGrid cols={{ base: 2, sm: 3, lg: 4 }} spacing="md" mt="sm">
              {secondary.map(([label, value]) => (
                <Field key={label} label={label}>
                  {value}
                </Field>
              ))}
            </SimpleGrid>
          </Collapse>
        </div>
      </Stack>
    </Card>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
        {label}
      </Text>
      <Group mt={4}>{children}</Group>
    </div>
  );
}

/**
 * The timeline as a story, not as the rows that store it.
 *
 * Every entry carries a type, an actor and a data bag; rendering them raw put
 * `actor: {"id":"","kind":"system"}` and a log id on screen next to the one
 * sentence that mattered, and titled every entry "event". The type is what
 * names the event — in the reader's language — the actor is shown only when a
 * person is behind it, and the data bag contributes the few keys that change
 * the meaning. Whatever is left is not lost: the Raw tab still holds the group
 * exactly as the API returned it.
 */
function GroupTimeline({ group }: { group: AlertGroup }) {
  const { t } = useI18n();
  const logs = group.logs ?? [];
  if (logs.length === 0) {
    return (
      <Text size="sm" c="dimmed">
        {t('group.noTimeline')}
      </Text>
    );
  }
  // Newest first: during an incident the last thing that happened is the thing
  // being asked about.
  const entries = [...logs].reverse();
  return (
    <Timeline active={entries.length} bulletSize={16} lineWidth={2}>
      {entries.map((raw, index) => (
        <TimelineEntry key={entryId(raw, index)} entry={raw as Record<string, unknown>} />
      ))}
    </Timeline>
  );
}

function entryId(raw: unknown, index: number): string {
  const id = (raw as Record<string, unknown> | null)?.id;
  return typeof id === 'string' ? id : String(index);
}

/** Event types that mean something went wrong with delivery or configuration. */
const PROBLEM_EVENTS = new Set([
  'missing_chain',
  'no_duty_users',
  'schedule_gap',
  'team_empty',
  'emergency_missing',
  'policy_step_no_target',
  'notify_skipped_unknown_users',
  'escalation_repeat_exhausted',
]);

/** Data keys worth showing next to an event, in the order they read best. */
const DETAIL_KEYS = [
  'users',
  'user_names',
  'channels',
  'targets',
  'step',
  'step_kind',
  'duration_minutes',
  'until',
  'reason',
  'window',
  'url',
  'status',
];

function TimelineEntry({ entry }: { entry: Record<string, unknown> }) {
  const { t } = useI18n();
  const type = typeof entry.type === 'string' ? entry.type : '';
  const actor = (entry.actor ?? {}) as Record<string, unknown>;
  const actorKind = typeof actor.kind === 'string' ? actor.kind : 'system';
  const actorName = typeof actor.name === 'string' ? actor.name : '';
  const data = (entry.data ?? {}) as Record<string, unknown>;
  const at = typeof entry.created_at === 'string' ? entry.created_at : null;

  // A type this UI has no wording for falls back to the message the server
  // wrote, and only then to the wire name: an unnamed event is still readable,
  // and the odd-looking name is the prompt to add it to the catalog.
  const title = translateEvent(t, type) ?? asString(entry.message) ?? type ?? t('group.event');

  const details = DETAIL_KEYS.filter((key) => data[key] !== undefined && data[key] !== null && data[key] !== '')
    .map((key) => [key, formatValue(data[key])] as const);

  return (
    <Timeline.Item
      // A plain string, not markup: Mantine renders the title inside a <p>, and
      // nesting block elements there is invalid HTML that React warns about.
      title={title}
      c={PROBLEM_EVENTS.has(type) ? 'orange' : undefined}
      color={PROBLEM_EVENTS.has(type) ? 'orange' : undefined}
    >
      <Group gap={6} wrap="wrap">
        <Text size="xs" c="dimmed" component="span">
          <AbsoluteTime value={at} />
        </Text>
        {actorKind !== 'system' && actorName && (
          <Text size="xs" c="dimmed" component="span">
            {t('group.byActor', { actor: actorName })}
          </Text>
        )}
      </Group>
      {details.length > 0 && (
        <Group gap="xs" mt={6} wrap="wrap">
          {details.map(([key, value]) => (
            <Text size="xs" key={key}>
              <Text span c="dimmed">
                {t(`timelineData.${key}` as StringKey) || key}:{' '}
              </Text>
              {value}
            </Text>
          ))}
        </Group>
      )}
    </Timeline.Item>
  );
}

function asString(value: unknown): string | undefined {
  return typeof value === 'string' && value !== '' ? value : undefined;
}

function formatValue(value: unknown): string {
  if (Array.isArray(value)) return value.map((item) => formatValue(item)).join(', ');
  if (value !== null && typeof value === 'object') return JSON.stringify(value);
  return String(value);
}

/**
 * Wording for an event type, or `undefined` when the catalog has no entry.
 *
 * The catalog is keyed `timeline.<type>`, so a type the backend adds shows the
 * server's own English sentence until somebody writes the translation — visibly
 * unlocalised rather than silently missing.
 */
function translateEvent(t: (key: StringKey) => string, type: string): string | undefined {
  if (!type) return undefined;
  const key = `timeline.${type}` as StringKey;
  const translated = (t as (k: StringKey) => string | undefined)(key);
  return translated ?? undefined;
}

/**
 * What a responder needs to know before trusting this page: which of the
 * notifications for this group never reached anyone.
 *
 * Two different failures, kept apart on purpose. "Permanently failed" means a
 * provider was tried and gave up — someone should be paged another way.
 * "Skipped" means the channel has no transport configured here, so nothing was
 * ever sent; that is an operator's configuration problem, not an outage.
 */
function DeliveryHealthBanner({ notifications }: { notifications: Notification[] }) {
  const { t, plural } = useI18n();
  const channelLabel = useChannelLabel();
  const failed = notifications.filter((n) => n.status === 'failed');
  const skipped = notifications.filter((n) => n.status === 'skipped');
  if (failed.length === 0 && skipped.length === 0) return null;

  const channels = (items: Notification[]) =>
    Array.from(new Set(items.map((n) => channelLabel(n.channel)))).join(', ');

  return (
    <Alert
      color={failed.length > 0 ? 'red' : 'orange'}
      icon={<IconAlertTriangle size={16} />}
      title={t('group.deliveryProblemTitle')}
    >
      <Stack gap={2}>
        {failed.length > 0 && (
          <Text size="sm">
            {plural('group.deliveryFailed', failed.length, { channels: channels(failed) })}
          </Text>
        )}
        {skipped.length > 0 && (
          <Text size="sm">
            {plural('group.deliverySkipped', skipped.length, { channels: channels(skipped) })}
          </Text>
        )}
      </Stack>
    </Alert>
  );
}

function NotificationRow({ notification }: { notification: Notification }) {
  const { t } = useI18n();
  const channelLabel = useChannelLabel();
  return (
    <Card
      withBorder={false}
      p="md"
      style={{ borderBottom: '1px solid var(--mantine-color-default-border)' }}
    >
      <Group justify="space-between" wrap="nowrap" align="flex-start">
        <Stack gap={4}>
          <Group gap="xs">
            <StatusBadge status={notification.status} />
            <Text size="sm" fw={500}>
              {channelLabel(notification.channel)}
            </Text>
            {notification.target && (
              <Text size="sm" c="dimmed">
                → {notification.target}
              </Text>
            )}
          </Group>
          <Text size="xs" c="dimmed">
            {notification.reason || EMPTY_VALUE}
            {notification.retry_count
              ? ` · ${t('group.retries', { count: notification.retry_count })}`
              : ''}
          </Text>
          {notification.last_error && (
            <Text size="xs" c="red">
              {notification.last_error}
            </Text>
          )}
        </Stack>
        <AbsoluteTime value={notification.created_at} />
      </Group>
    </Card>
  );
}
