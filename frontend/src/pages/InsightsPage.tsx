import { useMemo, useState } from 'react';
import {
  Anchor,
  Group,
  Paper,
  Select,
  SimpleGrid,
  Stack,
  Table,
  Text,
  Title,
} from '@mantine/core';
import { Link } from 'react-router-dom';
import { useAllOf, useHistory, useList } from '../api/hooks';
import {
  AbsoluteTime,
  PageHeader,
  QueryState,
  SeverityBadge,
  StatusBadge,
} from '../components/common';
import { DistributionBars, StatTile, type DistributionRow } from '../components/StatTile';
import { useI18n } from '../i18n/I18nProvider';
import { useSeverityLabel, useStatusLabel } from '../i18n/domain';

/** Counts come from the `total` of a filtered list query — one COUNT per bucket. */
function useCount(resource: 'alert-groups' | 'notifications', filter: Record<string, string>) {
  return useList(resource, { limit: 1, ...filter });
}

/** Drop empty values so an unset integration doesn't send integration_id=. */
function compact(filter: Record<string, string | undefined>): Record<string, string> {
  return Object.fromEntries(
    Object.entries(filter).filter(([, v]) => v != null && v !== ''),
  ) as Record<string, string>;
}

const RANGES = [
  { value: '24h', labelKey: 'insights.last24h', hours: 24 },
  { value: '7d', labelKey: 'insights.last7d', hours: 24 * 7 },
  { value: '30d', labelKey: 'insights.last30d', hours: 24 * 30 },
] as const;

