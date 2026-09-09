import { Link } from 'react-router-dom';
import {
  Alert,
  Badge,
  Button,
  Card,
  Divider,
  Group,
  List,
  Progress,
  Select,
  Stack,
  Text,
  ThemeIcon,
} from '@mantine/core';
import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  IconAlertTriangle,
  IconCheck,
  IconCircleDashed,
  IconExclamationCircle,
  IconSend,
} from '@tabler/icons-react';
import { PageHeader } from '../components/common';
import { api } from '../api/client';
import { useAllOf, useDebugRoute, useList, useReadiness } from '../api/hooks';
import type { AlertGroup } from '../api/types';
import { useI18n } from '../i18n/I18nProvider';
import { buildSetupSteps, firstOpenStep, setupProgress, type SetupStep } from './setup-steps';

const STATUS_META = {
  done: { color: 'teal', icon: IconCheck, labelKey: 'setup.statusDone' },
  blocked: { color: 'red', icon: IconExclamationCircle, labelKey: 'setup.statusBlocked' },
  todo: { color: 'gray', icon: IconCircleDashed, labelKey: 'setup.statusTodo' },
} as const;

function StepCard({ step, index }: { step: SetupStep; index: number }) {
  const { t } = useI18n();
  const meta = STATUS_META[step.status];
  const Icon = meta.icon;
  return (
    <Card withBorder padding="md">
      <Group justify="space-between" align="flex-start" wrap="nowrap">
        <Group align="flex-start" wrap="nowrap" gap="sm">
          <ThemeIcon color={meta.color} variant="light" size="md" radius="xl">
            <Icon size={16} />
          </ThemeIcon>
          <div>
            <Text fw={600}>
              {index + 1}. {t(step.labelKey)}
            </Text>
            <Text size="sm" c="dimmed" mt={2}>
              {t(step.descriptionKey)}
            </Text>
            {step.detail && (
              <Text size="sm" c={step.status === 'blocked' ? 'red' : undefined} mt="xs">
                {step.detail}
              </Text>
            )}
            {step.items.length > 0 && (
              <List size="sm" mt="xs" spacing={2}>
                {step.items.map((item) => (
                  <List.Item key={item}>{item}</List.Item>
                ))}
              </List>
            )}
          </div>
        </Group>
        <Group gap="xs" wrap="nowrap">
          <Badge color={meta.color} variant="light">
            {t(meta.labelKey)}
          </Badge>
          {step.to && (
            <Button component={Link} to={step.to} size="xs" variant="light">
              {step.status === 'done' ? t('common.review') : t('common.open')}
            </Button>
          )}
        </Group>
      </Group>
    </Card>
  );
}

/**
 * One stage of the real end-to-end test: route found, a provider attempted
 * delivery, a human acknowledged. These are not equivalent — a resolved route
 * can still page nobody if delivery fails, and a delivered notification is not
 * the same as someone having seen it — so each gets its own status rather than
 * being folded into a single pass/fail.
 */
type StageState = 'pending' | 'waiting' | 'ok' | 'fail';

function Stage({ state, label, detail }: { state: StageState; label: string; detail?: string }) {
  const meta: Record<StageState, { color: string; icon: typeof IconCheck }> = {
    pending: { color: 'gray', icon: IconCircleDashed },
    waiting: { color: 'blue', icon: IconCircleDashed },
    ok: { color: 'teal', icon: IconCheck },
    fail: { color: 'red', icon: IconExclamationCircle },
  };
  const { color, icon: Icon } = meta[state];
  return (
    <Group gap="xs" align="flex-start" wrap="nowrap">
      <ThemeIcon color={color} variant="light" size="sm" radius="xl" mt={2}>
        <Icon size={12} />
      </ThemeIcon>
      <div>
        <Text size="sm">{label}</Text>
        {detail && (
          <Text size="xs" c="dimmed">
            {detail}
          </Text>
        )}
      </div>
    </Group>
  );
}

/**
 * The real send: an actual alert through the ingest endpoint, not a preview.
 * It proves route resolution, real provider delivery, and lets the operator
 * follow through to a real acknowledgement — three separate, observable
 * outcomes, because "the route exists" and "someone would actually be paged"
 * are different claims and a wizard that shows only one teaches people to
 * trust the wrong one.
 *
 * Polls rather than assumes: escalation and delivery run on the worker's own
 * cycle, not synchronously with ingest, so the notification for a freshly
 * created group may not exist yet on the first check.
 */
/** Re-renders every second while `active`, so a `now < deadline` comparison
 *  elsewhere in the component turns itself false without any other event
 *  (a query response, a click) having to trigger the re-render. */
