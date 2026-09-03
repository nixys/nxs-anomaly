import { useEffect, useMemo, useState } from 'react';
import {
  Alert,
  Badge,
  Button,
  Code,
  Group,
  Pagination,
  Paper,
  Stack,
  Table,
  Text,
  TextInput,
} from '@mantine/core';
import { IconSearch, IconX } from '@tabler/icons-react';
import { api } from '../api/client';
import { AbsoluteTime, PageHeader } from '../components/common';
import { useI18n } from '../i18n/I18nProvider';
import { describeError } from '../i18n/errors';
import { EMPTY_VALUE } from '../i18n/format';

/** One row of the append-only trail, as the API returns it. */
interface AuditEvent {
  id: string;
  occurred_at: string;
  actor_id: string;
  actor_kind: 'user' | 'service' | 'system';
  actor_name: string;
  actor_role: string;
  action: string;
  entity_type: string;
  entity_id: string;
  request_ip: string;
  request_id: string;
  data: Record<string, unknown>;
}

const PAGE_SIZE = 50;

/** Filters the API supports. Kept in one place so the form and the query agree. */
const FILTERS = [
  { key: 'actor_id', labelKey: 'audit.actorId' },
  { key: 'entity_type', labelKey: 'audit.entityType' },
  { key: 'entity_id', labelKey: 'audit.entityId' },
  { key: 'action', labelKey: 'audit.action' },
  { key: 'request_id', labelKey: 'audit.requestId' },
] as const;

type FilterKey = (typeof FILTERS)[number]['key'];

const ACTOR_COLORS: Record<AuditEvent['actor_kind'], string> = {
  user: 'blue',
  service: 'violet',
  system: 'gray',
};

