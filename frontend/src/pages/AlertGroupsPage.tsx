import { useEffect, useMemo, useRef, useState } from 'react';
import {
  ActionIcon,
  Button,
  Checkbox,
  Group,
  Kbd,
  Menu,
  Modal,
  Pagination,
  Paper,
  Select,
  Stack,
  Switch,
  Table,
  Tabs,
  Text,
  Tooltip,
} from '@mantine/core';
import { useDisclosure, useHotkeys, useMediaQuery, useReducedMotion } from '@mantine/hooks';
import {
  IconBellOff,
  IconCheck,
  IconChevronDown,
  IconDotsVertical,
  IconKeyboard,
  IconRefresh,
  IconRotateClockwise,
  IconX,
} from '@tabler/icons-react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import {
  useAllOf,
  useBulkGroupAction,
  useGroupAction,
  useList,
  useSilenceGroup,
} from '../api/hooks';
import type { AlertGroup } from '../api/types';
import {
  IncidentAge,
  Labels,
  MonoId,
  PageHeader,
  QueryState,
  RelativeTime,
  SeverityBadge,
  SortControl,
  StatusBadge,
} from '../components/common';
import { SEVERITY_LEVELS, severityStripe } from '../domain/severity';
import { DURATION, EASE_SETTLE } from '../ui/motion';
import { useVisibleInterval } from '../ui/useVisibleInterval';
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

/**
 * The queues a responder actually works from, as named tabs.
 *
 * Three empty dropdowns are a question ("what do you want to see?"); these are
 * the four answers people give, one click away. Each one is only a set of the
 * same filters below, and picking a view writes them into the address bar — so
 * a view is also a link somebody can paste into a handover.
 */
const VIEWS: Array<{
  value: string;
  labelKey: StringKey;
  filters: { status?: string; severity?: string; sort?: string; order?: 'asc' | 'desc' };
}> = [
  { value: 'firing', labelKey: 'views.firing', filters: { status: 'open', sort: 'severity', order: 'desc' } },
  {
    value: 'critical',
    labelKey: 'views.critical',
    filters: { status: 'open', severity: 'critical', sort: 'created_at', order: 'asc' },
  },
  { value: 'working', labelKey: 'views.working', filters: { status: 'acknowledged' } },
  { value: 'all', labelKey: 'views.all', filters: {} },
];

const DEFAULT_SORT = { field: 'last_received_at', desc: true };

