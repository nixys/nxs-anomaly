import { useMemo } from 'react';
import {
  Anchor,
  Group,
  Pagination,
  Paper,
  Select,
  Stack,
  Table,
  Text,
} from '@mantine/core';
import { Link } from 'react-router-dom';
import { useAllOf, useList } from '../api/hooks';
import {
  AbsoluteTime,
  Labels,
  MonoId,
  PageHeader,
  QueryState,
  SeverityBadge,
  SortControl,
  StatusBadge,
} from '../components/common';
import { useI18n } from '../i18n/I18nProvider';
import { useListParams } from '../ui/useListParams';
import { useSeverityLabel, useStatusLabel } from '../i18n/domain';
import { EMPTY_VALUE } from '../i18n/format';
import type { StringKey } from '../i18n/I18nProvider';

const PAGE_SIZE = 50;

/** Columns an alert listing may be ordered by — typed columns on the table. */
const SORT_OPTIONS: Array<{ value: string; labelKey: StringKey }> = [
  { value: 'received_at', labelKey: 'sort.receivedAt' },
  { value: 'severity', labelKey: 'sort.severity' },
];

export function AlertsPage() {
  const { t, plural } = useI18n();
  const statusLabel = useStatusLabel();
  const severityLabel = useSeverityLabel();
  // Same rule as the incident queue: the filtered list is a link, not a state
  // that dies with the tab.
  const { get, patch, page, filtered } = useListParams();
  const status = get('status');
  const severity = get('severity');
  const integrationId = get('integration');
  const sort = { field: get('sort') ?? 'received_at', desc: (get('order') ?? 'desc') !== 'asc' };

  const integrations = useAllOf('integrations');
  const alerts = useList('alerts', {
    limit: PAGE_SIZE,
    offset: (page - 1) * PAGE_SIZE,
    status: status ?? undefined,
    severity: severity ?? undefined,
    integration_id: integrationId ?? undefined,
    sort: sort.field,
    order: sort.desc ? 'desc' : 'asc',
  });

  const integrationName = useMemo(() => {
    const map = new Map<string, string>();
    for (const integration of integrations.data ?? []) map.set(integration.id, integration.name);
    return map;
  }, [integrations.data]);

  const total = alerts.data?.total ?? 0;

  // Select options carry the wire value and a translated label: the filter is
  // sent to the API as `critical`, but nobody has to read it that way.
  const statusOptions = ['firing', 'resolved'].map((value) => ({
    value,
    label: statusLabel(value),
  }));
  const severityOptions = ['critical', 'error', 'warning', 'info', 'debug'].map((value) => ({
    value,
    label: severityLabel(value),
  }));

  return (
    <>
      <PageHeader
        title={t('alerts.title')}
        description={t('alerts.description')}
      />

      <Paper withBorder p="md" mb="md">
        <Group align="flex-end" gap="sm" wrap="wrap">
          <Select
            label={t('common.status')}
            placeholder={t('common.any')}
            clearable
            data={statusOptions}
            value={status}
            onChange={(value) => patch({ status: value })}
            w={160}
          />
          <Select
            label={t('common.severity')}
            placeholder={t('common.any')}
            clearable
            data={severityOptions}
            value={severity}
            onChange={(value) => patch({ severity: value })}
            w={160}
          />
          <Select
            label={t('common.integration')}
            placeholder={t('common.any')}
            clearable
            searchable
            data={(integrations.data ?? []).map((i) => ({ value: i.id, label: i.name }))}
            value={integrationId}
            onChange={(value) => patch({ integration: value })}
            w={220}
          />
          <SortControl
            options={SORT_OPTIONS}
            field={sort.field}
            desc={sort.desc}
            onChange={(next) => patch({ sort: next.field, order: next.desc ? 'desc' : 'asc' })}
          />
          <Text size="sm" c="dimmed" ml="auto">
            {plural('alerts.total', total)}
          </Text>
        </Group>
      </Paper>

      <Paper withBorder>
        <QueryState
          query={alerts}
          isEmpty={(data) => data.items.length === 0}
          emptyLabel={filtered ? t('alerts.empty') : t('alerts.emptyUnfiltered')}
          skeleton={{ rows: 8 }}
        >
          {(data) => (
            <Table.ScrollContainer minWidth={1000}>
              <Table highlightOnHover verticalSpacing="sm">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t('common.title')}</Table.Th>
                    <Table.Th w={110}>{t('common.status')}</Table.Th>
                    <Table.Th w={110}>{t('common.severity')}</Table.Th>
                    <Table.Th w={170}>{t('common.integration')}</Table.Th>
                    <Table.Th w={140}>{t('alerts.group')}</Table.Th>
                    <Table.Th w={180}>{t('common.received')}</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {data.items.map((alert) => (
                    <Table.Tr key={alert.id}>
                      <Table.Td>
                        <Stack gap={4}>
                          <Text size="sm" fw={500}>
                            {alert.title || alert.id}
                          </Text>
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
                        <Text size="sm">
                          {integrationName.get(alert.integration_id) ?? (
                            <MonoId id={alert.integration_id} />
                          )}
                        </Text>
                      </Table.Td>
                      <Table.Td>
                        {alert.alert_group_id ? (
                          <Anchor component={Link} to={`/alert-groups/${alert.alert_group_id}`} size="sm">
                            {t('common.open')}
                          </Anchor>
                        ) : (
                          <Text c="dimmed">{EMPTY_VALUE}</Text>
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

      {total > PAGE_SIZE && (
        <Group justify="center" mt="md">
          <Pagination
            value={page}
            onChange={(next) => patch({ page: String(next) }, { keepPage: true })}
            total={Math.ceil(total / PAGE_SIZE)}
          />
        </Group>
      )}
    </>
  );
}