export function AuditPage() {
  const { t } = useI18n();
  // Draft and applied filters are separate so typing does not fire a request
  // per keystroke against a table that is only ever going to grow.
  const [draft, setDraft] = useState<Record<FilterKey, string>>({
    actor_id: '',
    entity_type: '',
    entity_id: '',
    action: '',
    request_id: '',
  });
  const [applied, setApplied] = useState(draft);
  const [page, setPage] = useState(1);
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const query = useMemo(() => {
    const q: Record<string, string | number> = {
      limit: PAGE_SIZE,
      offset: (page - 1) * PAGE_SIZE,
    };
    for (const [key, value] of Object.entries(applied)) {
      if (value.trim()) q[key] = value.trim();
    }
    return q;
  }, [applied, page]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    api
      .get<{ items: AuditEvent[]; total: number }>('/api/v1/audit', query)
      .then((res) => {
        if (cancelled) return;
        setEvents(res.items ?? []);
        setTotal(res.total ?? 0);
        setError(null);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(describeError(err, t));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [query, t]);

  const applyFilters = () => {
    setPage(1);
    setApplied(draft);
  };

  const clearFilters = () => {
    const empty = {
      actor_id: '',
      entity_type: '',
      entity_id: '',
      action: '',
      request_id: '',
    };
    setDraft(empty);
    setApplied(empty);
    setPage(1);
  };

  const hasFilters = Object.values(applied).some((v) => v.trim());

  return (
    <>
      <PageHeader
        title={t('audit.title')}
        description={t('audit.description')}
      />

      <Paper withBorder p="md" mb="md">
        <Group align="flex-end" gap="sm" wrap="wrap">
          {FILTERS.map((filter) => (
            <TextInput
              key={filter.key}
              label={t(filter.labelKey)}
              value={draft[filter.key]}
              onChange={(event) =>
                setDraft({ ...draft, [filter.key]: event.currentTarget.value })
              }
              onKeyDown={(event) => {
                if (event.key === 'Enter') applyFilters();
              }}
              w={180}
            />
          ))}
          <Button leftSection={<IconSearch size={16} />} onClick={applyFilters}>
            {t('audit.filter')}
          </Button>
          {hasFilters && (
            <Button variant="subtle" leftSection={<IconX size={16} />} onClick={clearFilters}>
              {t('audit.clear')}
            </Button>
          )}
        </Group>
      </Paper>

      {error && (
        <Alert color="red" mb="md">
          {error}
        </Alert>
      )}

      <Paper withBorder>
        <Table.ScrollContainer minWidth={1000}>
          <Table highlightOnHover verticalSpacing="sm">
            <Table.Thead>
              <Table.Tr>
                <Table.Th w={170}>{t('audit.when')}</Table.Th>
                <Table.Th w={200}>{t('audit.actor')}</Table.Th>
                <Table.Th w={190}>{t('audit.action')}</Table.Th>
                <Table.Th w={230}>{t('audit.entity')}</Table.Th>
                <Table.Th>{t('common.details')}</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {events.map((event) => (
                <Table.Tr key={event.id}>
                  <Table.Td>
                    <AbsoluteTime value={event.occurred_at} />
                  </Table.Td>
                  <Table.Td>
                    <Stack gap={2}>
                      <Group gap={6} wrap="nowrap">
                        <Badge size="sm" variant="light" color={ACTOR_COLORS[event.actor_kind]}>
                          {event.actor_kind}
                        </Badge>
                        <Text size="sm">{event.actor_name || EMPTY_VALUE}</Text>
                      </Group>
                      <Text size="xs" c="dimmed">
                        {event.actor_role || t('audit.noRole')}
                        {event.request_ip ? ` · ${event.request_ip}` : ''}
                      </Text>
                    </Stack>
                  </Table.Td>
                  <Table.Td>
                    <Code>{event.action}</Code>
                  </Table.Td>
                  <Table.Td>
                    <Stack gap={0}>
                      <Text size="sm">{event.entity_type || EMPTY_VALUE}</Text>
                      <Text size="xs" c="dimmed" ff="monospace">
                        {event.entity_id || EMPTY_VALUE}
                      </Text>
                    </Stack>
                  </Table.Td>
                  <Table.Td>
                    <Stack gap={2}>
                      {/* Field names, never values: the trail deliberately does
                          not copy secrets out of configuration payloads. */}
                      <Text size="xs" style={{ wordBreak: 'break-word' }}>
                        {summarise(event.data)}
                      </Text>
                      {event.request_id && (
                        <Text size="xs" c="dimmed" ff="monospace">
                          {t('audit.requestPrefix')} {event.request_id}
                        </Text>
                      )}
                    </Stack>
                  </Table.Td>
                </Table.Tr>
              ))}
              {!loading && events.length === 0 && (
                <Table.Tr>
                  <Table.Td colSpan={5}>
                    <Text c="dimmed" ta="center" py="lg">
                      {hasFilters ? t('audit.noMatch') : t('audit.empty')}
                    </Text>
                  </Table.Td>
                </Table.Tr>
              )}
            </Table.Tbody>
          </Table>
        </Table.ScrollContainer>
      </Paper>

      {total > PAGE_SIZE && (
        <Group justify="space-between" mt="md">
          <Text size="sm" c="dimmed">
            {t('audit.total', { count: total })}
          </Text>
          <Pagination value={page} onChange={setPage} total={Math.ceil(total / PAGE_SIZE)} />
        </Group>
      )}
    </>
  );
}

/**
 * Renders the event payload compactly. `fields` is the common shape — the list
 * of field names a mutation touched — and gets special treatment because it is
 * what most rows carry.
 */
function summarise(data: Record<string, unknown>): string {
  if (!data || Object.keys(data).length === 0) return '—';
  const parts: string[] = [];
  for (const [key, value] of Object.entries(data)) {
    if (key === 'fields' && Array.isArray(value)) {
      parts.push(value.join(', '));
    } else {
      parts.push(`${key}: ${String(value)}`);
    }
  }
  return parts.join(' · ');
}
