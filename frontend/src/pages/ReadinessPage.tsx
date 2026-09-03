import { useState } from 'react';
import {
  Alert,
  Badge,
  Button,
  Card,
  Group,
  List,
  Modal,
  Stack,
  Text,
  Textarea,
  ThemeIcon,
} from '@mantine/core';
import {
  IconAlertTriangle,
  IconCheck,
  IconExclamationCircle,
  IconRefresh,
} from '@tabler/icons-react';
import { PageHeader, QueryState } from '../components/common';
import { StatTile } from '../components/StatTile';
import { useAcknowledgeReadiness, useReadiness } from '../api/hooks';
import { useAuth } from '../auth/AuthProvider';
import type { Readiness, ReadinessCheck, ReadinessSeverity } from '../api/types';
import { useI18n } from '../i18n/I18nProvider';

const SEVERITY: Record<ReadinessSeverity, { color: string; icon: typeof IconCheck; label: string }> =
  {
    ok: { color: 'teal', icon: IconCheck, label: 'ok' },
    warning: { color: 'yellow', icon: IconAlertTriangle, label: 'warning' },
    blocker: { color: 'red', icon: IconExclamationCircle, label: 'blocker' },
  };

/** Blockers first, then warnings: the page should open on what is broken. */
const ORDER: ReadinessSeverity[] = ['blocker', 'warning', 'ok'];

function CheckCard({ check }: { check: ReadinessCheck }) {
  const { t, locale } = useI18n();
  // `string` intentionally includes checks introduced by a newer backend than
  // the generated client schema; unknown keys still fall back to check.title.
  const localizedTitles: Partial<Record<string, string>> = {
    database: t('readiness.check.database'),
    worker: t('readiness.check.worker'),
    integrations: t('readiness.check.integrations'),
    routing: t('readiness.check.routing'),
    notification_targets: t('readiness.check.notification_targets'),
    team_scoping: t('readiness.check.team_scoping'),
    schedule_coverage: t('readiness.check.schedule_coverage'),
    backup: t('readiness.check.backup'),
    channel_policy: t('readiness.check.channel_policy'),
    data_retention: t('readiness.check.data_retention'),
  };
  const meta = SEVERITY[check.severity] ?? SEVERITY.warning;
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
              {locale === 'ru-RU' ? (localizedTitles[check.key] ?? check.title) : check.title}
            </Text>
            <Text size="sm" c="dimmed" mt={2}>
              {check.detail}
            </Text>
            {check.items && check.items.length > 0 && (
              <List size="sm" mt="xs" spacing={2}>
                {check.items.map((item) => (
                  <List.Item key={item}>{item}</List.Item>
                ))}
              </List>
            )}
          </div>
        </Group>
        <Badge color={meta.color} variant="light">
          {check.severity === 'ok'
            ? t('readiness.ok')
            : check.severity === 'blocker'
              ? t('readiness.blocker')
              : t('readiness.warning')}
        </Badge>
      </Group>
    </Card>
  );
}

function AcknowledgeBlockers({ readiness }: { readiness: Readiness }) {
  const [opened, setOpened] = useState(false);
  const [reason, setReason] = useState('');
  const acknowledge = useAcknowledgeReadiness();
  const { identity } = useAuth();
  const { t } = useI18n();

  if (readiness.blockers === 0) return null;
  // The API refuses a non-admin acknowledgement anyway; offering the button
  // would only produce a 403.
  if (!identity?.permissions.admin) return null;

  const submit = () => {
    acknowledge.mutate(
      { reason },
      {
        onSuccess: () => {
          setOpened(false);
          setReason('');
        },
      },
    );
  };

  return (
    <>
      <Button color="red" variant="light" onClick={() => setOpened(true)}>
        {t('readiness.acknowledge')}
      </Button>
      <Modal opened={opened} onClose={() => setOpened(false)} title={t('readiness.ackTitle')}>
        <Stack>
          <Text size="sm">
            {t('readiness.ackBody', { count: readiness.blockers })}
          </Text>
          <Text size="sm" c="dimmed">
            {t('readiness.ackScope')}
          </Text>
          <Textarea
            label={t('common.reason')}
            description={t('readiness.reasonDescription')}
            placeholder={t('readiness.reasonPlaceholder')}
            value={reason}
            onChange={(event) => setReason(event.currentTarget.value)}
            autosize
            minRows={2}
            required
          />
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setOpened(false)}>
              {t('common.cancel')}
            </Button>
            <Button
              color="red"
              onClick={submit}
              loading={acknowledge.isPending}
              disabled={reason.trim() === ''}
            >
              {t('readiness.acknowledge')}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}

export function ReadinessPage() {
  const readiness = useReadiness();
  const { t, fmt } = useI18n();

  return (
    <>
      <PageHeader
        title={t('readiness.title')}
        description={t('readiness.description')}
        actions={
          <Button
            variant="default"
            leftSection={<IconRefresh size={16} />}
            onClick={() => readiness.refetch()}
            loading={readiness.isFetching}
          >
            {t('readiness.recheck')}
          </Button>
        }
      />
      <QueryState query={readiness}>
        {(data) => {
          const checks = [...(data.checks ?? [])].sort(
            (a, b) => ORDER.indexOf(a.severity) - ORDER.indexOf(b.severity),
          );
          const ack = data.acknowledgement;
          return (
            <Stack gap="lg">
              <Group grow align="stretch">
                <StatTile
                  label={t('readiness.blockers')}
                  value={String(data.blockers)}
                  color={data.blockers > 0 ? 'red' : 'teal'}
                />
                <StatTile
                  label={t('readiness.warnings')}
                  value={String(data.warnings)}
                  color={data.warnings > 0 ? 'yellow' : 'teal'}
                />
                <StatTile
                  label={t('readiness.production')}
                  value={data.production_ready ? t('readiness.allowed') : t('readiness.blocked')}
                  color={data.production_ready ? 'teal' : 'red'}
                />
              </Group>

              {data.blockers > 0 && !data.production_ready && (
                <Alert color="red" icon={<IconExclamationCircle size={18} />} title={t('readiness.notReady')}>
                  {t('readiness.notReadyBody', { count: data.blockers })}
                </Alert>
              )}
              {data.blockers > 0 && data.production_ready && ack && (
                <Alert
                  color="orange"
                  icon={<IconAlertTriangle size={18} />}
                  title={t('readiness.runningAcknowledged')}
                >
                  {t('readiness.acceptedAt', { actor: ack.actor, when: fmt.relative(ack.at), reason: ack.reason })}
                </Alert>
              )}
              {ack && ack.current === false && (
                <Alert
                  color="yellow"
                  icon={<IconAlertTriangle size={18} />}
                  title={t('readiness.staleAck')}
                >
                  {t('readiness.staleAckBody', { actor: ack.actor, when: fmt.relative(ack.at) })}
                </Alert>
              )}

              <Group justify="space-between" align="center">
                <Text size="sm" c="dimmed">
                  {t('readiness.checked', { when: fmt.relative(data.checked_at) })}
                </Text>
                <AcknowledgeBlockers readiness={data} />
              </Group>

              <Stack gap="sm">
                {checks.map((check) => (
                  <CheckCard key={check.key} check={check} />
                ))}
              </Stack>
            </Stack>
          );
        }}
      </QueryState>
    </>
  );
}
