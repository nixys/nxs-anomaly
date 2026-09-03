import { useEffect, useState } from 'react';
import {
  ActionIcon,
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  Group,
  MultiSelect,
  NumberInput,
  Paper,
  Progress,
  Select,
  SimpleGrid,
  Stack,
  Switch,
  Table,
  Text,
  TextInput,
  Title,
} from '@mantine/core';
import {
  IconAlertTriangle,
  IconArrowLeft,
  IconDeviceFloppy,
  IconPlus,
  IconTrash,
  IconUserCheck,
} from '@tabler/icons-react';
import { Link, useParams } from 'react-router-dom';
import {
  useAllOf,
  useCreateOverride,
  useDeleteOverride,
  useItem,
  useSchedulePreview,
  useScheduleOnCall,
  useUpdate,
  useUpdateOverride,
} from '../api/hooks';
import {
  HANDOFF_UNITS,
  RESTRICTION_DAYS,
  SHIFT_RECURRENCES,
  type HandoffUnit,
  type Rotation,
  type ScheduleOverride,
  type ScheduleSegment,
  type Shift,
  type ShiftRecurrence,
} from '../api/types';
import { PageHeader, ProvisionedNotice, QueryState } from '../components/common';
import { formatInZone, toIso, toLocalInput } from './schedule-utils';
import { useI18n } from '../i18n/I18nProvider';
import { EMPTY_VALUE } from '../i18n/format';

const emptyRotation: Rotation = {
  enabled: true,
  start_at: '',
  handoff_interval: 1,
  handoff_unit: 'weeks',
  participant_ids: [],
  restriction: null,
};