function useEffectPollClock(active: boolean, setNow: (now: number) => void) {
  useEffect(() => {
    if (!active) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [active, setNow]);
}

// How long to keep polling after a real test alert is sent: long enough for
// the worker's default 5s poll interval to run a couple of cycles, short
// enough that leaving the page open does not poll forever.
const REAL_TEST_POLL_BUDGET_MS = 45_000;

function RealTest({ integrationKey }: { integrationKey: string | null }) {
  const { t } = useI18n();
  const [groupId, setGroupId] = useState<string | null>(null);
  const [sendError, setSendError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [pollUntil, setPollUntil] = useState(0);
  const [now, setNow] = useState(() => Date.now());
  const stillPolling = Boolean(groupId) && now < pollUntil;

  // Ticks the clock while a test is in flight, which is what lets
  // stillPolling — and therefore refetchInterval below — turn itself off.
  useEffectPollClock(stillPolling, setNow);

  const group = useQuery<AlertGroup | null>({
    queryKey: ['setup-test', 'group', groupId],
    queryFn: () => api.get<AlertGroup>(`/api/v1/alert-groups/${groupId}`),
    enabled: Boolean(groupId),
    refetchInterval: stillPolling ? 2000 : false,
  });
  const notifications = useList(
    'notifications',
    { alert_group_id: groupId ?? undefined, limit: 10 },
    { enabled: Boolean(groupId), refetchInterval: stillPolling ? 2000 : false },
  );

  const send = async () => {
    if (!integrationKey) return;
    setSending(true);
    setSendError(null);
    setGroupId(null);
    try {
      const marker = `setup-test-${Date.now().toString(36)}`;
      const result = await api.post<{ group: AlertGroup | null; result: string }>(
        `/integrations/v1/webhook/${integrationKey}`,
        {
          title: 'nxs-anomaly setup test',
          dedupe_key: marker,
          labels: { alertname: 'SetupTest', severity: 'critical' },
        },
      );
      if (!result.group) {
        setSendError(t('setup.realTestDropped'));
        return;
      }
      setGroupId(result.group.id);
      setPollUntil(Date.now() + REAL_TEST_POLL_BUDGET_MS);
    } catch (err) {
      setSendError(err instanceof Error ? err.message : String(err));
    } finally {
      setSending(false);
    }
  };

  const routeState: StageState = !groupId ? 'pending' : group.data ? 'ok' : 'waiting';
  const items = notifications.data?.items ?? [];
  const deliveryState: StageState = !groupId
    ? 'pending'
    : items.length === 0
      ? stillPolling
        ? 'waiting'
        : 'fail'
      : items.some((n) => n.status === 'delivered')
        ? 'ok'
        : items.every((n) => n.status === 'failed' || n.status === 'skipped')
          ? 'fail'
          : 'waiting';
  const ackState: StageState = !groupId
    ? 'pending'
    : group.data?.status === 'acknowledged' || group.data?.status === 'resolved'
      ? 'ok'
      : 'waiting';

  const deliveryDetail = items.length
    ? items.map((n) => `${n.channel} → ${n.status}`).join(', ')
    : undefined;

  return (
    <Stack gap="sm">
      {sendError && (
        <Alert color="red" title={t('setup.realTestFailed')}>
          {sendError}
        </Alert>
      )}
      {groupId && (
        <Stack gap={6}>
          <Stage state={routeState} label={t('setup.stageRouteFound')} />
          <Stage
            state={deliveryState}
            label={t('setup.stageDelivery')}
            detail={
              deliveryDetail ??
              (deliveryState === 'waiting' ? t('setup.stageDeliveryWaiting') : undefined)
            }
          />
          <Stage
            state={ackState}
            label={t('setup.stageAck')}
            detail={ackState !== 'ok' ? t('setup.stageAckHint') : undefined}
          />
          {group.data && ackState !== 'ok' && (
            <Button
              component={Link}
              to={`/alert-groups/${group.data.id}`}
              size="xs"
              variant="light"
              style={{ alignSelf: 'flex-start' }}
            >
              {t('setup.openAlertGroup')}
            </Button>
          )}
        </Stack>
      )}
      <Group>
        <Button
          leftSection={<IconSend size={16} />}
          onClick={send}
          disabled={!integrationKey}
          loading={sending}
          variant="filled"
        >
          {t('setup.sendRealTest')}
        </Button>
      </Group>
    </Stack>
  );
}

/**
 * The wizard's last step. Two independent actions, deliberately not one
 * button: a dry run (routing debug — resolves the route and chain for a
 * sample payload without delivering anything) is quick to repeat while
 * editing routes, and the real test below actually ingests, escalates and
 * delivers through a live provider — proving the path a dry run cannot.
 */
function TestAlert({ step, index }: { step: SetupStep; index: number }) {
  const integrations = useAllOf('integrations');
  const [key, setKey] = useState<string | null>(null);
  const debug = useDebugRoute();
  const { t } = useI18n();

  const options = (integrations.data ?? []).map((integration) => ({
    value: String(integration.key),
    label: `${integration.name} (${integration.key})`,
  }));

  const send = () => {
    if (!key) return;
    debug.mutate({
      key,
      payload: { title: 'nxs-anomaly setup test', severity: 'critical', labels: { setup: 'true' } },
    });
  };

  return (
    <Card withBorder padding="md">
      <Stack gap="sm">
        <div>
          <Text fw={600}>
            {index + 1}. {t(step.labelKey)}
          </Text>
          <Text size="sm" c="dimmed" mt={2}>
            {t(step.descriptionKey)}
          </Text>
        </div>
        <Select
          label={t('common.integration')}
          placeholder={t('setup.pickIntegration')}
          data={options}
          value={key}
          onChange={setKey}
          searchable
        />

        <Divider label={t('setup.dryRun')} labelPosition="left" />
        <Text size="xs" c="dimmed" mt={-8}>
          {t('setup.dryRunDescription')}
        </Text>
        <Group>
          <Button
            leftSection={<IconSend size={16} />}
            onClick={send}
            disabled={!key}
            loading={debug.isPending}
            variant="default"
          >
            {t('setup.dryRun')}
          </Button>
        </Group>
        {debug.data !== undefined && (
          <Alert color="teal" title={t('setup.routeResolved')}>
            <Text size="sm" component="pre" style={{ whiteSpace: 'pre-wrap', margin: 0 }}>
              {JSON.stringify(debug.data, null, 2)}
            </Text>
          </Alert>
        )}

        <Divider label={t('setup.sendRealTest')} labelPosition="left" />
        <Text size="xs" c="dimmed" mt={-8}>
          {t('setup.realTestDescription')}
        </Text>
        <RealTest integrationKey={key} />
      </Stack>
    </Card>
  );
}

export function SetupPage() {
  const { t, plural } = useI18n();
  const readiness = useReadiness();
  const users = useAllOf('users');
  const teams = useAllOf('teams');
  const schedules = useAllOf('schedules');
  const chains = useAllOf('escalation-chains');
  const integrations = useAllOf('integrations');

  const steps = buildSetupSteps(readiness.data, {
    teams: teams.data?.length ?? 0,
    users: users.data?.length ?? 0,
    schedules: schedules.data?.length ?? 0,
    hasNonEmptyChain: (chains.data ?? []).some((c) => (c.steps?.length ?? 0) > 0),
    integrations: integrations.data?.length ?? 0,
  });
  const progress = setupProgress(steps);
  const next = firstOpenStep(steps);

  return (
    <>
      <PageHeader
        title={t('setup.title')}
        description={t('setup.description')}
        actions={
          <Button component={Link} to="/readiness" variant="default">
            {t('setup.fullReport')}
          </Button>
        }
      />
      <Stack gap="lg">
        <Card withBorder padding="md">
          <Group justify="space-between" mb="xs">
            <Text fw={600}>
              {t('setup.progress', { done: progress.done, total: progress.total })}
            </Text>
            {next && (
              <Text size="sm" c="dimmed">
                {t('setup.next', { step: t(next.labelKey) })}
              </Text>
            )}
          </Group>
          <Progress value={progress.percent} color={progress.percent === 100 ? 'teal' : 'blue'} />
        </Card>

        {readiness.data && readiness.data.blockers > 0 && (
          <Alert
            color="red"
            icon={<IconExclamationCircle size={18} />}
            title={t('setup.blockersTitle')}
          >
            {plural('setup.blockersBody', readiness.data.blockers)}{' '}
            <Link to="/readiness">{t('setup.readinessLink')}</Link>
          </Alert>
        )}
        {readiness.isError && (
          <Alert
            color="yellow"
            icon={<IconAlertTriangle size={18} />}
            title={t('setup.readinessUnavailable')}
          >
            {t('setup.readinessUnavailableBody')}
          </Alert>
        )}

        <Stack gap="sm">
          {steps.map((step, index) =>
            // The final step is interactive rather than a link, so it renders as
            // the dry-run card below — keeping its position in the numbering.
            step.key === 'test' ? (
              <TestAlert key={step.key} step={step} index={index} />
            ) : (
              <StepCard key={step.key} step={step} index={index} />
            ),
          )}
        </Stack>
      </Stack>
    </>
  );
}
