import { useState } from 'react';
import {
  ActionIcon,
  Badge,
  Button,
  Group,
  Modal,
  Paper,
  Select,
  Stack,
  Table,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core';
import { IconCalendarPlus, IconSettings } from '@tabler/icons-react';
import { Link } from 'react-router-dom';
import { useAllOf, useCreate, useDelete, useList, useScheduleCoverage } from '../api/hooks';
import { ConfirmDeleteButton, PageHeader, ProvisionedBadge, QueryState } from '../components/common';
import type { ScheduleCoverageItem } from '../api/types';
import { useI18n } from '../i18n/I18nProvider';
import { EMPTY_VALUE } from '../i18n/format';

export function SchedulesPage() {
  const { t } = useI18n();
  const schedules = useList('schedules', { limit: 500 });
  const teams = useAllOf('teams');
  const coverage = useScheduleCoverage();
  const remove = useDelete('schedules');
  const [creating, setCreating] = useState(false);

  const teamName = new Map((teams.data ?? []).map((team) => [team.id, team.name]));
  // Same check the worker runs and alerts on, so the list and the metric agree.
  const degraded = new Map(
    (coverage.data?.items ?? []).map((item) => [item.schedule_id, item]),
  );

  return (
    <>
      <PageHeader
        title={t('schedules.title')}
        description={t('schedules.description')}
        actions={
          <Button leftSection={<IconCalendarPlus size={16} />} onClick={() => setCreating(true)}>
            {t('schedules.add')}
          </Button>
        }
      />

      <Paper withBorder>
        <QueryState
          query={schedules}
          isEmpty={(data) => data.items.length === 0}
          emptyLabel={t('schedules.empty')}
        >
          {(data) => (
            <Table highlightOnHover verticalSpacing="sm">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('common.name')}</Table.Th>
                  <Table.Th w={180}>{t('common.team')}</Table.Th>
                  <Table.Th w={120}>{t('common.timezone')}</Table.Th>
                  <Table.Th w={160}>{t('schedules.rotation')}</Table.Th>
                  <Table.Th w={190}>{t('schedules.coverage')}</Table.Th>
                  <Table.Th w={120}>{t('schedules.overrides')}</Table.Th>
                  <Table.Th w={90} />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.items.map((schedule) => (
                  <Table.Tr key={schedule.id}>
                    <Table.Td>
                      <Group gap={6}>
                        <Text component={Link} to={`/schedules/${schedule.id}`} size="sm" fw={500}>
                          {schedule.name}
                        </Text>
                        <ProvisionedBadge by={schedule.provisioned_by} />
                      </Group>
                    </Table.Td>
                    <Table.Td>
                      <Text size="sm">
                        {schedule.team_id ? (teamName.get(schedule.team_id) ?? schedule.team_id) : EMPTY_VALUE}
                      </Text>
                    </Table.Td>
                    <Table.Td>
                      <Text size="sm">{schedule.timezone}</Text>
                    </Table.Td>
                    <Table.Td>
                      {(schedule.rotation?.participant_ids ?? []).length > 0 ? (
                        <Badge variant="light" color={schedule.rotation?.enabled ? 'blue' : 'gray'}>
                          {t('schedules.rotationSummary', {
                            count: schedule.rotation?.participant_ids.length ?? 0,
                            interval: schedule.rotation?.handoff_interval ?? 0,
                            unit: schedule.rotation?.handoff_unit ?? '',
                          })}
                        </Badge>
                      ) : (
                        <Badge variant="light" color="gray">
                          {t('schedules.shiftCount', { count: (schedule.shifts ?? []).length })}
                        </Badge>
                      )}
                    </Table.Td>
                    <Table.Td>
                      <CoverageCell item={degraded.get(schedule.id)} />
                    </Table.Td>
                    <Table.Td>
                      <Badge variant="light" color="grape">
                        {(schedule.overrides ?? []).length}
                      </Badge>
                    </Table.Td>
                    <Table.Td>
                      <Group gap={4} wrap="nowrap">
                        <ActionIcon
                          component={Link}
                          to={`/schedules/${schedule.id}`}
                          variant="subtle"
                          aria-label={t('common.select', { name: schedule.name })}
                        >
                          <IconSettings size={16} />
                        </ActionIcon>
                        <ConfirmDeleteButton
                          label={schedule.name}
                          loading={remove.isPending}
                          disabled={Boolean(schedule.provisioned_by)}
                          disabledReason={t('common.provisionedHint', { tool: schedule.provisioned_by ?? '' })}
                          onConfirm={() => remove.mutate(schedule.id)}
                        />
                      </Group>
                    </Table.Td>
                  </Table.Tr>
                ))}
              </Table.Tbody>
            </Table>
          )}
        </QueryState>
      </Paper>

      <CreateScheduleModal opened={creating} onClose={() => setCreating(false)} />
    </>
  );
}

/**
 * Coverage state of one schedule. "Accepted" means a chain step opted into the
 * gaps with allow_uncovered — a deliberate choice, not a healthy schedule.
 */
function CoverageCell({ item }: { item: ScheduleCoverageItem | undefined }) {
  const { t } = useI18n();
  if (!item) {
    return (
      <Badge variant="light" color="teal">
        {t('schedules.covered')}
      </Badge>
    );
  }
  const reasons = [
    item.disabled ? t('schedules.disabled') : '',
    item.gap_count > 0 ? t('schedules.gaps', { count: item.gap_count }) : '',
    item.unknown_users.length > 0 ? t('schedules.unknownUsers', { count: item.unknown_users.length }) : '',
  ].filter(Boolean);
  const attached = item.attached_to.length > 0;
  return (
    <Tooltip
      label={
        attached
          ? t('schedules.usedBy', { chains: item.attached_to.map((chain) => chain.name).join(', ') })
          : t('schedules.notUsed')
      }
      withArrow
    >
      <Badge variant="light" color={attached && !item.acknowledged ? 'red' : 'orange'}>
        {reasons.join(', ')}
        {item.acknowledged ? ` (${t('schedules.accepted')})` : ''}
      </Badge>
    </Tooltip>
  );
}

function CreateScheduleModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const create = useCreate('schedules');
  const teams = useAllOf('teams');
  const [name, setName] = useState('');
  const [timezone, setTimezone] = useState('UTC');
  const [teamId, setTeamId] = useState<string | null>(null);
  const { t } = useI18n();

  return (
    <Modal opened={opened} onClose={onClose} title={t('schedules.add')} centered>
      <Stack>
        <TextInput
          label={t('common.name')}
          required
          value={name}
          onChange={(event) => setName(event.currentTarget.value)}
        />
        <TextInput
          label={t('common.timezone')}
          value={timezone}
          onChange={(event) => setTimezone(event.currentTarget.value)}
        />
        <Select
          label={t('common.team')}
          placeholder={t('common.none')}
          clearable
          searchable
          data={(teams.data ?? []).map((team) => ({ value: team.id, label: team.name }))}
          value={teamId}
          onChange={setTeamId}
        />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            loading={create.isPending}
            disabled={!name.trim()}
            onClick={() =>
              create.mutate(
                { name, timezone, team_id: teamId ?? '', shifts: [] },
                {
                  onSuccess: () => {
                    setName('');
                    onClose();
                  },
                },
              )
            }
          >
            {t('common.create')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
