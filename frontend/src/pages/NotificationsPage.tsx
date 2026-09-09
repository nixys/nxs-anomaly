import { useState } from 'react';
import {
  Alert,
  Anchor,
  Badge,
  Button,
  Drawer,
  Group,
  Pagination,
  Paper,
  Select,
  Stack,
  Table,
  Text,
} from '@mantine/core';
import { IconPlayerPlay } from '@tabler/icons-react';
import { Link } from 'react-router-dom';
import { useAllOf, useDeliveryAttempts, useList, useRunEscalations } from '../api/hooks';
import { NOTIFICATION_TARGET_TYPES, type Notification } from '../api/types';
import {
  AbsoluteTime,
  JsonBlock,
  PageHeader,
  QueryState,
  RelativeTime,
  StatusBadge,
} from '../components/common';
import { useListParams } from '../ui/useListParams';
import { useVisibleInterval } from '../ui/useVisibleInterval';
import { useI18n } from '../i18n/I18nProvider';
import { useChannelLabel, useStatusLabel } from '../i18n/domain';
import { EMPTY_VALUE } from '../i18n/format';

const PAGE_SIZE = 50;

const STATUSES = [
  'delivered',
  'delivery_scheduled',
  'retry_scheduled',
  'failed',
  'skipped',
  'batched',
];

