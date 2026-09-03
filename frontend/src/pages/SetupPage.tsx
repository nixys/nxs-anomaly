import { Link } from 'react-router-dom';
import {
  Alert,
  Badge,
  Button,
  Card,
  Group,
  List,
  Progress,
  Select,
  Stack,
  Text,
  ThemeIcon,
} from '@mantine/core';
import { useState } from 'react';
import {
  IconAlertTriangle,
  IconCheck,
  IconCircleDashed,
  IconExclamationCircle,
  IconSend,
} from '@tabler/icons-react';
import { PageHeader } from '../components/common';
import { useAllOf, useDebugRoute, useReadiness } from '../api/hooks';
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
 * The last wizard step, done here rather than by a link: routing preview is the
 * one thing the existing pages do not offer in a single click, and it is the
 * step that proves the whole path.
 *
 * It uses the routing debug endpoint, which resolves the route and the chain
 * without delivering anything — a dry run, so a pilot team can check the wiring
 * without paging whoever is on call.
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
            {t('setup.dryRunDescription')}
          </Text>
        </div>
        <Group align="flex-end">
          <Select
            label={t('common.integration')}
            placeholder={t('setup.pickIntegration')}
            data={options}
            value={key}
            onChange={setKey}
            searchable
            style={{ flex: 1 }}
          />
          <Button
            leftSection={<IconSend size={16} />}
            onClick={send}
            disabled={!key}
            loading={debug.isPending}
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
    chains: chains.data?.length ?? 0,
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
