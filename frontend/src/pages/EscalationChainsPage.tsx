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
  IconArrowDown,
  IconArrowUp,
  IconDeviceFloppy,
  IconPlus,
  IconTrash,
} from '@tabler/icons-react';
import { useAllOf, useCreate, useDelete, useList, useUpdate } from '../api/hooks';
import { STEP_KINDS, type EscalationChain, type EscalationStep, type StepKind } from '../api/types';
import { ConfirmDeleteButton, PageHeader, ProvisionedBadge, QueryState } from '../components/common';
import { useI18n } from '../i18n/I18nProvider';
import type { Messages } from '../i18n/messages';

function useStepHint() {
  const { t } = useI18n();
  return (kind: StepKind) => t(`chains.hint.${kind}` as keyof Pick<Messages, `chains.hint.${StepKind}`>);
}

export function EscalationChainsPage() {
  const { t } = useI18n();
  const stepHint = useStepHint();
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
                <Group gap={6}>
                  {(chain.steps ?? []).length === 0 && (
                    <Text size="sm" c="dimmed">
                      {t('chains.draft')}
                    </Text>
                  )}
                  {(chain.steps ?? []).map((step, index) => (
                    <Tooltip key={step.id ?? index} label={stepHint(step.kind)} withArrow>
                      <Badge variant="default" tt="none" style={{ fontWeight: 400 }}>
                        {index + 1}. {step.kind}
                        {step.kind === 'WAIT' && step.delay_minutes
                          ? ` ${step.delay_minutes}m`
                          : ''}
                      </Badge>
                    </Tooltip>
                  ))}
                </Group>
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

function CreateChainModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const create = useCreate('escalation-chains');
  const [name, setName] = useState('');
  const { t } = useI18n();

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
            onClick={() =>
              create.mutate(
                { name, steps: [] },
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

function StepsModal({ chain, onClose }: { chain: EscalationChain | null; onClose: () => void }) {
  const update = useUpdate('escalation-chains');
  const [steps, setSteps] = useState<EscalationStep[]>([]);
  const { t } = useI18n();
  const stepHint = useStepHint();

  useEffect(() => setSteps(chain?.steps ?? []), [chain]);

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
              onClick={() =>
                chain &&
                update.mutate({ id: chain.id, body: { steps } }, { onSuccess: onClose })
              }
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