export function InsightsPage() {
  const { t } = useI18n();
  const severityLabel = useSeverityLabel();
  const statusLabel = useStatusLabel();
  const [range, setRange] = useState('7d');
  const [integration, setIntegration] = useState<string | null>(null);

  const hours = RANGES.find((item) => item.value === range)?.hours ?? 168;
  // Memoised on the range, not recomputed per render: `from` goes into the
  // history query key, so a fresh timestamp on every render makes every render
  // a new query. Each response then re-rendered the page, which produced
  // another timestamp — a refetch loop against /api/v1/history, which is the
  // widest read in the service (alert groups with their notifications and
  // delivery attempts inlined). It settled only because nothing forced a
  // render; under React Query's own updates it did not settle at all.
  const from = useMemo(
    () => new Date(Date.now() - hours * 3600 * 1000).toISOString(),
    [hours],
  );

  const integrations = useAllOf('integrations');

  // The integration selector scopes every count on the page — the tiles and both
  // distributions — not just the history table below. Passing it to each count is
  // what keeps the page honest: a number under an integration is that
  // integration's number.
  const scope = (filter: Record<string, string>) => compact({ ...filter, integration_id: integration ?? undefined });

  const open = useCount('alert-groups', scope({ status: 'open' }));
  const acknowledged = useCount('alert-groups', scope({ status: 'acknowledged' }));
  const resolved = useCount('alert-groups', scope({ status: 'resolved' }));

  const critical = useCount('alert-groups', scope({ severity: 'critical' }));
  const error = useCount('alert-groups', scope({ severity: 'error' }));
  const warning = useCount('alert-groups', scope({ severity: 'warning' }));
  const info = useCount('alert-groups', scope({ severity: 'info' }));

  const delivered = useCount('notifications', scope({ status: 'delivered' }));
  const scheduled = useCount('notifications', scope({ status: 'delivery_scheduled' }));
  const retrying = useCount('notifications', scope({ status: 'retry_scheduled' }));
  const failed = useCount('notifications', scope({ status: 'failed' }));
  // Skipped is its own outcome, not a rounding error: leaving it out of the
  // distribution made a deployment where nothing could be delivered look calm.
  const skipped = useCount('notifications', scope({ status: 'skipped' }));
  const batched = useCount('notifications', scope({ status: 'batched' }));

  const history = useHistory({
    from,
    integration: integration ?? undefined,
    limit: 50,
  });

  const severityRows: DistributionRow[] = [
    { label: severityLabel('critical'), value: critical.data?.total ?? 0, color: 'red' },
    { label: severityLabel('error'), value: error.data?.total ?? 0, color: 'orange' },
    { label: severityLabel('warning'), value: warning.data?.total ?? 0, color: 'yellow' },
    { label: severityLabel('info'), value: info.data?.total ?? 0, color: 'blue' },
  ];

  const deliveryRows: DistributionRow[] = [
    { label: statusLabel('delivered'), value: delivered.data?.total ?? 0, color: 'teal' },
    { label: t('insights.scheduled'), value: scheduled.data?.total ?? 0, color: 'blue' },
    { label: statusLabel('retrying'), value: retrying.data?.total ?? 0, color: 'orange' },
    { label: statusLabel('failed'), value: failed.data?.total ?? 0, color: 'red' },
    { label: t('insights.skippedNoTransport'), value: skipped.data?.total ?? 0, color: 'gray' },
    { label: statusLabel('batched'), value: batched.data?.total ?? 0, color: 'grape' },
  ];

  return (
    <>
      <PageHeader
        title={t('insights.title')}
        description={t('insights.description')}
      />

      <Paper withBorder p="md" mb="lg">
        <Group gap="sm" align="flex-end">
          <Select
            label={t('common.integration')}
            description={t('insights.scopeDescription')}
            placeholder={t('insights.allIntegrations')}
            clearable
            searchable
            data={(integrations.data ?? []).map((item) => ({ value: item.id, label: item.name }))}
            value={integration}
            onChange={setIntegration}
            w={260}
          />
          <Select
            label={t('insights.range')}
            description={t('insights.rangeDescription')}
            data={RANGES.map(({ value, labelKey }) => ({ value, label: t(labelKey) }))}
            value={range}
            onChange={(value) => setRange(value ?? '7d')}
            allowDeselect={false}
            w={200}
          />
          <Text size="sm" c="dimmed" ml="auto">
            {t('insights.groupsInRange', { count: history.data?.total ?? 0 })}
          </Text>
        </Group>
      </Paper>

      <Text size="sm" c="dimmed" mb="xs">
        {t('insights.currentTotals', { scope: integration ? t('insights.selectedScope') : '' })}
      </Text>
      <SimpleGrid cols={{ base: 2, sm: 4 }} mb="lg">
        <StatTile
          label={t('insights.open')}
          value={open.data?.total}
          color="red"
          loading={open.isPending}
          hint={t('insights.awaitingAck')}
        />
        <StatTile
          label={t('insights.acknowledged')}
          value={acknowledged.data?.total}
          color="yellow"
          loading={acknowledged.isPending}
          hint={t('insights.beingWorked')}
        />
        <StatTile
          label={t('insights.resolved')}
          value={resolved.data?.total}
          color="teal"
          loading={resolved.isPending}
          hint={t('insights.withinRetention')}
        />
        <StatTile
          label={t('insights.failedNotifications')}
          value={failed.data?.total}
          color="red"
          loading={failed.isPending}
          hint={t('insights.retriesExhausted')}
        />
      </SimpleGrid>

      <SimpleGrid cols={{ base: 1, md: 2 }} mb="lg">
        <Paper withBorder p="lg">
          <Title order={5} mb="md">
            {t('insights.bySeverity')}
          </Title>
          <DistributionBars rows={severityRows} />
        </Paper>
        <Paper withBorder p="lg">
          <Title order={5} mb="md">
            {t('insights.byDelivery')}
          </Title>
          <DistributionBars rows={deliveryRows} />
        </Paper>
      </SimpleGrid>

      <Title order={5} mb="xs">
        {t('insights.history', {
          range: t(RANGES.find((r) => r.value === range)?.labelKey ?? 'insights.last7d').toLowerCase(),
          scope: integration ? t('insights.selectedIntegration') : '',
        })}
      </Title>
      <Paper withBorder>
        <QueryState
          query={history}
          isEmpty={(data) => data.items.length === 0}
          emptyLabel={t('insights.empty')}
        >
          {(data) => (
            <Table.ScrollContainer minWidth={900}>
              <Table highlightOnHover verticalSpacing="sm">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t('insights.alertGroup')}</Table.Th>
                    <Table.Th w={130}>{t('common.status')}</Table.Th>
                    <Table.Th w={110}>{t('common.severity')}</Table.Th>
                    <Table.Th w={90}>{t('nav.alerts')}</Table.Th>
                    <Table.Th w={130}>{t('nav.notifications')}</Table.Th>
                    <Table.Th w={180}>{t('common.created')}</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {data.items.map((item) => (
                    <Table.Tr key={item.alert_group.id}>
                      <Table.Td>
                        <Stack gap={2}>
                          <Anchor
                            component={Link}
                            to={`/alert-groups/${item.alert_group.id}`}
                            size="sm"
                            fw={500}
                          >
                            {item.alert_group.title || item.alert_group.dedupe_key}
                          </Anchor>
                          <Text size="xs" c="dimmed">
                            {t('insights.timelineEntries', { count: (item.timeline ?? []).length })}
                          </Text>
                        </Stack>
                      </Table.Td>
                      <Table.Td>
                        <StatusBadge status={item.alert_group.status} />
                      </Table.Td>
                      <Table.Td>
                        <SeverityBadge severity={item.alert_group.severity} />
                      </Table.Td>
                      <Table.Td>
                        <Text size="sm">{item.alerts?.length ?? 0}</Text>
                      </Table.Td>
                      <Table.Td>
                        <Text size="sm">
                          {item.notifications?.length ?? 0}
                          {item.delivery_attempts?.length
                            ? t('insights.attempts', { count: item.delivery_attempts.length })
                            : ''}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        <AbsoluteTime value={item.alert_group.created_at} />
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          )}
        </QueryState>
      </Paper>
    </>
  );
}