export function NotificationsPage() {
  const { t, plural } = useI18n();
  const statusLabel = useStatusLabel();
  const channelLabel = useChannelLabel();
  // Filters live in the address, like the incident queue: "look at the failed
  // Telegram deliveries" is a link somebody can send, not a description of which
  // three dropdowns to set.
  const { get, patch, page, filtered } = useListParams();
  const status = get('status');
  const channel = get('channel');
  const userId = get('user');
  const [inspecting, setInspecting] = useState<Notification | null>(null);

  const users = useAllOf('users');
  const runEscalations = useRunEscalations();
  const notifications = useList(
    'notifications',
    {
      limit: PAGE_SIZE,
      offset: (page - 1) * PAGE_SIZE,
      status: status ?? undefined,
      channel: channel ?? undefined,
      user_id: userId ?? undefined,
    },
    { refetchInterval: useVisibleInterval(15_000) },
  );

  const userName = new Map((users.data ?? []).map((user) => [user.id, user.name]));
  const total = notifications.data?.total ?? 0;
  const items = notifications.data?.items ?? [];

  // Names for the incidents on this page, fetched in one request rather than one
  // per row. The column used to read "Open" — a link with no subject, which is
  // the one thing a delivery log must never hide: what the delivery was about.
  const groupIds = Array.from(
    new Set(items.map((item) => item.alert_group_id).filter((id): id is string => Boolean(id))),
  );
  const groups = useList(
    'alert-groups',
    { limit: 200, ids: groupIds.join(',') },
    { enabled: groupIds.length > 0 },
  );
  const groupTitle = new Map(
    (groups.data?.items ?? []).map((group) => [
      group.id,
      group.title || group.dedupe_key || group.id,
    ]),
  );

  // Columns whose every value on this page is the same (or empty) carry no
  // information and are hidden: a "Target" column of dashes and a "Retries"
  // column of zeros are three centimetres of screen saying nothing.
  const showTarget = items.some((item) => Boolean(item.target));
  const showRetries = items.some((item) => (item.retry_count ?? 0) > 0);
  const showUser = items.some((item) => Boolean(item.user_id));

  return (
    <>
      <PageHeader
        title={t('notifications.title')}
        description={t('notifications.description')}
        actions={
          <Button
            variant="light"
            leftSection={<IconPlayerPlay size={16} />}
            loading={runEscalations.isPending}
            onClick={() => runEscalations.mutate()}
          >
            {t('notifications.runEscalations')}
          </Button>
        }
      />

      <Paper withBorder p="md" mb="md">
        <Group align="flex-end" gap="sm" wrap="wrap">
          <Select
            label={t('common.status')}
            placeholder={t('common.any')}
            clearable
            data={STATUSES.map((value) => ({ value, label: statusLabel(value) }))}
            value={status}
            onChange={(value) => patch({ status: value })}
            w={190}
          />
          <Select
            label={t('common.channel')}
            placeholder={t('common.any')}
            clearable
            data={NOTIFICATION_TARGET_TYPES.map((value) => ({
              value,
              label: channelLabel(value),
            }))}
            value={channel}
            onChange={(value) => patch({ channel: value })}
            w={160}
          />
          <Select
            label={t('common.user')}
            placeholder={t('common.any')}
            clearable
            searchable
            data={(users.data ?? []).map((user) => ({ value: user.id, label: user.name }))}
            value={userId}
            onChange={(value) => patch({ user: value })}
            w={220}
          />
          <Text size="sm" c="dimmed" ml="auto">
            {plural('notifications.total', total)}
          </Text>
        </Group>
      </Paper>

      <Paper withBorder>
        <QueryState
          query={notifications}
          isEmpty={(data) => data.items.length === 0}
          emptyLabel={filtered ? t('notifications.empty') : t('notifications.emptyUnfiltered')}
          skeleton={{ rows: 8 }}
        >
          {(data) => (
            <Table.ScrollContainer minWidth={900}>
              <Table highlightOnHover verticalSpacing="sm">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th w={160}>{t('common.status')}</Table.Th>
                    <Table.Th>{t('notifications.aboutColumn')}</Table.Th>
                    <Table.Th w={130}>{t('common.channel')}</Table.Th>
                    {showUser && <Table.Th w={170}>{t('common.user')}</Table.Th>}
                    {showTarget && <Table.Th w={200}>{t('common.target')}</Table.Th>}
                    {showRetries && <Table.Th w={90}>{t('notifications.retriesColumn')}</Table.Th>}
                    <Table.Th w={150}>{t('common.created')}</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {data.items.map((notification) => (
                    <Table.Tr
                      key={notification.id}
                      style={{ cursor: 'pointer' }}
                      onClick={() => setInspecting(notification)}
                    >
                      <Table.Td>
                        <StatusBadge status={notification.status} />
                      </Table.Td>
                      <Table.Td onClick={(event) => event.stopPropagation()}>
                        {notification.alert_group_id ? (
                          <Stack gap={2}>
                            <Anchor
                              component={Link}
                              to={`/alert-groups/${notification.alert_group_id}`}
                              size="sm"
                              fw={500}
                            >
                              {groupTitle.get(notification.alert_group_id) ??
                                notification.alert_group_id}
                            </Anchor>
                            {notification.reason && (
                              <Text size="xs" c="dimmed">
                                {notification.reason}
                              </Text>
                            )}
                            {notification.last_error && (
                              <Text size="xs" c="red" lineClamp={1}>
                                {notification.last_error}
                              </Text>
                            )}
                          </Stack>
                        ) : (
                          <Text c="dimmed">{EMPTY_VALUE}</Text>
                        )}
                      </Table.Td>
                      <Table.Td>
                        <Badge variant="default" tt="none" style={{ fontWeight: 400 }}>
                          {channelLabel(notification.channel)}
                        </Badge>
                      </Table.Td>
                      {showUser && (
                        <Table.Td>
                          <Text size="sm">
                            {notification.user_id
                              ? (userName.get(notification.user_id) ?? notification.user_id)
                              : EMPTY_VALUE}
                          </Text>
                        </Table.Td>
                      )}
                      {showTarget && (
                        <Table.Td>
                          <Text size="sm" style={{ wordBreak: 'break-all' }}>
                            {notification.target || EMPTY_VALUE}
                          </Text>
                        </Table.Td>
                      )}
                      {showRetries && (
                        <Table.Td>
                          <Text size="sm">{notification.retry_count ?? 0}</Text>
                        </Table.Td>
                      )}
                      <Table.Td>
                        {/* Relative first, exact on hover: a delivery log is read
                            as "how long ago", and twenty identical absolute
                            timestamps to the second read as noise. */}
                        <RelativeTime value={notification.created_at} />
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          )}
        </QueryState>
      </Paper>

      {total > PAGE_SIZE && (
        <Group justify="center" mt="md">
          <Pagination
            value={page}
            onChange={(next) => patch({ page: String(next) }, { keepPage: true })}
            total={Math.ceil(total / PAGE_SIZE)}
          />
        </Group>
      )}

      <NotificationDrawer notification={inspecting} onClose={() => setInspecting(null)} />
    </>
  );
}

function NotificationDrawer({
  notification,
  onClose,
}: {
  notification: Notification | null;
  onClose: () => void;
}) {
  const attempts = useDeliveryAttempts(notification?.id);
  const { t, fmt } = useI18n();
  const channelLabel = useChannelLabel();

  return (
    <Drawer
      opened={notification !== null}
      onClose={onClose}
      position="right"
      size="lg"
      title={t('notifications.one')}
    >
      {notification && (
        <Stack>
          <Group gap="xs">
            <StatusBadge status={notification.status} />
            <Badge variant="default" tt="none">
              {channelLabel(notification.channel)}
            </Badge>
          </Group>
          <Stack gap={4}>
            <Field label={t('common.target')} value={notification.target || EMPTY_VALUE} />
            <Field label={t('common.reason')} value={notification.reason || EMPTY_VALUE} />
            <Field
              label={t('notifications.idempotencyKey')}
              value={notification.idempotency_key || EMPTY_VALUE}
            />
            <Field
              label={t('notifications.batchKey')}
              value={notification.batch_key || EMPTY_VALUE}
            />
            <Field
              label={t('notifications.providerStatus')}
              value={notification.provider_status || EMPTY_VALUE}
            />
            <Field
              label={t('notifications.lastError')}
              value={notification.last_error || EMPTY_VALUE}
            />
          </Stack>

          {notification.status === 'skipped' && (
            <Alert color="gray" title={t('notifications.skippedTitle')}>
              <Text size="sm">
                {notification.provider_status === 'not_configured'
                  ? t('notifications.skippedNotConfigured')
                  : t('notifications.skippedGeneric')}
              </Text>
              {notification.last_error && (
                <Text size="sm" mt={4}>
                  {notification.last_error}
                </Text>
              )}
            </Alert>
          )}

          <Text fw={600} size="sm">
            {t('notifications.attempts')}
          </Text>
          <QueryState
            query={attempts}
            isEmpty={(data) => (data.items?.length ?? 0) === 0}
            emptyLabel={t('notifications.noAttempts')}
          >
            {(data) => (
              <Table>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th w={50}>#</Table.Th>
                    <Table.Th w={110}>{t('common.status')}</Table.Th>
                    <Table.Th w={150}>{t('notifications.provider')}</Table.Th>
                    <Table.Th w={70}>{t('notifications.took')}</Table.Th>
                    <Table.Th>{t('common.error')}</Table.Th>
                    <Table.Th w={160}>{t('notifications.finished')}</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {data.items.map((attempt) => (
                    <Table.Tr key={attempt.id}>
                      <Table.Td>{attempt.attempt}</Table.Td>
                      <Table.Td>
                        <StatusBadge status={attempt.status} />
                      </Table.Td>
                      <Table.Td>
                        <Text size="xs">
                          {attempt.provider_status || EMPTY_VALUE}
                          {attempt.provider_code ? ` (${attempt.provider_code})` : ''}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        <Text size="xs">
                          {attempt.duration_ms != null ? fmt.duration(attempt.duration_ms) : EMPTY_VALUE}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        <Text size="xs" c={attempt.error ? 'red' : undefined}>
                          {attempt.error || EMPTY_VALUE}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        <AbsoluteTime value={attempt.finished_at} />
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </QueryState>

          <Text fw={600} size="sm">
            {t('common.raw')}
          </Text>
          <JsonBlock value={notification} />
        </Stack>
      )}
    </Drawer>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <Group gap="xs" wrap="nowrap" align="flex-start">
      <Text size="xs" c="dimmed" w={130} style={{ flexShrink: 0 }}>
        {label}
      </Text>
      <Text size="sm" style={{ wordBreak: 'break-all' }}>
        {value}
      </Text>
    </Group>
  );
}
