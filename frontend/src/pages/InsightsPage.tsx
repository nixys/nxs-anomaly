import { useMemo } from 'react';
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
import { useAllOf, useHistory, useInsightsSummary } from '../api/hooks';
import {
  AbsoluteTime,
  PageHeader,
  QueryState,
  SeverityBadge,
  StatusBadge,
} from '../components/common';
import { DistributionBars, StatTile, type DistributionRow } from '../components/StatTile';
import { TrendChart } from '../components/TrendChart';
import { SEVERITY_COLOR, SEVERITY_LEVELS } from '../domain/severity';
import { useListParams } from '../ui/useListParams';
import { useI18n } from '../i18n/I18nProvider';
import { useSeverityLabel, useStatusLabel } from '../i18n/domain';

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
  // The scope of this screen lives in the address, like every other list: a
  // colleague asking "why do you say deliveries got worse" should be able to
  // open the same numbers, not rebuild the filters from a description.
  const { get, patch } = useListParams({ range: '7d' });
  const range = get('range') ?? '7d';
  const integration = get('integration');

  const hours = RANGES.find((item) => item.value === range)?.hours ?? 168;
  // Memoised on the range, not recomputed per render: `from` goes into the
  // query key, so a fresh timestamp on every render makes every render a new
  // query — and every response another render.
  const from = useMemo(
    () => new Date(Date.now() - hours * 3600 * 1000).toISOString(),
    [hours],
  );

  const integrations = useAllOf('integrations');

  // One request for the whole screen. It used to be twelve — one COUNT per tile
  // and per distribution row — none of which could say whether the numbers are
  // going up or down.
  const summary = useInsightsSummary(compact({ from, integration_id: integration ?? undefined }));

  const history = useHistory({
    from,
    integration: integration ?? undefined,
    limit: 50,
  });

  const groups = summary.data?.groups_by_status ?? {};
  const levels = summary.data?.groups_by_level ?? {};
  const states = summary.data?.notifications_by_state ?? {};
  const trend = summary.data?.trend ?? [];
  const days = trend.map((bucket) => bucket.day);

  // Levels, not spellings: a group a source labelled "high" is counted under
  // "error" here, exactly as the filter and the ordering count it. The label
  // says so, otherwise a reader comparing this bar with the badges in the table
  // below sees two different vocabularies for one fact.
  const severityRows: DistributionRow[] = SEVERITY_LEVELS.map((level) => ({
    label: severityLabel(level),
    value: levels[level] ?? 0,
    color: SEVERITY_COLOR[level],
  }));

  const deliveryRows: DistributionRow[] = [
    { label: statusLabel('delivered'), value: states.delivered ?? 0, color: 'teal' },
    { label: t('insights.scheduled'), value: states.delivery_scheduled ?? 0, color: 'blue' },
    { label: statusLabel('retrying'), value: states.retry_scheduled ?? 0, color: 'orange' },
    { label: statusLabel('failed'), value: states.failed ?? 0, color: 'red' },
    { label: t('insights.skippedNoTransport'), value: states.skipped ?? 0, color: 'gray' },
    { label: statusLabel('batched'), value: states.batched ?? 0, color: 'grape' },
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
            onChange={(value) => patch({ integration: value })}
            w={260}
          />
          <Select
            label={t('insights.range')}
            description={t('insights.rangeDescription')}
            data={RANGES.map(({ value, labelKey }) => ({ value, label: t(labelKey) }))}
            value={range}
            onChange={(value) => patch({ range: value ?? '7d' })}
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
          value={groups.open ?? 0}
          color="red"
          loading={summary.isPending}
          hint={t('insights.awaitingAck')}
        />
        <StatTile
          label={t('insights.acknowledged')}
          value={groups.acknowledged ?? 0}
          color="yellow"
          loading={summary.isPending}
          hint={t('insights.beingWorked')}
        />
        <StatTile
          label={t('insights.resolved')}
          value={groups.resolved ?? 0}
          color="teal"
          loading={summary.isPending}
          hint={t('insights.withinRetention')}
        />
        <StatTile
          label={t('insights.failedNotifications')}
          value={states.failed ?? 0}
          color="red"
          loading={summary.isPending}
          hint={t('insights.retriesExhausted')}
        />
      </SimpleGrid>

      {days.length > 0 && (
        <SimpleGrid cols={{ base: 1, md: 2 }} mb="lg">
          <TrendChart
            title={t('insights.trendIncidents')}
            days={days}
            series={[
              { key: 'opened', label: t('insights.opened'), color: 'blue', values: trend.map((b) => b.opened) },
              { key: 'resolved', label: t('insights.closed'), color: 'teal', values: trend.map((b) => b.resolved) },
            ]}
            emptyLabel={t('insights.trendEmpty')}
          />
          <TrendChart
            title={t('insights.trendDelivery')}
            days={days}
            series={[
              { key: 'delivered', label: t('status.delivered'), color: 'teal', values: trend.map((b) => b.delivered) },
              { key: 'failed', label: t('status.failed'), color: 'red', values: trend.map((b) => b.failed) },
            ]}
            emptyLabel={t('insights.trendEmpty')}
          />
        </SimpleGrid>
      )}

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
          skeleton={{ rows: 6 }}
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
