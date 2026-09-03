import {
  Alert,
  Anchor,
  Button,
  Card,
  Grid,
  Group,
  Menu,
  Paper,
  SimpleGrid,
  Stack,
  Table,
  Tabs,
  Text,
  Timeline,
} from '@mantine/core';
import {
  IconAlertTriangle,
  IconArrowLeft,
  IconBellOff,
  IconChevronDown,
} from '@tabler/icons-react';
import { Link, useParams } from 'react-router-dom';
import { useGroupAction, useItem, useList, useSilenceGroup } from '../api/hooks';
import type { ReactNode } from 'react';
import type { AlertGroup, Notification } from '../api/types';
import {
  AbsoluteTime,
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
import { useChannelLabel } from '../i18n/domain';
import { EMPTY_VALUE } from '../i18n/format';

export function AlertGroupDetailPage() {
  const { t } = useI18n();
  const { id } = useParams<{ id: string }>();
  const group = useItem('alert-groups', id);
  const alerts = useList('alerts', { alert_group_id: id, limit: 200 }, { enabled: Boolean(id) });
  const notifications = useList(
    'notifications',
    { alert_group_id: id, limit: 200 },
    { enabled: Boolean(id) },
  );
  const action = useGroupAction();
  const silence = useSilenceGroup();

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
  const fields: Array<[string, ReactNode]> = [
    [t('common.status'), <StatusBadge status={group.status} />],
    [t('common.severity'), <SeverityBadge severity={group.severity} />],
    [t('groups.alerts'), <Text size="sm">{group.alert_count ?? group.alert_ids?.length ?? 0}</Text>],
    [t('common.integration'), <MonoId id={group.integration_id} />],
    [t('group.escalationChain'), <MonoId id={group.escalation_chain_id} />],
    [t('group.dedupeKey'), <MonoId id={group.dedupe_key} />],
    [t('group.currentStep'), <Text size="sm">{group.current_step ?? 0}</Text>],
    [t('group.nextEscalation'), <RelativeTime value={group.next_run_at} />],
    [t('groups.lastAlert'), <AbsoluteTime value={group.last_received_at} />],
    [t('group.acknowledgedAt'), <AbsoluteTime value={group.acknowledged_at} />],
    [t('group.resolvedAt'), <AbsoluteTime value={group.resolved_at} />],
    [t('group.silencedUntil'), <AbsoluteTime value={group.silenced_until ?? null} />],
  ];

  return (
    <Card withBorder p="lg">
      <Stack gap="md">
        <div>
          <Text fw={600} size="lg">
            {group.title || group.dedupe_key || group.id}
          </Text>
          <Group mt="xs">
            <Labels labels={group.labels} />
          </Group>
        </div>
        <SimpleGrid cols={{ base: 2, sm: 3, lg: 4 }} spacing="md">
          {fields.map(([label, value]) => (
            <div key={label}>
              <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
                {label}
              </Text>
              <Group mt={4}>{value}</Group>
            </div>
          ))}
        </SimpleGrid>
      </Stack>
    </Card>
  );
}

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
  return (
    <Timeline active={logs.length} bulletSize={14} lineWidth={2}>
      {logs.map((entry, index) => {
        const { at, event, ...rest } = entry;
        const details = Object.entries(rest).filter(([, value]) => value !== null && value !== '');
        return (
          <Timeline.Item key={index} title={String(event ?? t('group.event'))}>
            <Text size="xs" c="dimmed">
              <AbsoluteTime value={typeof at === 'string' ? at : null} />
            </Text>
            {details.length > 0 && (
              <Grid gutter={4} mt={6}>
                {details.map(([key, value]) => (
                  <Grid.Col span={{ base: 12, sm: 6 }} key={key}>
                    <Text size="xs">
                      <Text span c="dimmed">
                        {key}:{' '}
                      </Text>
                      {typeof value === 'object' ? JSON.stringify(value) : String(value)}
                    </Text>
                  </Grid.Col>
                ))}
              </Grid>
            )}
          </Timeline.Item>
        );
      })}
    </Timeline>
  );
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
