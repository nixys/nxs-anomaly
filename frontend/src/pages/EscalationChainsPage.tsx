import { useEffect, useState } from 'react';
import {
  ActionIcon,
  Badge,
  Button,
  Card,
  Checkbox,
  Group,
  Modal,
  MultiSelect,
  NumberInput,
  Paper,
  Select,
  Stack,
  Switch,
  Text,
  TextInput,
  Textarea,
  Tooltip,
} from '@mantine/core';
import {
  IconAlertTriangle,
  IconArrowDown,
  IconArrowUp,
  IconDeviceFloppy,
  IconPlus,
  IconTrash,
  IconUserSearch,
} from '@tabler/icons-react';
import { useAllOf, useCreate, useDelete, useList, useOnCall, useUpdate } from '../api/hooks';
import { STEP_KINDS, type EscalationChain, type EscalationStep, type StepKind } from '../api/types';
import { ConfirmDeleteButton, PageHeader, ProvisionedBadge, QueryState } from '../components/common';
import {
  chainPagesNobody,
  stepReachesNobody,
  stepSentence,
  type ChainNames,
} from './chain-sentence';
import { useI18n } from '../i18n/I18nProvider';
import { useSubmitShortcut } from '../ui/useSubmitShortcut';
import type { Messages } from '../i18n/messages';

/**
 * Resolves the ids a chain step carries into the names a person knows.
 *
 * These three collections are small and already cached by other pages, so this
 * costs nothing beyond the first visit — and it is what turns
 * `usr_5d5c8e52efe0` into "Ada Okonkwo" on the one screen where knowing which
 * human is meant is the whole point.
 */
function useChainNames(): ChainNames {
  const users = useAllOf('users');
  const teams = useAllOf('teams');
  const schedules = useAllOf('schedules');
  const map = (items: Array<{ id: string; name?: string }> | undefined) =>
    new Map((items ?? []).map((item) => [item.id, item.name ?? item.id]));
  const userNames = map(users.data);
  const teamNames = map(teams.data);
  const scheduleNames = map(schedules.data);
  return {
    user: (id) => userNames.get(id) ?? id,
    team: (id) => teamNames.get(id) ?? id,
    schedule: (id) => scheduleNames.get(id) ?? id,
  };
}

function useStepHint() {
  const { t } = useI18n();
  return (kind: StepKind) => t(`chains.hint.${kind}` as keyof Pick<Messages, `chains.hint.${StepKind}`>);
}

export function EscalationChainsPage() {
  const { t } = useI18n();
  const stepHint = useStepHint();
  const names = useChainNames();
  const chains = useList('escalation-chains', { limit: 500 });
  const remove = useDelete('escalation-chains');
  const [selected, setSelected] = useState<EscalationChain | null>(null);
  const [creating, setCreating] = useState(false);

  return (
    <>
      <PageHeader
        title={t('chains.title')}
        description={t('chains.description')}
        actions={
          <Button leftSection={<IconPlus size={16} />} onClick={() => setCreating(true)}>
            {t('chains.add')}
          </Button>
        }
      />

      <QueryState
        query={chains}
        isEmpty={(data) => data.items.length === 0}
        emptyLabel={t('chains.empty')}
      >
        {(data) => (
          <Stack gap="md">
            {data.items.map((chain) => (
              <Paper withBorder p="lg" key={chain.id}>
                <Group justify="space-between" mb="sm">
                  <Group gap="sm">
                    <Text fw={600}>{chain.name}</Text>
                    <Badge variant="light">{t('chains.stepsCount', { count: (chain.steps ?? []).length })}</Badge>
                    <ProvisionedBadge by={chain.provisioned_by} />
                  </Group>
                  <Group gap="xs">
                    <WhoNowButton chain={chain} />
                    <Button
                      size="compact-sm"
                      variant="light"
                      disabled={Boolean(chain.provisioned_by)}
                      onClick={() => setSelected(chain)}
                    >
                      {t('chains.editSteps')}
                    </Button>
                    <ConfirmDeleteButton
                      label={chain.name}
                      loading={remove.isPending}
                      disabled={Boolean(chain.provisioned_by)}
                      disabledReason={t('common.provisionedHint', { tool: chain.provisioned_by ?? '' })}
                      onConfirm={() => remove.mutate(chain.id)}
                    />
                  </Group>
                </Group>
                <Group gap={6} wrap="wrap">
                  {(chain.steps ?? []).length === 0 && (
                    <Text size="sm" c="dimmed">
                      {t('chains.draft')}
                    </Text>
                  )}
                  {/* The chain as a sentence: what it does, to whom, in order.
                      The wire names of the step kinds are in the editor, where
                      somebody is choosing between them — not here, where
                      somebody is checking whether this chain wakes the right
                      person. */}
                  {(chain.steps ?? []).map((step, index) => (
                    <Group gap={6} key={step.id ?? index} wrap="nowrap">
                      {index > 0 && (
                        <Text size="sm" c="dimmed" aria-hidden>
                          →
                        </Text>
                      )}
                      <Tooltip label={stepHint(step.kind)} withArrow>
                        <Text
                          size="sm"
                          c={stepReachesNobody(step) ? 'orange' : undefined}
                          fw={stepReachesNobody(step) ? 500 : 400}
                        >
                          {stepSentence(step, names, t)}
                        </Text>
                      </Tooltip>
                    </Group>
                  ))}
                </Group>
                {chainPagesNobody(chain.steps) && (chain.steps ?? []).length > 0 && (
                  <Group gap={6} mt="xs">
                    <IconAlertTriangle size={14} color="var(--mantine-color-orange-6)" />
                    <Text size="xs" c="orange">
                      {t('chains.pagesNobody')}
                    </Text>
                  </Group>
                )}
              </Paper>
            ))}
          </Stack>
        )}
      </QueryState>

      <CreateChainModal opened={creating} onClose={() => setCreating(false)} />
      <StepsModal chain={selected} onClose={() => setSelected(null)} />
    </>
  );
}