export function AlertGroupsPage() {
  const { t, plural, fmt } = useI18n();
  const statusLabel = useStatusLabel();
  const severityLabel = useSeverityLabel();
  const navigate = useNavigate();

  // Every filter lives in the address bar. A responder who filtered down to the
  // three groups that matter can now send that list to whoever takes over, and
  // a reload keeps the place instead of dropping back to "everything".
  const [params, setParams] = useSearchParams();
  const status = params.get('status');
  const severity = params.get('severity');
  const integrationId = params.get('integration');
  const view = params.get('view') ?? '';
  const page = Math.max(1, Number(params.get('page') ?? 1) || 1);
  const sort = {
    field: params.get('sort') ?? DEFAULT_SORT.field,
    desc: (params.get('order') ?? 'desc') !== 'asc',
  };

  const patchParams = (next: Record<string, string | null>, keepPage = false) => {
    setParams(
      (prev) => {
        const updated = new URLSearchParams(prev);
        for (const [key, value] of Object.entries(next)) {
          if (value === null || value === '') updated.delete(key);
          else updated.set(key, value);
        }
        if (!keepPage) updated.delete('page');
        return updated;
      },
      { replace: true },
    );
  };

  const applyView = (value: string | null) => {
    const preset = VIEWS.find((item) => item.value === value);
    if (!preset) return;
    patchParams({
      view: preset.value,
      status: preset.filters.status ?? null,
      severity: preset.filters.severity ?? null,
      sort: preset.filters.sort ?? null,
      order: preset.filters.order ?? null,
    });
  };

  // Landing with a bare URL lands on the queue, not on the archive: the page
  // exists to answer "what is burning". Writing the preset into the address
  // rather than defaulting silently keeps the rule visible — and the link
  // shareable.
  useEffect(() => {
    if (params.toString() === '') applyView('firing');
    // Only on arrival: re-running this would fight every filter change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const [selected, setSelected] = useState<string[]>([]);
  const [cursor, setCursor] = useState(0);
  const [live, setLive] = useState(true);
  // One list or the other, never both: rendering the table and the cards
  // together and hiding one with CSS doubles the rows a screen reader walks.
  const compact = useMediaQuery('(max-width: 48em)', false);
  const visibleInterval = useVisibleInterval(15_000);
  const [helpOpen, help] = useDisclosure(false);
  const reduceMotion = useReducedMotion();
  const rowRefs = useRef<Array<HTMLTableRowElement | null>>([]);

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
    // Polling stops when the tab is hidden: a queue nobody is looking at does
    // not need a request every fifteen seconds, and a responder with six tabs
    // open should not be six pollers.
    { refetchInterval: live ? visibleInterval : false },
  );

  const groupAction = useGroupAction();
  const silence = useSilenceGroup();
  const bulk = useBulkGroupAction();

  const integrationName = useMemo(() => {
    const map = new Map<string, string>();
    for (const integration of integrations.data ?? []) map.set(integration.id, integration.name);
    return map;
  }, [integrations.data]);

  const items = useMemo(() => groups.data?.items ?? [], [groups.data]);
  // Which rows are new since the last refetch. Auto-refresh every 15 seconds
  // otherwise changes the list in silence: something arrives while the responder
  // is reading and nothing says so.
  const arrived = useArrivals(items.map((item) => item.id));
  // "Nothing matched" and "nothing exists yet" are different screens with
  // different next moves: widen the filter, or connect a source.
  const filtered = Boolean(status || severity || integrationId);
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

  // Keep the keyboard cursor inside the page after a refetch shortens the list.
  useEffect(() => {
    if (cursor > items.length - 1) setCursor(Math.max(0, items.length - 1));
  }, [items.length, cursor]);

  const moveCursor = (delta: number) => {
    if (items.length === 0) return;
    const next = Math.min(items.length - 1, Math.max(0, cursor + delta));
    setCursor(next);
    rowRefs.current[next]?.scrollIntoView({ block: 'nearest' });
  };

  const current = items[cursor];
  const actOnCursor = (action: 'acknowledge' | 'resolve') => {
    if (!current) return;
    groupAction.mutate({ id: current.id, action });
  };

  // The keys are the ones every queue tool shares (j/k to move, x to select,
  // Enter to open) plus the two actions this product exists for. Mantine's hook
  // already ignores keystrokes typed into inputs, so filtering still works.
  useHotkeys([
    ['j', () => moveCursor(1)],
    ['ArrowDown', () => moveCursor(1)],
    ['k', () => moveCursor(-1)],
    ['ArrowUp', () => moveCursor(-1)],
    ['x', () => current && toggleOne(current.id)],
    ['a', () => actOnCursor('acknowledge')],
    ['r', () => actOnCursor('resolve')],
    ['Enter', () => current && navigate(`/alert-groups/${current.id}`)],
    ['Escape', () => setSelected([])],
    ['shift+/', () => help.open()],
  ]);

  const updatedAt = groups.dataUpdatedAt ? fmt.relative(groups.dataUpdatedAt) : null;

  return (
    <>
      <PageHeader
        title={t('groups.title')}
        description={t('groups.description')}
        actions={
          <>
            <Tooltip label={t('groups.shortcutsHint')} withArrow>
              <ActionIcon variant="subtle" onClick={help.open} aria-label={t('groups.shortcuts')}>
                <IconKeyboard size={18} />
              </ActionIcon>
            </Tooltip>
            <Button
              variant="default"
              leftSection={<IconRefresh size={16} />}
              onClick={() => groups.refetch()}
              loading={groups.isFetching}
            >
              {t('common.refresh')}
            </Button>
          </>
        }
      />

      <Tabs value={view || 'custom'} onChange={applyView} mb="md">
        <Tabs.List>
          {VIEWS.map((item) => (
            <Tabs.Tab key={item.value} value={item.value}>
              {t(item.labelKey)}
            </Tabs.Tab>
          ))}
          {!view && <Tabs.Tab value="custom">{t('views.custom')}</Tabs.Tab>}
        </Tabs.List>
      </Tabs>

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
            onChange={(value) => patchParams({ status: value, view: null })}
            w={160}
          />
          <Select
            label={t('common.severity')}
            placeholder={t('common.any')}
            clearable
            // One option per level, not per spelling: filtering by "critical"
            // matches the group a source labelled "P1", because the server
            // expands the level into every word that means it.
            data={SEVERITY_LEVELS.map((value) => ({ value, label: severityLabel(value) }))}
            value={severity}
            onChange={(value) => patchParams({ severity: value, view: null })}
            w={160}
          />
          <Select
            label={t('common.integration')}
            placeholder={t('common.any')}
            clearable
            searchable
            data={(integrations.data ?? []).map((i) => ({ value: i.id, label: i.name }))}
            value={integrationId}
            onChange={(value) => patchParams({ integration: value, view: null })}
            w={220}
          />
          <SortControl
            options={SORT_OPTIONS}
            field={sort.field}
            desc={sort.desc}
            onChange={(next) => patchParams({ sort: next.field, order: next.desc ? 'desc' : 'asc', view: null })}
          />
          <Group gap="sm" ml="auto" align="center">
            <Switch
              size="xs"
              checked={live}
              onChange={(event) => setLive(event.currentTarget.checked)}
              label={
                <Text size="xs" c="dimmed">
                  {live && updatedAt ? t('groups.liveUpdated', { when: updatedAt }) : t('groups.livePaused')}
                </Text>
              }
            />
            <Text size="sm" c="dimmed">
              {plural('groups.total', total)}
            </Text>
          </Group>
        </Group>
      </Paper>

      {/* The bulk bar exists only while something is selected. Three permanently
          disabled buttons taught people to read this strip as decoration. */}
      {selected.length > 0 && (
        <Paper
          withBorder
          p="sm"
          mb="md"
          bg="var(--mantine-color-blue-light)"
          style={{ animation: reduceMotion ? undefined : `nxsSlideDown ${DURATION.panel}ms ${EASE_SETTLE}` }}
        >
          <Group gap="sm" wrap="wrap">
            <Text size="sm" fw={500}>
              {plural('groups.selected', selected.length)}
            </Text>
            <Button
              size="xs"
              variant="light"
              loading={bulk.isPending}
              onClick={() => runBulk('bulk-acknowledge')}
            >
              {t('groups.acknowledge')}
            </Button>
            <Button
              size="xs"
              variant="light"
              color="teal"
              loading={bulk.isPending}
              onClick={() => runBulk('bulk-resolve')}
            >
              {t('groups.resolve')}
            </Button>
            <Menu withinPortal>
              <Menu.Target>
                <Button size="xs" variant="light" color="gray" rightSection={<IconChevronDown size={14} />}>
                  {t('groups.silence')}
                </Button>
              </Menu.Target>
              <Menu.Dropdown>
                {SILENCE_OPTIONS.map((option) => (
                  <Menu.Item key={option.minutes} onClick={() => runBulk('bulk-silence', option.minutes)}>
                    {t(option.labelKey)}
                  </Menu.Item>
                ))}
              </Menu.Dropdown>
            </Menu>
            <Button
              size="xs"
              variant="subtle"
              color="gray"
              leftSection={<IconX size={14} />}
              onClick={() => setSelected([])}
              ml="auto"
            >
              {t('groups.clearSelection')}
            </Button>
          </Group>
        </Paper>
      )}

      <Paper withBorder>
        <QueryState
          query={groups}
          isEmpty={(data) => (data.items?.length ?? 0) === 0}
          emptyLabel={filtered ? t('groups.empty') : t('groups.emptyUnfiltered')}
          emptyAction={
            filtered ? (
              <Button variant="light" size="xs" onClick={() => applyView('all')}>
                {t('groups.clearFilters')}
              </Button>
            ) : (
              <Button variant="light" size="xs" component={Link} to="/integrations">
                {t('groups.addIntegration')}
              </Button>
            )
          }
        >
          {() => (
            <>
              {/* Below the navbar breakpoint the table becomes a horizontal
                  scroll of nine columns, which is not a list — it is a
                  spreadsheet on a phone at three in the morning. The card
                  carries the same five facts a responder triages by. */}
              {compact ? (
              <Stack gap={0}>
                {items.map((group) => (
                  <MobileCard
                    key={group.id}
                    group={group}
                    integrationName={
                      group.integration_id ? integrationName.get(group.integration_id) : undefined
                    }
                    selected={selected.includes(group.id)}
                    arrived={arrived.has(group.id)}
                    onToggle={() => toggleOne(group.id)}
                  />
                ))}
              </Stack>
              ) : (
              <Table.ScrollContainer minWidth={1100}>
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
                    <Table.Th w={120}>{t('common.severity')}</Table.Th>
                    <Table.Th w={180}>{t('common.integration')}</Table.Th>
                    <Table.Th w={80}>{t('groups.alerts')}</Table.Th>
                    <Table.Th w={150}>{t('groups.age')}</Table.Th>
                    <Table.Th w={140}>{t('groups.lastAlert')}</Table.Th>
                    <Table.Th w={60} />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {items.map((group, index) => (
                    <Row
                      key={group.id}
                      rowRef={(node) => {
                        rowRefs.current[index] = node;
                      }}
                      group={group}
                      integrationName={
                        group.integration_id ? integrationName.get(group.integration_id) : undefined
                      }
                      selected={selected.includes(group.id)}
                      arrived={arrived.has(group.id)}
                      focused={index === cursor}
                      onFocus={() => setCursor(index)}
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
            </>
          )}
        </QueryState>
      </Paper>

      {total > PAGE_SIZE && (
        <Group justify="center" mt="md">
          <Pagination
            value={page}
            onChange={(next) => patchParams({ page: String(next) }, true)}
            total={Math.ceil(total / PAGE_SIZE)}
          />
        </Group>
      )}

      <Modal opened={helpOpen} onClose={help.close} title={t('groups.shortcuts')} size="sm">
        <Stack gap="xs">
          {[
            { keys: ['j', 'k'], labelKey: 'shortcuts.move' as StringKey },
            { keys: ['x'], labelKey: 'shortcuts.select' as StringKey },
            { keys: ['a'], labelKey: 'shortcuts.ack' as StringKey },
            { keys: ['r'], labelKey: 'shortcuts.resolve' as StringKey },
            { keys: ['Enter'], labelKey: 'shortcuts.open' as StringKey },
            { keys: ['Esc'], labelKey: 'shortcuts.clear' as StringKey },
            { keys: ['⌘', 'K'], labelKey: 'shortcuts.palette' as StringKey },
            { keys: ['⌘', 'B'], labelKey: 'shortcuts.sidebar' as StringKey },
          ].map((row) => (
            <Group key={row.labelKey} justify="space-between">
              <Text size="sm">{t(row.labelKey)}</Text>
              <Group gap={4}>
                {row.keys.map((key) => (
                  <Kbd key={key}>{key}</Kbd>
                ))}
              </Group>
            </Group>
          ))}
        </Stack>
      </Modal>
    </>
  );
}

function Row({
  group,
  integrationName,
  selected,
  arrived,
  focused,
  rowRef,
  onFocus,
  onToggle,
  onAction,
  onSilence,
}: {
  group: AlertGroup;
  integrationName: string | undefined;
  selected: boolean;
  arrived: boolean;
  focused: boolean;
  rowRef: (node: HTMLTableRowElement | null) => void;
  onFocus: () => void;
  onToggle: () => void;
  onAction: (action: 'acknowledge' | 'unacknowledge' | 'resolve' | 'unresolve') => void;
  onSilence: (minutes: number) => void;
}) {
  const { t } = useI18n();
  const reduce = useReducedMotion();
  return (
    <Table.Tr
      ref={rowRef}
      onClick={onFocus}
      bg={selected ? 'var(--mantine-color-blue-light)' : undefined}
      style={{
        // The stripe is the only thing that survives a squint: severity read as
        // shape and position, before any badge is parsed. It never animates —
        // it is what the eye compares down the column.
        boxShadow: `inset 4px 0 0 0 ${severityStripe(group.severity)}`,
        outline: focused ? '2px solid var(--mantine-color-blue-filled)' : undefined,
        outlineOffset: '-2px',
        animation: arrived && !reduce ? `nxsArrive ${DURATION.arrival}ms ${EASE_SETTLE}` : undefined,
      }}
    >
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
        <IncidentAge createdAt={group.created_at} resolvedAt={group.resolved_at} />
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

/**
 * One incident as a card: severity and status first, then what it is, then how
 * long it has been going. The row actions are deliberately absent — a fat
 * finger on a phone should not resolve an incident by accident; the card opens
 * the group, where the buttons are labelled.
 */
function MobileCard({
  group,
  integrationName,
  selected,
  arrived,
  onToggle,
}: {
  group: AlertGroup;
  integrationName: string | undefined;
  selected: boolean;
  arrived: boolean;
  onToggle: () => void;
}) {
  const { t } = useI18n();
  const reduce = useReducedMotion();
  return (
    <Paper
      withBorder={false}
      p="sm"
      style={{
        borderBottom: '1px solid var(--mantine-color-default-border)',
        boxShadow: `inset 4px 0 0 0 ${severityStripe(group.severity)}`,
        background: selected ? 'var(--mantine-color-blue-light)' : undefined,
        animation: arrived && !reduce ? `nxsArrive ${DURATION.arrival}ms ${EASE_SETTLE}` : undefined,
      }}
    >
      <Group align="flex-start" wrap="nowrap" gap="sm">
        <Checkbox
          checked={selected}
          onChange={onToggle}
          aria-label={t('common.select', { name: group.id })}
          mt={4}
        />
        <Stack gap={6} style={{ flex: 1, minWidth: 0 }}>
          <Group gap="xs">
            <SeverityBadge severity={group.severity} />
            <StatusBadge status={group.status} />
          </Group>
          <Text component={Link} to={`/alert-groups/${group.id}`} fw={500} size="sm">
            {group.title || group.dedupe_key || group.id}
          </Text>
          <Group gap="xs" wrap="wrap">
            <IncidentAge createdAt={group.created_at} resolvedAt={group.resolved_at} />
            {integrationName && (
              <Text size="xs" c="dimmed">
                · {integrationName}
              </Text>
            )}
          </Group>
          <Labels labels={group.labels} />
        </Stack>
      </Group>
    </Paper>
  );
}

/**
 * Ids that appeared since the previous render of the list.
 *
 * The first load is not an arrival: everything is new then, and a list that
 * lights up entirely on open teaches people to ignore the highlight.
 */
function useArrivals(ids: string[]): Set<string> {
  const seen = useRef<Set<string> | null>(null);
  const [arrived, setArrived] = useState<Set<string>>(new Set());
  const key = ids.join(',');

  useEffect(() => {
    const current = new Set(ids);
    if (seen.current === null) {
      seen.current = current;
      return;
    }
    const fresh = new Set<string>();
    for (const id of current) if (!seen.current.has(id)) fresh.add(id);
    seen.current = current;
    if (fresh.size === 0) return;
    setArrived(fresh);
    const timer = setTimeout(() => setArrived(new Set()), DURATION.arrival);
    return () => clearTimeout(timer);
    // ids is rebuilt every render; the joined key is what actually changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  return arrived;
}

/** Exported so other pages can reuse the same silence presets. */
export { SILENCE_OPTIONS };
