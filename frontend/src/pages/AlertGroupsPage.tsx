import { useMemo, useState } from 'react';
import {
  ActionIcon,
  Button,
  Checkbox,
  Group,
  Menu,
  Pagination,
  Paper,
  Select,
  Stack,
  Table,
  Text,
} from '@mantine/core';
import {
  IconBellOff,
  IconCheck,
  IconChevronDown,
  IconDotsVertical,
  IconRefresh,
  IconRotateClockwise,
} from '@tabler/icons-react';
import { Link } from 'react-router-dom';
import {
  useAllOf,
  useBulkGroupAction,
  useGroupAction,
  useList,
  useSilenceGroup,
} from '../api/hooks';
import type { AlertGroup } from '../api/types';
import {
  Labels,
  MonoId,
  PageHeader,
  QueryState,
  RelativeTime,
  SeverityBadge,
  SortControl,
  StatusBadge,
} from '../components/common';
import { useI18n } from '../i18n/I18nProvider';
import { useSeverityLabel, useStatusLabel } from '../i18n/domain';
import type { StringKey } from '../i18n/I18nProvider';
import type { Messages } from '../i18n/messages';

const PAGE_SIZE = 25;

/**
 * Columns a group listing may be ordered by. They are typed columns on the
 * table, which is what lets the ordering happen in the query instead of over
 * the current page.
 */
const SORT_OPTIONS: Array<{ value: string; labelKey: StringKey }> = [
  { value: 'last_received_at', labelKey: 'sort.lastReceivedAt' },
  { value: 'created_at', labelKey: 'sort.createdAt' },
  { value: 'severity', labelKey: 'sort.severity' },
  { value: 'alert_count', labelKey: 'sort.alertCount' },
];

/**
 * Silence presets. The label is a catalog key rather than text: "4 hours" and
 * «4 часа» differ by more than the noun, and these are also rendered from the
 * group detail page, which imports this list.
 */
const SILENCE_OPTIONS: Array<{ labelKey: Extract<keyof Messages, `groups.silence${string}`>; minutes: number }> = [
  { labelKey: 'groups.silence30m', minutes: 30 },
  { labelKey: 'groups.silence1h', minutes: 60 },
  { labelKey: 'groups.silence4h', minutes: 240 },
  { labelKey: 'groups.silence24h', minutes: 1440 },
];