/**
 * "Who does this page right now" — the question a chain exists to answer, and
 * the one nobody could ask without sending a real alert.
 *
 * It resolves the notifying steps against the current rotas and duty flags and
 * lists the people, without delivering anything. The class of error it catches
 * is the expensive one: a chain that looks configured, is configured, and
 * reaches nobody tonight because the rota it names has a hole.
 */
function WhoNowButton({ chain }: { chain: EscalationChain }) {
  const { t } = useI18n();
  const [opened, setOpened] = useState(false);
  const users = useAllOf('users');
  const teams = useAllOf('teams');
  const onCall = useOnCall();

  const steps = chain.steps ?? [];
  const userName = new Map((users.data ?? []).map((user) => [user.id, user.name]));
  const teamById = new Map((teams.data ?? []).map((team) => [team.id, team]));
  const onCallBySchedule = new Map<string, string[]>();
  for (const entry of onCall.data?.items ?? []) {
    const list = onCallBySchedule.get(entry.schedule_id) ?? [];
    list.push(entry.name || entry.username || entry.user_id);
    onCallBySchedule.set(entry.schedule_id, list);
  }

  const reached: string[] = [];
  for (const step of steps) {
    if (step.kind === 'NOTIFY_USER') {
      for (const id of (step.user_ids ?? []) as string[]) reached.push(userName.get(id) ?? id);
    } else if (step.kind === 'NOTIFY_TEAM') {
      for (const id of (step.team_ids ?? []) as string[]) {
        const team = teamById.get(id);
        for (const member of (team?.member_ids ?? []) as string[]) {
          reached.push(userName.get(member) ?? member);
        }
      }
    } else if (step.kind === 'NOTIFY_SCHEDULE') {
      for (const id of (step.schedule_ids ?? []) as string[]) {
        for (const name of onCallBySchedule.get(id) ?? []) reached.push(name);
      }
    } else if (step.kind === 'NOTIFY_DUTY_USERS') {
      for (const user of users.data ?? []) {
        if (user.on_duty) reached.push(user.name);
      }
    }
  }
  const unique = Array.from(new Set(reached));

  return (
    <>
      <Button
        size="compact-sm"
        variant="subtle"
        leftSection={<IconUserSearch size={14} />}
        onClick={() => setOpened(true)}
      >
        {t('chains.whoNow')}
      </Button>
      <Modal opened={opened} onClose={() => setOpened(false)} title={t('chains.whoNow')} centered>
        <Stack gap="sm">
          {unique.length === 0 ? (
            <Text size="sm" c="orange">
              {t('chains.whoNowEmpty')}
            </Text>
          ) : (
            <Group gap="xs" wrap="wrap">
              {unique.map((name) => (
                <Badge key={name} variant="light">
                  {name}
                </Badge>
              ))}
            </Group>
          )}
          <Text size="xs" c="dimmed">
            {t('chains.whoNowHint')}
          </Text>
        </Stack>
      </Modal>
    </>
  );
}

function CreateChainModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const create = useCreate('escalation-chains');
  const [name, setName] = useState('');
  const { t } = useI18n();

  const submit = () => {
    if (!name.trim()) return;
    create.mutate(
      { name, steps: [] },
      {
        onSuccess: () => {
          setName('');
          onClose();
        },
      },
    );
  };
  useSubmitShortcut(opened, submit);

  return (
    <Modal opened={opened} onClose={onClose} title={t('chains.add')} centered>
      <Stack>
        <TextInput
          label={t('common.name')}
          required
          value={name}
          onChange={(event) => setName(event.currentTarget.value)}
        />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            loading={create.isPending}
            disabled={!name.trim()}
            onClick={submit}
          >
            {t('common.create')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}

function StepsModal({ chain, onClose }: { chain: EscalationChain | null; onClose: () => void }) {
  const update = useUpdate('escalation-chains');
  const [steps, setSteps] = useState<EscalationStep[]>([]);
  const { t } = useI18n();
  const stepHint = useStepHint();

  useEffect(() => setSteps(chain?.steps ?? []), [chain]);

  const save = () => {
    if (chain) update.mutate({ id: chain.id, body: { steps } }, { onSuccess: onClose });
  };
  useSubmitShortcut(chain !== null, save);

  const patch = (index: number, changes: Partial<EscalationStep>) =>
    setSteps(steps.map((step, i) => (i === index ? { ...step, ...changes } : step)));

  const move = (index: number, delta: number) => {
    const target = index + delta;
    if (target < 0 || target >= steps.length) return;
    const next = [...steps];
    [next[index], next[target]] = [next[target], next[index]];
    setSteps(next);
  };

  return (
    <Modal
      opened={chain !== null}
      onClose={onClose}
      title={chain ? t('chains.stepsOf', { name: chain.name }) : ''}
      size="xl"
      centered
    >
      <Stack>
        {steps.length === 0 && (
          <Text size="sm" c="dimmed">
            {t('chains.noSteps')}
          </Text>
        )}

        {steps.map((step, index) => (
          <Card withBorder key={index} p="md">
            <Stack gap="sm">
              <Group wrap="nowrap">
                <Badge variant="light">{index + 1}</Badge>
                <Select
                  data={STEP_KINDS}
                  value={step.kind}
                  onChange={(value) => patch(index, { kind: (value ?? 'WAIT') as StepKind })}
                  allowDeselect={false}
                  w={220}
                />
                <Text size="xs" c="dimmed">
                  {stepHint(step.kind)}
                </Text>
                <Group gap={2} ml="auto" wrap="nowrap">
                  <ActionIcon
                    variant="subtle"
                    onClick={() => move(index, -1)}
                    disabled={index === 0}
                    aria-label={t('chains.moveUp')}
                  >
                    <IconArrowUp size={16} />
                  </ActionIcon>
                  <ActionIcon
                    variant="subtle"
                    onClick={() => move(index, 1)}
                    disabled={index === steps.length - 1}
                    aria-label={t('chains.moveDown')}
                  >
                    <IconArrowDown size={16} />
                  </ActionIcon>
                  <ActionIcon
                    color="red"
                    variant="subtle"
                    onClick={() => setSteps(steps.filter((_, i) => i !== index))}
                    aria-label={t('chains.removeStep')}
                  >
                    <IconTrash size={16} />
                  </ActionIcon>
                </Group>
              </Group>
              <StepFields step={step} onChange={(changes) => patch(index, changes)} />
            </Stack>
          </Card>
        ))}

        <Group justify="space-between">
          <Button
            variant="light"
            leftSection={<IconPlus size={16} />}
            onClick={() => setSteps([...steps, { kind: 'WAIT', delay_minutes: 5 }])}
          >
            {t('chains.addStep')}
          </Button>
          <Group>
            <Button variant="default" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button
              leftSection={<IconDeviceFloppy size={16} />}
              loading={update.isPending}
              onClick={save}
            >
              {t('chains.save')}
            </Button>
          </Group>
        </Group>
      </Stack>
    </Modal>
  );
}

function StepFields({
  step,
  onChange,
}: {
  step: EscalationStep;
  onChange: (changes: Partial<EscalationStep>) => void;
}) {
  const users = useAllOf('users');
  const teams = useAllOf('teams');
  const schedules = useAllOf('schedules');
  const { t } = useI18n();

  const userOptions = (users.data ?? []).map((user) => ({ value: user.id, label: user.name }));
  const teamOptions = (teams.data ?? []).map((team) => ({ value: team.id, label: team.name }));

  switch (step.kind) {
    case 'WAIT':
      return (
        <NumberInput
          label={t('chains.delay')}
          min={0}
          value={step.delay_minutes ?? 0}
          onChange={(value) => onChange({ delay_minutes: Number(value) || 0 })}
          w={200}
        />
      );

    case 'NOTIFY_USER':
      return (
        <MultiSelect
          label={t('common.users')}
          searchable
          data={userOptions}
          value={step.user_ids ?? []}
          onChange={(value) => onChange({ user_ids: value })}
        />
      );

    case 'NOTIFY_SCHEDULE':
      return (
        <Stack gap="xs">
          <Select
            label={t('chains.schedule')}
            searchable
            data={(schedules.data ?? []).map((s) => ({ value: s.id, label: s.name }))}
            value={step.schedule_id ?? null}
            onChange={(value) => onChange({ schedule_id: value ?? '' })}
          />
          {/* Saving is refused when the schedule has gaps in the next week; this
              is how an operator accepts them deliberately. */}
          <Checkbox
            label={t('chains.acceptGaps')}
            checked={step.allow_uncovered ?? false}
            onChange={(event) => onChange({ allow_uncovered: event.currentTarget.checked })}
          />
        </Stack>
      );

    case 'NOTIFY_TEAM':
      return (
        <Select
          label={t('common.team')}
          searchable
          data={teamOptions}
          value={step.team_id ?? null}
          onChange={(value) => onChange({ team_id: value })}
        />
      );

    case 'NOTIFY_DUTY_USERS':
      return (
        <Group align="flex-end">
          <Select
            label={t('common.team')}
            placeholder={t('chains.anyTeam')}
            clearable
            searchable
            data={teamOptions}
            value={step.team_id ?? null}
            onChange={(value) => onChange({ team_id: value })}
            w={260}
          />
          <Switch
            label={t('chains.fallbackAll')}
            checked={step.fallback_to_all ?? true}
            onChange={(event) => onChange({ fallback_to_all: event.currentTarget.checked })}
          />
        </Group>
      );

    case 'NOTIFY_EMERGENCY':
      return (
        <Select
          label={t('chains.emergencyUser')}
          placeholder={t('chains.useIntegrationPolicy')}
          clearable
          searchable
          data={userOptions}
          value={step.user_id ?? null}
          onChange={(value) => onChange({ user_id: value })}
          w={280}
        />
      );

    case 'TRIGGER_WEBHOOK':
      return (
        <TextInput
          label={t('chains.webhookUrl')}
          placeholder="https://example.com/hook"
          value={step.webhook_url ?? ''}
          onChange={(event) => onChange({ webhook_url: event.currentTarget.value })}
        />
      );

    case 'CREATE_ISSUE':
      return (
        <Stack gap="xs">
          <Group grow>
            <TextInput
              label={t('chains.trackerType')}
              placeholder="redmine"
              value={step.tracker_type ?? ''}
              onChange={(event) => onChange({ tracker_type: event.currentTarget.value })}
            />
            <TextInput
              label={t('chains.url')}
              required
              value={step.url ?? ''}
              onChange={(event) => onChange({ url: event.currentTarget.value })}
            />
            <TextInput
              label={t('chains.project')}
              value={step.project ?? ''}
              onChange={(event) => onChange({ project: event.currentTarget.value })}
            />
          </Group>
          <Group grow>
            <TextInput
              label={t('chains.token')}
              value={step.token ?? ''}
              onChange={(event) => onChange({ token: event.currentTarget.value })}
            />
            <TextInput
              label={t('chains.tokenEnv')}
              description={t('chains.tokenEnvDescription')}
              value={step.token_env ?? ''}
              onChange={(event) => onChange({ token_env: event.currentTarget.value })}
            />
          </Group>
          <TextInput
            label={t('chains.subjectTemplate')}
            value={step.subject_template ?? ''}
            onChange={(event) => onChange({ subject_template: event.currentTarget.value })}
          />
          <Textarea
            label={t('chains.bodyTemplate')}
            autosize
            minRows={3}
            value={step.body_template ?? ''}
            onChange={(event) => onChange({ body_template: event.currentTarget.value })}
          />
        </Stack>
      );

    case 'REPEAT':
      return (
        <Group>
          <NumberInput
            label={t('chains.fromPosition')}
            min={0}
            value={step.from_position ?? 0}
            onChange={(value) => onChange({ from_position: Number(value) || 0 })}
            w={160}
          />
          <NumberInput
            label={t('chains.maxRepeats')}
            min={1}
            value={step.max_repeat_count ?? 50}
            onChange={(value) => onChange({ max_repeat_count: Number(value) || 1 })}
            w={160}
          />
          <NumberInput
            label={t('chains.cooldown')}
            min={0}
            value={step.cooldown_minutes ?? 60}
            onChange={(value) => onChange({ cooldown_minutes: Number(value) || 0 })}
            w={180}
          />
        </Group>
      );

    case 'RESOLVE':
      return (
        <Text size="sm" c="dimmed">
          {t('chains.noConfig')}
        </Text>
      );
  }
}