export function ScheduleDetailPage() {
  const { t, fmt, locale } = useI18n();
  const { id } = useParams<{ id: string }>();
  const schedule = useItem('schedules', id);
  const users = useAllOf('users');
  const update = useUpdate('schedules');
  const createOverride = useCreateOverride();
  const updateOverride = useUpdateOverride();
  const deleteOverride = useDeleteOverride();
  const onCall = useScheduleOnCall(id);
  const preview = useSchedulePreview(id);

  const [shifts, setShifts] = useState<Shift[]>([]);
  const [rotation, setRotation] = useState<Rotation>(emptyRotation);
  const [restricted, setRestricted] = useState(false);
  const [notifyOnShiftChange, setNotifyOnShiftChange] = useState(false);
  useEffect(() => {
    setShifts(schedule.data?.shifts ?? []);
    setRotation(schedule.data?.rotation ?? emptyRotation);
    setRestricted(Boolean(schedule.data?.rotation?.restriction));
    setNotifyOnShiftChange(Boolean(schedule.data?.notify_on_shift_change));
  }, [schedule.data]);

  const [override, setOverride] = useState({ user_id: '', start_at: '', until: '', reason: '' });

  const userOptions = (users.data ?? []).map((user) => ({ value: user.id, label: user.name }));
  const userName = new Map((users.data ?? []).map((user) => [user.id, user.name]));
  const names = (ids: string[]) => ids.map((uid) => userName.get(uid) ?? uid).join(', ');

  const patch = (index: number, changes: Partial<Shift>) =>
    setShifts(shifts.map((shift, i) => (i === index ? { ...shift, ...changes } : shift)));

  const patchRotation = (changes: Partial<Rotation>) => setRotation({ ...rotation, ...changes });

  const saveShifts = () =>
    id &&
    update.mutate({
      id,
      body: {
        shifts: shifts.map((shift) => ({
          ...shift,
          start_at: toIso(shift.start_at),
          end_at: toIso(shift.end_at),
        })),
      },
    });

  const saveRotation = () =>
    id &&
    update.mutate({
      id,
      body: {
        rotation: {
          ...rotation,
          start_at: toIso(rotation.start_at),
          restriction: restricted ? rotation.restriction : null,
        },
      },
    });

  const restriction = rotation.restriction ?? { start: '09:00', end: '18:00', days: [] };
  const hasRotation = (schedule.data?.rotation?.participant_ids ?? []).length > 0;

  return (
    <>
      <PageHeader
        title={schedule.data?.name ?? t('schedule.title')}
        description={id}
        actions={
          <Button
            component={Link}
            to="/schedules"
            variant="default"
            leftSection={<IconArrowLeft size={16} />}
          >
            {t('common.back')}
          </Button>
        }
      />

      <QueryState query={schedule}>
        {(data) => (
          <Stack gap="md">
            {/* The overrides below stay editable: covering a shift tonight is
                what the rota is for, and Terraform does not describe it. */}
            <ProvisionedNotice by={data.provisioned_by} />
            {(preview.data?.unknown_users ?? []).length > 0 && (
              <Alert color="red" icon={<IconAlertTriangle size={16} />} title={t('schedule.unknownParticipants')}>
                <Text size="sm">
                  {t('schedule.unknownParticipantsBody', { users: (preview.data?.unknown_users ?? []).join(', ') })}
                </Text>
              </Alert>
            )}
            {(preview.data?.warnings ?? []).length > 0 && (
              <Alert
                color="orange"
                icon={<IconAlertTriangle size={16} />}
                title={t('schedule.coverageWarnings')}
              >
                <Stack gap={2}>
                  {(preview.data?.warnings ?? []).map((warning) => (
                    <Text size="sm" key={warning}>
                      {warning}
                    </Text>
                  ))}
                  <Text size="sm" c="dimmed">
                    {t('schedule.coverageRule')}
                  </Text>
                </Stack>
              </Alert>
            )}

            <Paper withBorder p="lg">
              <Group gap="xl" align="flex-start">
                <div>
                  <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
                    {t('common.timezone')}
                  </Text>
                  <Text size="sm">{data.timezone}</Text>
                </div>
                <div>
                  <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
                    {t('schedule.onCallNow')}
                  </Text>
                  <Group gap={6} mt={4}>
                    {onCall.isPending && <Text size="sm">…</Text>}
                    {(onCall.data?.user_ids ?? []).length === 0 && !onCall.isPending && (
                      <Text size="sm" c="dimmed">
                        {t('schedule.nobody')}
                      </Text>
                    )}
                    {(onCall.data?.user_ids ?? []).map((userId) => (
                      <Badge key={userId} leftSection={<IconUserCheck size={12} />} variant="light">
                        {userName.get(userId) ?? userId}
                      </Badge>
                    ))}
                    {onCall.data?.source && (
                      <Badge variant="outline" color="gray">
                        {t('schedule.via', { source: onCall.data.source })}
                      </Badge>
                    )}
                  </Group>
                </div>
                <div>
                  <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
                    {t('schedule.next')}
                  </Text>
                  {onCall.data?.next ? (
                    <Group gap={6} mt={4}>
                      <Text size="sm">
                        {onCall.data.next.user_ids.length
                          ? names(onCall.data.next.user_ids)
                          : t('schedule.nobody')}
                      </Text>
                      <Text size="sm" c="dimmed">
                        {t('schedule.fromTime', { time: formatInZone(onCall.data.next.start, data.timezone, locale) })}
                      </Text>
                    </Group>
                  ) : (
                    <Text size="sm" c="dimmed" mt={4}>
                      {t('schedule.noChange')}
                    </Text>
                  )}
                </div>
                <div>
                  <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
                    {t('schedule.coverage4w')}
                  </Text>
                  <Group gap="xs" mt={6} w={180}>
                    <Progress
                      value={(preview.data?.coverage_ratio ?? 0) * 100}
                      color={(preview.data?.coverage_ratio ?? 0) > 0.999 ? 'teal' : 'orange'}
                      style={{ flex: 1 }}
                    />
                    <Text size="sm">{Math.round((preview.data?.coverage_ratio ?? 0) * 100)}%</Text>
                  </Group>
                </div>
              </Group>
            </Paper>

            <Paper withBorder p="lg">
              <Group justify="space-between" mb="md">
                <Title order={5}>{t('schedule.rotation')}</Title>
                <Group>
                  <Switch
                    label={t('schedule.notifyHandoff')}
                    checked={notifyOnShiftChange}
                    disabled={Boolean(data.provisioned_by)}
                    onChange={(event) => {
                      const on = event.currentTarget.checked;
                      setNotifyOnShiftChange(on);
                      if (id) update.mutate({ id, body: { notify_on_shift_change: on } });
                    }}
                  />
                  <Switch
                    label={t('common.enabled')}
                    checked={rotation.enabled}
                    disabled={Boolean(data.provisioned_by)}
                    onChange={(event) => patchRotation({ enabled: event.currentTarget.checked })}
                  />
                  <Button
                    leftSection={<IconDeviceFloppy size={16} />}
                    loading={update.isPending}
                    disabled={
                      rotation.participant_ids.length === 0 ||
                      !rotation.start_at ||
                      Boolean(data.provisioned_by)
                    }
                    onClick={saveRotation}
                  >
                    {t('schedule.saveRotation')}
                  </Button>
                </Group>
              </Group>

              <SimpleGrid cols={{ base: 1, md: 4 }} spacing="sm">
                <MultiSelect
                  label={t('schedule.participants')}
                  searchable
                  data={userOptions}
                  value={rotation.participant_ids}
                  onChange={(value) => patchRotation({ participant_ids: value })}
                />
                <TextInput
                  label={t('schedule.rotationStart')}
                  type="datetime-local"
                  value={toLocalInput(rotation.start_at)}
                  onChange={(event) => patchRotation({ start_at: event.currentTarget.value })}
                />
                <NumberInput
                  label={t('schedule.handoffEvery')}
                  min={1}
                  value={rotation.handoff_interval}
                  onChange={(value) => patchRotation({ handoff_interval: Number(value) || 1 })}
                />
                <Select
                  label={t('schedule.unit')}
                  data={HANDOFF_UNITS}
                  value={rotation.handoff_unit}
                  allowDeselect={false}
                  onChange={(value) =>
                    patchRotation({ handoff_unit: (value ?? 'weeks') as HandoffUnit })
                  }
                />
              </SimpleGrid>

              <Checkbox
                mt="md"
                label={t('schedule.restrictWindow')}
                checked={restricted}
                onChange={(event) => {
                  const on = event.currentTarget.checked;
                  setRestricted(on);
                  patchRotation({ restriction: on ? restriction : null });
                }}
              />
              {restricted && (
                <SimpleGrid cols={{ base: 1, md: 3 }} spacing="sm" mt="sm">
                  <TextInput
                    label={t('maintenance.from')}
                    type="time"
                    value={restriction.start}
                    onChange={(event) =>
                      patchRotation({
                        restriction: { ...restriction, start: event.currentTarget.value },
                      })
                    }
                  />
                  <TextInput
                    label={t('schedule.to')}
                    type="time"
                    value={restriction.end}
                    onChange={(event) =>
                      patchRotation({
                        restriction: { ...restriction, end: event.currentTarget.value },
                      })
                    }
                  />
                  <MultiSelect
                    label={t('schedule.days')}
                    data={[...RESTRICTION_DAYS]}
                    value={restriction.days}
                    onChange={(value) =>
                      patchRotation({ restriction: { ...restriction, days: value } })
                    }
                  />
                </SimpleGrid>
              )}
              <Text size="sm" c="dimmed" mt="sm">
                {t('schedule.dstHelp')}
              </Text>
            </Paper>

            <Paper withBorder p="lg">
              <Group justify="space-between" mb="md">
                <Title order={5}>{t('schedule.next4w')}</Title>
                <Text size="xs" c="dimmed">
                  {t('schedule.timesIn', { timezone: schedule.data?.timezone ?? 'UTC' })}
                </Text>
              </Group>
              <QueryState query={preview}>
                {(data) => (
                  <Table>
                    <Table.Thead>
                      <Table.Tr>
                        <Table.Th w={220}>{t('maintenance.from')}</Table.Th>
                        <Table.Th w={220}>{t('maintenance.until')}</Table.Th>
                        <Table.Th w={80}>{t('schedule.length')}</Table.Th>
                        <Table.Th>{t('schedule.onCall')}</Table.Th>
                        <Table.Th w={110}>{t('common.source')}</Table.Th>
                      </Table.Tr>
                    </Table.Thead>
                    <Table.Tbody>
                      {data.segments.map((segment: ScheduleSegment) => (
                        <Table.Tr key={`${segment.start}-${segment.end}`}>
                          <Table.Td>
                            <Text size="sm" style={{ whiteSpace: 'nowrap' }}>
                              {formatInZone(segment.start, data.timezone, locale)}
                            </Text>
                          </Table.Td>
                          <Table.Td>
                            <Text size="sm" style={{ whiteSpace: 'nowrap' }}>
                              {formatInZone(segment.end, data.timezone, locale)}
                            </Text>
                          </Table.Td>
                          <Table.Td>{fmt.duration(segment.duration_seconds * 1000)}</Table.Td>
                          <Table.Td>
                            {segment.user_ids.length === 0 ? (
                              <Badge color="orange" variant="light">
                                {t('schedule.gap')}
                              </Badge>
                            ) : (
                              <Text size="sm">{names(segment.user_ids)}</Text>
                            )}
                          </Table.Td>
                          <Table.Td>
                            <Text size="sm" c="dimmed">
                              {segment.source || EMPTY_VALUE}
                            </Text>
                          </Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                )}
              </QueryState>
            </Paper>

            <Paper withBorder p="lg">
              <Group justify="space-between" mb="md">
                <Title order={5}>{t('schedule.legacyShifts')}</Title>
                <Group>
                  <Button
                    variant="light"
                    leftSection={<IconPlus size={16} />}
                    onClick={() =>
                      setShifts([
                        ...shifts,
                        {
                          id: '',
                          user_id: userOptions[0]?.value ?? '',
                          start_at: '',
                          end_at: '',
                          recurrence: 'weekly',
                        },
                      ])
                    }
                  >
                    {t('schedule.addShift')}
                  </Button>
                  <Button
                    leftSection={<IconDeviceFloppy size={16} />}
                    loading={update.isPending}
                    disabled={Boolean(data.provisioned_by)}
                    onClick={saveShifts}
                  >
                    {t('schedule.saveShifts')}
                  </Button>
                </Group>
              </Group>

              {hasRotation && (
                <Alert variant="light" color="gray" mb="md">
                  {t('schedule.rotationIgnoresShifts')}
                </Alert>
              )}

              {shifts.length === 0 && (
                <Text size="sm" c="dimmed">
                  {t('schedule.noShifts')}
                </Text>
              )}

              <Stack gap="sm">
                {shifts.map((shift, index) => (
                  <Card withBorder key={index} p="md">
                    <SimpleGrid cols={{ base: 1, md: 4 }} spacing="sm">
                      <Select
                        label={t('common.user')}
                        searchable
                        data={userOptions}
                        value={shift.user_id}
                        onChange={(value) => patch(index, { user_id: value ?? '' })}
                      />
                      <TextInput
                        label={t('schedule.start')}
                        type="datetime-local"
                        value={toLocalInput(shift.start_at)}
                        onChange={(event) => patch(index, { start_at: event.currentTarget.value })}
                      />
                      <TextInput
                        label={t('schedule.end')}
                        type="datetime-local"
                        value={toLocalInput(shift.end_at)}
                        onChange={(event) => patch(index, { end_at: event.currentTarget.value })}
                      />
                      <Group align="flex-end" wrap="nowrap">
                        <Select
                          label={t('schedule.recurrence')}
                          data={SHIFT_RECURRENCES}
                          value={shift.recurrence}
                          onChange={(value) =>
                            patch(index, { recurrence: (value ?? 'none') as ShiftRecurrence })
                          }
                          allowDeselect={false}
                          style={{ flex: 1 }}
                        />
                        <ActionIcon
                          color="red"
                          variant="subtle"
                          mb={4}
                          onClick={() => setShifts(shifts.filter((_, i) => i !== index))}
                          aria-label={t('schedule.removeShift')}
                        >
                          <IconTrash size={16} />
                        </ActionIcon>
                      </Group>
                    </SimpleGrid>
                  </Card>
                ))}
              </Stack>
            </Paper>

            <Paper withBorder p="lg">
              <Title order={5} mb="md">
                {t('schedule.overrides')}
              </Title>

              {(data.overrides ?? []).length === 0 ? (
                <Text size="sm" c="dimmed" mb="md">
                  {t('schedule.noOverrides')}
                </Text>
              ) : (
                <Table mb="md">
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th>{t('common.user')}</Table.Th>
                      <Table.Th w={200}>{t('maintenance.from')}</Table.Th>
                      <Table.Th w={200}>{t('maintenance.until')}</Table.Th>
                      <Table.Th>{t('common.reason')}</Table.Th>
                      <Table.Th w={160}>{t('schedule.createdBy')}</Table.Th>
                      <Table.Th w={60} />
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {(data.overrides ?? []).map((item: ScheduleOverride) => (
                      <OverrideRow
                        key={item.id}
                        scheduleId={id ?? ''}
                        override={item}
                        userOptions={userOptions}
                        onSave={(body) =>
                          id &&
                          updateOverride.mutate({ id, overrideId: item.id, body })
                        }
                        onDelete={() =>
                          id && deleteOverride.mutate({ id, overrideId: item.id })
                        }
                        saving={updateOverride.isPending}
                      />
                    ))}
                  </Table.Tbody>
                </Table>
              )}

              <Alert variant="light" mb="md">
                {t('schedule.overrideHelp')}
              </Alert>

              <Group align="flex-end" wrap="wrap">
                <Select
                  label={t('common.user')}
                  searchable
                  data={userOptions}
                  value={override.user_id || null}
                  onChange={(value) => setOverride({ ...override, user_id: value ?? '' })}
                  w={200}
                />
                <TextInput
                  label={t('schedule.optionalStart')}
                  type="datetime-local"
                  value={override.start_at}
                  onChange={(event) =>
                    setOverride({ ...override, start_at: event.currentTarget.value })
                  }
                />
                <TextInput
                  label={t('maintenance.until')}
                  type="datetime-local"
                  value={override.until}
                  onChange={(event) =>
                    setOverride({ ...override, until: event.currentTarget.value })
                  }
                />
                <TextInput
                  label={t('common.reason')}
                  placeholder={t('schedule.overrideReason')}
                  value={override.reason}
                  onChange={(event) =>
                    setOverride({ ...override, reason: event.currentTarget.value })
                  }
                  w={240}
                />
                <Button
                  loading={createOverride.isPending}
                  disabled={!id || !override.user_id || !override.until}
                  onClick={() =>
                    id &&
                    createOverride.mutate(
                      {
                        id,
                        body: {
                          user_id: override.user_id,
                          until: toIso(override.until),
                          reason: override.reason,
                          ...(override.start_at ? { start_at: toIso(override.start_at) } : {}),
                        },
                      },
                      {
                        onSuccess: () =>
                          setOverride({ user_id: '', start_at: '', until: '', reason: '' }),
                      },
                    )
                  }
                >
                  {t('schedule.addOverride')}
                </Button>
              </Group>
            </Paper>
          </Stack>
        )}
      </QueryState>
    </>
  );
}

/**
 * One editable override row. Edits are local until saved, so extending an
 * override does not fire a request per keystroke.
 */
function OverrideRow({
  override,
  userOptions,
  onSave,
  onDelete,
  saving,
}: {
  scheduleId: string;
  override: ScheduleOverride;
  userOptions: { value: string; label: string }[];
  onSave: (body: Record<string, unknown>) => void;
  onDelete: () => void;
  saving: boolean;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState({
    user_id: override.user_id,
    start_at: toLocalInput(override.start_at),
    until: toLocalInput(override.until),
    reason: override.reason ?? '',
  });
  const dirty =
    draft.user_id !== override.user_id ||
    draft.start_at !== toLocalInput(override.start_at) ||
    draft.until !== toLocalInput(override.until) ||
    draft.reason !== (override.reason ?? '');

  return (
    <Table.Tr>
      <Table.Td>
        <Select
          searchable
          data={userOptions}
          value={draft.user_id}
          onChange={(value) => setDraft({ ...draft, user_id: value ?? '' })}
        />
      </Table.Td>
      <Table.Td>
        <TextInput
          type="datetime-local"
          value={draft.start_at}
          onChange={(event) => setDraft({ ...draft, start_at: event.currentTarget.value })}
        />
      </Table.Td>
      <Table.Td>
        <TextInput
          type="datetime-local"
          value={draft.until}
          onChange={(event) => setDraft({ ...draft, until: event.currentTarget.value })}
        />
      </Table.Td>
      <Table.Td>
        <TextInput
          value={draft.reason}
          placeholder={EMPTY_VALUE}
          onChange={(event) => setDraft({ ...draft, reason: event.currentTarget.value })}
        />
      </Table.Td>
      <Table.Td>
        <Text size="sm" c="dimmed">
          {override.created_by?.name ?? EMPTY_VALUE}
        </Text>
      </Table.Td>
      <Table.Td>
        <Group gap={4} wrap="nowrap">
          <ActionIcon
            variant="subtle"
            aria-label={t('schedule.saveOverride')}
            disabled={!dirty}
            loading={saving && dirty}
            onClick={() =>
              onSave({
                user_id: draft.user_id,
                start_at: toIso(draft.start_at),
                until: toIso(draft.until),
                reason: draft.reason,
              })
            }
          >
            <IconDeviceFloppy size={16} />
          </ActionIcon>
          <ActionIcon color="red" variant="subtle" aria-label={t('schedule.deleteOverride')} onClick={onDelete}>
            <IconTrash size={16} />
          </ActionIcon>
        </Group>
      </Table.Td>
    </Table.Tr>
  );
}