export function AlertGroupsPage() {
  const { t, plural } = useI18n();
  const statusLabel = useStatusLabel();
  const severityLabel = useSeverityLabel();
  const [status, setStatus] = useState<string | null>(null);
  const [severity, setSeverity] = useState<string | null>(null);
  const [integrationId, setIntegrationId] = useState<string | null>(null);
  const [page, setPage] = useState(1);
  const [sort, setSort] = useState({ field: 'last_received_at', desc: true });
  const [selected, setSelected] = useState<string[]>([]);

  const integrations = useAllOf('integrations');
  const groups = useList(
    'alert-groups',
    {
      limit: PAGE_SIZE,
      offset: (page - 1) * PAGE_SIZE,
      status: status ?? undefined,
      severity: severity ?? undefined,
      integration_id: integrationId ?? undefined,
      sort: sort.field,
      order: sort.desc ? 'desc' : 'asc',
    },
    { refetchInterval: 15_000 },
  );

  const groupAction = useGroupAction();
  const silence = useSilenceGroup();
  const bulk = useBulkGroupAction();

  const integrationName = useMemo(() => {
    const map = new Map<string, string>();
    for (const integration of integrations.data ?? []) map.set(integration.id, integration.name);
    return map;
  }, [integrations.data]);

  const items = groups.data?.items ?? [];
  const total = groups.data?.total ?? 0;
  const allSelected = items.length > 0 && selected.length === items.length;

  const toggleAll = () => setSelected(allSelected ? [] : items.map((item) => item.id));
  const toggleOne = (id: string) =>
    setSelected((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));

  const runBulk = (action: 'bulk-resolve' | 'bulk-acknowledge' | 'bulk-silence', minutes?: number) =>
    bulk.mutate(
      { action, groupIds: selected, durationMinutes: minutes },
      { onSuccess: () => setSelected([]) },
    );

  return (
    <>
      <PageHeader
        title={t('groups.title')}
        description={t('groups.description')}
        actions={
          <Button
            variant="default"
            leftSection={<IconRefresh size={16} />}
            onClick={() => groups.refetch()}
          >
            {t('common.refresh')}
          </Button>
        }
      />

      <Paper withBorder p="md" mb="md">
        <Group align="flex-end" gap="sm" wrap="wrap">
          <Select
            label={t('common.status')}
            placeholder={t('common.any')}
            clearable
            data={['open', 'acknowledged', 'resolved'].map((value) => ({
              value,
              label: statusLabel(value),
            }))}
            value={status}
            onChange={(value) => {
              setStatus(value);
              setPage(1);
            }}
            w={160}
          />
          <Select
            label={t('common.severity')}
            placeholder={t('common.any')}
            clearable
            data={['critical', 'error', 'warning', 'info', 'debug'].map((value) => ({
              value,
              label: severityLabel(value),
            }))}
            value={severity}
            onChange={(value) => {
              setSeverity(value);
              setPage(1);
            }}
            w={160}
          />
          <Select
            label={t('common.integration')}
            placeholder={t('common.any')}
            clearable
            searchable
            data={(integrations.data ?? []).map((i) => ({ value: i.id, label: i.name }))}
            value={integrationId}
            onChange={(value) => {
              setIntegrationId(value);
              setPage(1);
            }}
            w={220}
          />
          <SortControl
            options={SORT_OPTIONS}
            field={sort.field}
            desc={sort.desc}
            onChange={(next) => {
              setSort(next);
              setPage(1);
            }}
          />
          <Group gap="xs" ml="auto">
            <Text size="sm" c="dimmed">
              {selected.length > 0
                ? plural('groups.selected', selected.length)
                : plural('groups.total', total)}
            </Text>
            <Button
              size="sm"
              variant="light"
              disabled={selected.length === 0}
              loading={bulk.isPending}
              onClick={() => runBulk('bulk-acknowledge')}
            >
              {t('groups.acknowledge')}
            </Button>
            <Button
              size="sm"
              variant="light"
              color="teal"
              disabled={selected.length === 0}
              loading={bulk.isPending}
              onClick={() => runBulk('bulk-resolve')}
            >
              {t('groups.resolve')}
            </Button>
            <Menu withinPortal>
              <Menu.Target>
                <Button
                  size="sm"
                  variant="light"
                  color="gray"
                  disabled={selected.length === 0}
                  rightSection={<IconChevronDown size={14} />}
                >
                  {t('groups.silence')}
                </Button>
              </Menu.Target>
              <Menu.Dropdown>
                {SILENCE_OPTIONS.map((option) => (
                  <Menu.Item
                    key={option.minutes}
                    onClick={() => runBulk('bulk-silence', option.minutes)}
                  >
                    {t(option.labelKey)}
                  </Menu.Item>
                ))}
              </Menu.Dropdown>
            </Menu>
          </Group>
        </Group>
      </Paper>

      <Paper withBorder>
        <QueryState
          query={groups}
          isEmpty={(data) => (data.items?.length ?? 0) === 0}
          emptyLabel={t('groups.empty')}
        >
          {() => (
            <Table.ScrollContainer minWidth={1000}>
              <Table highlightOnHover verticalSpacing="sm">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th w={40}>
                      <Checkbox
                        checked={allSelected}
                        indeterminate={selected.length > 0 && !allSelected}
                        onChange={toggleAll}
                        aria-label={t('common.selectAll')}
                      />
                    </Table.Th>
                    <Table.Th>{t('common.title')}</Table.Th>
                    <Table.Th w={130}>{t('common.status')}</Table.Th>
                    <Table.Th w={110}>{t('common.severity')}</Table.Th>
                    <Table.Th w={180}>{t('common.integration')}</Table.Th>
                    <Table.Th w={80}>{t('groups.alerts')}</Table.Th>
                    <Table.Th w={140}>{t('groups.lastAlert')}</Table.Th>
                    <Table.Th w={60} />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {items.map((group) => (
                    <Row
                      key={group.id}
                      group={group}
                      integrationName={
                        group.integration_id ? integrationName.get(group.integration_id) : undefined
                      }
                      selected={selected.includes(group.id)}
                      onToggle={() => toggleOne(group.id)}
                      onAction={(action) => groupAction.mutate({ id: group.id, action })}
                      onSilence={(minutes) =>
                        silence.mutate({ id: group.id, durationMinutes: minutes })
                      }
                    />
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          )}
        </QueryState>
      </Paper>

      {total > PAGE_SIZE && (
        <Group justify="center" mt="md">
          <Pagination value={page} onChange={setPage} total={Math.ceil(total / PAGE_SIZE)} />
        </Group>
      )}
    </>
  );
}

function Row({
  group,
  integrationName,
  selected,
  onToggle,
  onAction,
  onSilence,
}: {
  group: AlertGroup;
  integrationName: string | undefined;
  selected: boolean;
  onToggle: () => void;
  onAction: (action: 'acknowledge' | 'unacknowledge' | 'resolve' | 'unresolve') => void;
  onSilence: (minutes: number) => void;
}) {
  const { t } = useI18n();
  return (
    <Table.Tr bg={selected ? 'var(--mantine-color-blue-light)' : undefined}>
      <Table.Td>
        <Checkbox
          checked={selected}
          onChange={onToggle}
          aria-label={t('common.select', { name: group.id })}
        />
      </Table.Td>
      <Table.Td>
        <Stack gap={4}>
          <Text component={Link} to={`/alert-groups/${group.id}`} fw={500} size="sm">
            {group.title || group.dedupe_key || group.id}
          </Text>
          <Labels labels={group.labels} />
        </Stack>
      </Table.Td>
      <Table.Td>
        <StatusBadge status={group.status} />
      </Table.Td>
      <Table.Td>
        <SeverityBadge severity={group.severity} />
      </Table.Td>
      <Table.Td>
        {integrationName ? (
          <Text size="sm">{integrationName}</Text>
        ) : (
          <MonoId id={group.integration_id} />
        )}
      </Table.Td>
      <Table.Td>
        <Text size="sm">{group.alert_count ?? group.alert_ids?.length ?? 0}</Text>
      </Table.Td>
      <Table.Td>
        <RelativeTime value={group.last_received_at ?? group.created_at} />
      </Table.Td>
      <Table.Td>
        <Menu withinPortal position="bottom-end">
          <Menu.Target>
            <ActionIcon variant="subtle" aria-label={t('common.actions')}>
              <IconDotsVertical size={16} />
            </ActionIcon>
          </Menu.Target>
          <Menu.Dropdown>
            {group.status === 'acknowledged' ? (
              <Menu.Item
                leftSection={<IconRotateClockwise size={14} />}
                onClick={() => onAction('unacknowledge')}
              >
                {t('groups.unacknowledge')}
              </Menu.Item>
            ) : (
              <Menu.Item
                leftSection={<IconCheck size={14} />}
                onClick={() => onAction('acknowledge')}
              >
                {t('groups.acknowledge')}
              </Menu.Item>
            )}
            {group.status === 'resolved' ? (
              <Menu.Item
                leftSection={<IconRotateClockwise size={14} />}
                onClick={() => onAction('unresolve')}
              >
                {t('groups.unresolve')}
              </Menu.Item>
            ) : (
              <Menu.Item leftSection={<IconCheck size={14} />} onClick={() => onAction('resolve')}>
                {t('groups.resolve')}
              </Menu.Item>
            )}
            <Menu.Divider />
            <Menu.Label>{t('groups.silence')}</Menu.Label>
            {SILENCE_OPTIONS.map((option) => (
              <Menu.Item
                key={option.minutes}
                leftSection={<IconBellOff size={14} />}
                onClick={() => onSilence(option.minutes)}
              >
                {t(option.labelKey)}
              </Menu.Item>
            ))}
          </Menu.Dropdown>
        </Menu>
      </Table.Td>
    </Table.Tr>
  );
}

/** Exported so other pages can reuse the same silence presets. */
export { SILENCE_OPTIONS };
