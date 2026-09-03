import {
  Anchor,
  Badge,
  Button,
  Group,
  Paper,
  SimpleGrid,
  Stack,
  Table,
  Text,
  Title,
} from '@mantine/core';
import { IconArrowRight, IconUserCheck } from '@tabler/icons-react';
import { Link } from 'react-router-dom';
import { useAllOf, useList, useOnCall } from '../api/hooks';
import {
  Labels,
  PageHeader,
  QueryState,
  RelativeTime,
  SeverityBadge,
  StatusBadge,
} from '../components/common';
import { StatTile } from '../components/StatTile';
import { useI18n } from '../i18n/I18nProvider';
import { EMPTY_VALUE } from '../i18n/format';

export function HomePage() {
  const { t } = useI18n();
  const open = useList('alert-groups', { status: 'open', limit: 10 }, { refetchInterval: 15_000 });
  const acknowledged = useList('alert-groups', { status: 'acknowledged', limit: 1 });
  const failed = useList('notifications', { status: 'failed', limit: 1 });
  const retrying = useList('notifications', { status: 'retry_scheduled', limit: 1 });
  const integrations = useAllOf('integrations');
  // Who is actually on call now, resolved through Schedule v2 (rotation /
  // overrides / restriction window) — not the manual on_duty flag, which can say
  // "on duty" while the schedule has handed off to someone else.
  const onCall = useOnCall({ refetchInterval: 30_000 });

  const onCallEntries = onCall.data?.items ?? [];
  const integrationName = new Map((integrations.data ?? []).map((i) => [i.id, i.name]));

  return (
    <>
      <PageHeader
        title={t('home.title')}
        description={t('home.description')}
        actions={
          <Button
            component={Link}
            to="/alert-groups"
            rightSection={<IconArrowRight size={16} />}
            variant="light"
          >
            {t('home.allGroups')}
          </Button>
        }
      />

      <SimpleGrid cols={{ base: 2, sm: 4 }} mb="lg">
        <StatTile
          label={t('home.openTile')}
          value={open.data?.total}
          color="red"
          loading={open.isPending}
          hint={t('home.openHint')}
        />
        <StatTile
          label={t('home.acknowledgedTile')}
          value={acknowledged.data?.total}
          color="yellow"
          loading={acknowledged.isPending}
        />
        <StatTile
          label={t('home.retryingTile')}
          value={retrying.data?.total}
          color="orange"
          loading={retrying.isPending}
          hint={t('home.retryingHint')}
        />
        <StatTile
          label={t('home.failedTile')}
          value={failed.data?.total}
          color="red"
          loading={failed.isPending}
          hint={t('home.failedHint')}
        />
      </SimpleGrid>

      <SimpleGrid cols={{ base: 1, lg: 3 }} spacing="lg">
        <Paper withBorder p="lg" style={{ gridColumn: 'span 2' }}>
          <Group justify="space-between" mb="md">
            <Title order={5}>{t('home.openGroups')}</Title>
            <Anchor component={Link} to="/alert-groups" size="sm">
              {t('common.viewAll')}
            </Anchor>
          </Group>
          <QueryState
            query={open}
            isEmpty={(data) => data.items.length === 0}
            emptyLabel={t('home.nothingFiring')}
          >
            {(data) => (
              <Table.ScrollContainer minWidth={600}>
                <Table highlightOnHover verticalSpacing="sm">
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th>{t('common.title')}</Table.Th>
                      <Table.Th w={110}>{t('common.severity')}</Table.Th>
                      <Table.Th w={150}>{t('common.integration')}</Table.Th>
                      <Table.Th w={130}>{t('home.lastAlert')}</Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {data.items.map((group) => (
                      <Table.Tr key={group.id}>
                        <Table.Td>
                          <Stack gap={4}>
                            <Anchor
                              component={Link}
                              to={`/alert-groups/${group.id}`}
                              size="sm"
                              fw={500}
                            >
                              {group.title || group.dedupe_key || group.id}
                            </Anchor>
                            <Labels labels={group.labels} />
                          </Stack>
                        </Table.Td>
                        <Table.Td>
                          <SeverityBadge severity={group.severity} />
                        </Table.Td>
                        <Table.Td>
                          <Text size="sm">
                            {/* A directly-paged group belongs to no integration. */}
                            {group.integration_id
                              ? (integrationName.get(group.integration_id) ?? group.integration_id)
                              : EMPTY_VALUE}
                          </Text>
                        </Table.Td>
                        <Table.Td>
                          <RelativeTime value={group.last_received_at ?? group.created_at} />
                        </Table.Td>
                      </Table.Tr>
                    ))}
                  </Table.Tbody>
                </Table>
              </Table.ScrollContainer>
            )}
          </QueryState>
        </Paper>

        <Stack gap="lg">
          <Paper withBorder p="lg">
            <Group justify="space-between" mb="md">
              <Title order={5}>{t('home.onCallNow')}</Title>
              <Anchor component={Link} to="/schedules" size="sm">
                {t('nav.schedules')}
              </Anchor>
            </Group>
            {onCall.isPending ? (
              <Text size="sm" c="dimmed">
                {t('common.resolving')}
              </Text>
            ) : onCallEntries.length === 0 ? (
              <Text size="sm" c="dimmed">
                {t('home.nobodyOnCall')}
              </Text>
            ) : (
              <Group gap={6}>
                {onCallEntries.map((entry) => (
                  <Badge
                    key={`${entry.schedule_id}:${entry.user_id}`}
                    variant="light"
                    color={entry.exists ? undefined : 'red'}
                    leftSection={<IconUserCheck size={12} />}
                    title={t('home.onCallTooltip', {
                      schedule: entry.schedule_name,
                      source: entry.source,
                    })}
                  >
                    {entry.exists ? entry.name : t('home.onCallRemoved', { name: entry.name })}
                  </Badge>
                ))}
              </Group>
            )}
          </Paper>

          <Paper withBorder p="lg">
            <Group justify="space-between" mb="md">
              <Title order={5}>{t('nav.integrations')}</Title>
              <Anchor component={Link} to="/integrations" size="sm">
                {t('common.manage')}
              </Anchor>
            </Group>
            {(integrations.data ?? []).length === 0 ? (
              <Text size="sm" c="dimmed">
                {t('home.noIntegrations')}
              </Text>
            ) : (
              <Stack gap={6}>
                {(integrations.data ?? []).slice(0, 8).map((integration) => (
                  <Group key={integration.id} justify="space-between">
                    <Anchor component={Link} to={`/integrations/${integration.id}`} size="sm">
                      {integration.name}
                    </Anchor>
                    <StatusBadge status={integration.type} />
                  </Group>
                ))}
              </Stack>
            )}
          </Paper>
        </Stack>
      </SimpleGrid>
    </>
  );
}
