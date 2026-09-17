import { useEffect, useState } from 'react';
import {
  ActionIcon,
  Button,
  Card,
  Group,
  JsonInput,
  MultiSelect,
  NumberInput,
  Paper,
  Radio,
  Select,
  SimpleGrid,
  Stack,
  Tabs,
  Text,
  TextInput,
  Textarea,
  Title,
} from '@mantine/core';
import { IconArrowLeft, IconDeviceFloppy, IconPlus, IconRefresh, IconTrash } from '@tabler/icons-react';
import { Link, useParams } from 'react-router-dom';
import { useAllOf, useDebugRoute, useItem, useRotateKey, useUpdate } from '../api/hooks';
import {
  NOTIFICATION_TARGET_TYPES,
  type Integration,
  type IntegrationRoute,
  type NotificationPolicy,
  type NotificationTargetType,
  type RouteMatchType,
} from '../api/types';
import { JsonBlock, PageHeader, ProvisionedNotice, QueryState } from '../components/common';
import { CopyField, ingestUrl } from './IntegrationsPage';
import { useI18n } from '../i18n/I18nProvider';
import { useChannelLabel } from '../i18n/domain';

export function IntegrationDetailPage() {
  const { t } = useI18n();
  const { id } = useParams<{ id: string }>();
  const integration = useItem('integrations', id);

  return (
    <>
      <PageHeader
        title={integration.data?.name ?? t('integration.title')}
        description={id}
        actions={
          <Button
            component={Link}
            to="/integrations"
            variant="default"
            leftSection={<IconArrowLeft size={16} />}
          >
            {t('common.back')}
          </Button>
        }
      />
      <QueryState query={integration}>
        {(data) => (
          <>
            <ProvisionedNotice by={data.provisioned_by} />
            <Tabs defaultValue="overview">
            <Tabs.List mb="md">
              <Tabs.Tab value="overview">{t('integration.overview')}</Tabs.Tab>
              <Tabs.Tab value="routes">{t('integration.routesTab', { count: (data.routes ?? []).length })}</Tabs.Tab>
              <Tabs.Tab value="policy">{t('integration.policy')}</Tabs.Tab>
              <Tabs.Tab value="templates">{t('integration.templates')}</Tabs.Tab>
              <Tabs.Tab value="debug">{t('integration.debugger')}</Tabs.Tab>
            </Tabs.List>
            <Tabs.Panel value="overview">
              <OverviewTab integration={data} />
            </Tabs.Panel>
            <Tabs.Panel value="routes">
              <RoutesTab integration={data} />
            </Tabs.Panel>
            <Tabs.Panel value="policy">
              <PolicyTab integration={data} />
            </Tabs.Panel>
            <Tabs.Panel value="templates">
              <TemplatesTab integration={data} />
            </Tabs.Panel>
            <Tabs.Panel value="debug">
              <DebugTab integration={data} />
            </Tabs.Panel>
            </Tabs>
          </>
        )}
      </QueryState>
    </>
  );
}

function OverviewTab({ integration }: { integration: Integration }) {
  const update = useUpdate('integrations');
  const rotate = useRotateKey();
  const [name, setName] = useState(integration.name);
  const [groupBy, setGroupBy] = useState((integration.group_by ?? []).join(', '));
  // The API never returns the secret, only whether one is set. The field holds
  // a replacement: left empty it changes nothing, so saving the name cannot
  // wipe a secret the form was never shown.
  const [secret, setSecret] = useState('');
  const secretSet = Boolean(integration.webhook_secret_set);
  const { t } = useI18n();

  useEffect(() => {
    setName(integration.name);
    setGroupBy((integration.group_by ?? []).join(', '));
    setSecret('');
  }, [integration]);

  const save = () =>
    update.mutate({
      id: integration.id,
      body: {
        name,
        group_by: groupBy
          .split(',')
          .map((part) => part.trim())
          .filter(Boolean),
        ...(secret !== '' ? { webhook_secret: secret } : {}),
      },
    });
  const removeSecret = () => update.mutate({ id: integration.id, body: { webhook_secret: null } });

  return (
    <Stack gap="md">
      <Paper withBorder p="lg">
        <Stack>
          <TextInput
            label={t('common.name')}
            value={name}
            onChange={(event) => setName(event.currentTarget.value)}
          />
          <TextInput
            label={t('integrations.groupBy')}
            description={t('integrations.groupByDescription')}
            value={groupBy}
            onChange={(event) => setGroupBy(event.currentTarget.value)}
          />
          <TextInput
            label={t('integration.webhookSecret')}
            description={
              secretSet
                ? t('integration.webhookSecretConfigured')
                : t('integration.webhookSecretDescription')
            }
            placeholder={secretSet ? t('integration.webhookSecretKeep') : undefined}
            value={secret}
            onChange={(event) => setSecret(event.currentTarget.value)}
          />
          {secretSet && (
            <Group>
              <Button
                variant="subtle"
                color="red"
                size="xs"
                leftSection={<IconTrash size={14} />}
                onClick={removeSecret}
                loading={update.isPending}
                disabled={Boolean(integration.provisioned_by)}
              >
                {t('integration.webhookSecretRemove')}
              </Button>
            </Group>
          )}
          <Group justify="flex-end">
            <Button
              leftSection={<IconDeviceFloppy size={16} />}
              onClick={save}
              loading={update.isPending}
              disabled={Boolean(integration.provisioned_by)}
            >
              {t('common.save')}
            </Button>
          </Group>
        </Stack>
      </Paper>

      <Paper withBorder p="lg">
        <Stack gap="sm">
          <Title order={5}>{t('integration.ingestion')}</Title>
          <SimpleGrid cols={{ base: 1, md: 2 }}>
            <div>
              <Text size="xs" c="dimmed" tt="uppercase" fw={600} mb={4}>
                {t('integration.endpoint')}
              </Text>
              <CopyField value={ingestUrl(integration)} />
            </div>
            <div>
              <Text size="xs" c="dimmed" tt="uppercase" fw={600} mb={4}>
                {t('integration.routingKey')}
              </Text>
              <CopyField value={integration.key} />
            </div>
          </SimpleGrid>
          <Group>
            <Button
              variant="light"
              color="orange"
              leftSection={<IconRefresh size={16} />}
              loading={rotate.isPending}
              onClick={() => rotate.mutate(integration.id)}
            >
              {t('integration.rotateKey')}
            </Button>
            <Text size="xs" c="dimmed">
              {t('integration.rotateWarning')}
            </Text>
          </Group>
        </Stack>
      </Paper>
    </Stack>
  );
}

/**
 * A route being edited. Saved routes always carry an `id` — the contract
 * guarantees it on read — but one added here has none until the server assigns
 * it on save, so the editor works with a draft type rather than pretending an
 * unsaved route is a complete one.
 */
type RouteDraft = Omit<IntegrationRoute, 'id'> & { id?: string };

const EMPTY_ROUTE: RouteDraft = {
  name: '',
  match_type: 'labels',
  is_default: false,
  labels: {},
  pattern: '',
  escalation_chain_id: null,
};

function RoutesTab({ integration }: { integration: Integration }) {
  const update = useUpdate('integrations');
  const chains = useAllOf('escalation-chains');
  const [routes, setRoutes] = useState<RouteDraft[]>(integration.routes ?? []);
  const { t } = useI18n();

  useEffect(() => setRoutes(integration.routes ?? []), [integration]);

  const patch = (index: number, changes: Partial<RouteDraft>) =>
    setRoutes(routes.map((route, i) => (i === index ? { ...route, ...changes } : route)));

  const setDefault = (index: number) =>
    setRoutes(routes.map((route, i) => ({ ...route, is_default: i === index })));

  const defaultCount = routes.filter((route) => route.is_default).length;

  const save = () => update.mutate({ id: integration.id, body: { routes } });

  return (
    <Stack gap="md">
      <Group justify="space-between">
        <Text size="sm" c="dimmed">
          {t('integration.routesHelp')}
        </Text>
        <Group>
          <Button
            variant="light"
            leftSection={<IconPlus size={16} />}
            onClick={() => setRoutes([...routes, { ...EMPTY_ROUTE, name: `route-${routes.length + 1}` }])}
          >
            {t('integration.addRoute')}
          </Button>
          <Button
            leftSection={<IconDeviceFloppy size={16} />}
            onClick={save}
            loading={update.isPending}
            disabled={defaultCount !== 1 || Boolean(integration.provisioned_by)}
          >
            {t('integration.saveRoutes')}
          </Button>
        </Group>
      </Group>

      {defaultCount !== 1 && (
        <Text size="sm" c="red">
          {t('integration.defaultCount', { count: defaultCount })}
        </Text>
      )}

      {routes.map((route, index) => (
        <Card withBorder key={index} p="lg">
          <Stack>
            <Group align="flex-end" wrap="wrap">
              <TextInput
                label={t('common.name')}
                value={route.name}
                onChange={(event) => patch(index, { name: event.currentTarget.value })}
                w={200}
              />
              <Select
                label={t('integration.matchType')}
                data={['all', 'labels', 'regex']}
                value={route.match_type}
                onChange={(value) => patch(index, { match_type: (value ?? 'all') as RouteMatchType })}
                allowDeselect={false}
                w={140}
              />
              <Select
                label={t('integration.chain')}
                placeholder={t('common.none')}
                clearable
                searchable
                data={(chains.data ?? []).map((chain) => ({ value: chain.id, label: chain.name }))}
                value={route.escalation_chain_id}
                onChange={(value) => patch(index, { escalation_chain_id: value })}
                w={240}
              />
              <Radio
                label={t('integration.defaultRoute')}
                checked={route.is_default}
                onChange={() => setDefault(index)}
              />
              <ActionIcon
                color="red"
                variant="subtle"
                ml="auto"
                onClick={() => setRoutes(routes.filter((_, i) => i !== index))}
                aria-label={t('integration.removeRoute')}
              >
                <IconTrash size={16} />
              </ActionIcon>
            </Group>

            {route.match_type === 'labels' && (
              <LabelsEditor
                labels={route.labels ?? {}}
                onChange={(labels) => patch(index, { labels })}
              />
            )}
            {route.match_type === 'regex' && (
              <TextInput
                label={t('integration.pattern')}
                description={t('integration.patternDescription')}
                value={route.pattern}
                onChange={(event) => patch(index, { pattern: event.currentTarget.value })}
              />
            )}
          </Stack>
        </Card>
      ))}
    </Stack>
  );
}

function LabelsEditor({
  labels,
  onChange,
}: {
  labels: Record<string, string>;
  onChange: (labels: Record<string, string>) => void;
}) {
  const { t } = useI18n();
  const entries = Object.entries(labels);
  const setEntry = (index: number, key: string, value: string) => {
    const next = entries.map((entry, i) => (i === index ? [key, value] : entry));
    onChange(Object.fromEntries(next));
  };

  return (
    <Stack gap="xs">
      <Group justify="space-between">
        <Text size="sm" fw={500}>
          {t('integration.labelsToMatch')}
        </Text>
        <Button
          size="compact-sm"
          variant="light"
          leftSection={<IconPlus size={14} />}
          onClick={() => onChange({ ...labels, '': '' })}
        >
          {t('integration.addLabel')}
        </Button>
      </Group>
      {entries.length === 0 && (
        <Text size="xs" c="dimmed">
          {t('integration.labelsRequired')}
        </Text>
      )}
      {entries.map(([key, value], index) => (
        <Group key={index} gap="xs" wrap="nowrap">
          <TextInput
            placeholder={t('integration.label')}
            value={key}
            onChange={(event) => setEntry(index, event.currentTarget.value, value)}
            w={220}
          />
          <TextInput
            placeholder={t('integration.value')}
            value={value}
            onChange={(event) => setEntry(index, key, event.currentTarget.value)}
            style={{ flex: 1 }}
          />
          <ActionIcon
            color="red"
            variant="subtle"
            onClick={() => onChange(Object.fromEntries(entries.filter((_, i) => i !== index)))}
            aria-label={t('integration.removeLabel')}
          >
            <IconTrash size={16} />
          </ActionIcon>
        </Group>
      ))}
    </Stack>
  );
}

function PolicyTab({ integration }: { integration: Integration }) {
  const update = useUpdate('integrations');
  const users = useAllOf('users');
  const [policy, setPolicy] = useState<NotificationPolicy>(
    integration.notification_policy ?? {
      channels: [],
      batch_timeout_seconds: 0,
      batch_deadline_seconds: 0,
      emergency_user_id: null,
      epic_user_id: null,
      epic_threshold_count: 0,
      epic_threshold_seconds: 0,
    },
  );
  const { t } = useI18n();
  const channelLabel = useChannelLabel();

  useEffect(() => {
    if (integration.notification_policy) setPolicy(integration.notification_policy);
  }, [integration]);

  const userOptions = (users.data ?? []).map((user) => ({ value: user.id, label: user.name }));

  return (
    <Paper withBorder p="lg">
      <Stack>
        <MultiSelect
          label={t('integration.deliveryChannels')}
          description={t('integration.deliveryChannelsDescription')}
          data={NOTIFICATION_TARGET_TYPES.map((value) => ({ value, label: channelLabel(value) }))}
          value={policy.channels ?? []}
          onChange={(value) =>
            setPolicy({ ...policy, channels: value as NotificationTargetType[] })
          }
        />
        <SimpleGrid cols={{ base: 1, sm: 2 }}>
          <NumberInput
            label={t('integration.batchTimeout')}
            description={t('integration.batchDisabled')}
            min={0}
            value={policy.batch_timeout_seconds}
            onChange={(value) => setPolicy({ ...policy, batch_timeout_seconds: Number(value) || 0 })}
          />
          <NumberInput
            label={t('integration.batchDeadline')}
            min={0}
            value={policy.batch_deadline_seconds}
            onChange={(value) =>
              setPolicy({ ...policy, batch_deadline_seconds: Number(value) || 0 })
            }
          />
          <Select
            label={t('integration.emergencyUser')}
            placeholder={t('common.none')}
            clearable
            searchable
            data={userOptions}
            value={policy.emergency_user_id}
            onChange={(value) => setPolicy({ ...policy, emergency_user_id: value })}
          />
          <Select
            label={t('integration.epicUser')}
            placeholder={t('common.none')}
            clearable
            searchable
            data={userOptions}
            value={policy.epic_user_id}
            onChange={(value) => setPolicy({ ...policy, epic_user_id: value })}
          />
          <NumberInput
            label={t('integration.epicCount')}
            min={0}
            value={policy.epic_threshold_count}
            onChange={(value) => setPolicy({ ...policy, epic_threshold_count: Number(value) || 0 })}
          />
          <NumberInput
            label={t('integration.epicSeconds')}
            min={0}
            value={policy.epic_threshold_seconds}
            onChange={(value) =>
              setPolicy({ ...policy, epic_threshold_seconds: Number(value) || 0 })
            }
          />
        </SimpleGrid>
        <Group justify="flex-end">
          <Button
            leftSection={<IconDeviceFloppy size={16} />}
            loading={update.isPending}
            disabled={Boolean(integration.provisioned_by)}
            onClick={() =>
              update.mutate({ id: integration.id, body: { notification_policy: policy } })
            }
          >
            {t('integration.savePolicy')}
          </Button>
        </Group>
      </Stack>
    </Paper>
  );
}

function TemplatesTab({ integration }: { integration: Integration }) {
  const update = useUpdate('integrations');
  const [templates, setTemplates] = useState<Record<string, string>>(integration.templates ?? {});
  const { t } = useI18n();

  useEffect(() => setTemplates(integration.templates ?? {}), [integration]);

  const entries = Object.entries(templates);
  const rename = (index: number, name: string) =>
    setTemplates(
      Object.fromEntries(entries.map((entry, i) => (i === index ? [name, entry[1]] : entry))),
    );

  return (
    <Paper withBorder p="lg">
      <Stack>
        <Group justify="space-between">
          <Text size="sm" c="dimmed">
            {t('integration.templatesHelp')}
          </Text>
          <Button
            variant="light"
            leftSection={<IconPlus size={16} />}
            onClick={() => setTemplates({ ...templates, '': '' })}
          >
            {t('integration.addTemplate')}
          </Button>
        </Group>
        {entries.length === 0 && (
          <Text size="sm" c="dimmed">
            {t('integration.noTemplates')}
          </Text>
        )}
        {entries.map(([name, body], index) => (
          <Card withBorder key={index} p="md">
            <Stack gap="xs">
              <Group>
                <TextInput
                  placeholder={t('integration.templateChannel')}
                  value={name}
                  onChange={(event) => rename(index, event.currentTarget.value)}
                  w={260}
                />
                <ActionIcon
                  color="red"
                  variant="subtle"
                  ml="auto"
                  onClick={() =>
                    setTemplates(Object.fromEntries(entries.filter((_, i) => i !== index)))
                  }
                  aria-label={t('integration.removeTemplate')}
                >
                  <IconTrash size={16} />
                </ActionIcon>
              </Group>
              <Textarea
                autosize
                minRows={3}
                value={body}
                onChange={(event) =>
                  setTemplates({ ...templates, [name]: event.currentTarget.value })
                }
              />
            </Stack>
          </Card>
        ))}
        <Group justify="flex-end">
          <Button
            leftSection={<IconDeviceFloppy size={16} />}
            loading={update.isPending}
            disabled={Boolean(integration.provisioned_by)}
            onClick={() => update.mutate({ id: integration.id, body: { templates } })}
          >
            {t('integration.saveTemplates')}
          </Button>
        </Group>
      </Stack>
    </Paper>
  );
}

const SAMPLE_PAYLOAD = JSON.stringify(
  {
    title: 'Disk almost full',
    severity: 'critical',
    labels: { alertname: 'DiskFull', service: 'api' },
  },
  null,
  2,
);

function DebugTab({ integration }: { integration: Integration }) {
  const { t } = useI18n();
  const debug = useDebugRoute();
  const [payload, setPayload] = useState(SAMPLE_PAYLOAD);
  const [parseError, setParseError] = useState<string | null>(null);

  const run = () => {
    try {
      const parsed = JSON.parse(payload);
      setParseError(null);
      debug.mutate({ key: integration.key, payload: parsed });
    } catch (error) {
      setParseError(error instanceof Error ? error.message : String(error));
    }
  };

  return (
    <Stack gap="md">
      <Paper withBorder p="lg">
        <Stack>
          <Text size="sm" c="dimmed">
            {t('integration.debugHelp')}
          </Text>
          <JsonInput
            label={t('integration.payload')}
            autosize
            minRows={10}
            formatOnBlur
            value={payload}
            onChange={setPayload}
            error={parseError}
          />
          <Group justify="flex-end">
            <Button onClick={run} loading={debug.isPending}>
              {t('integration.evaluate')}
            </Button>
          </Group>
        </Stack>
      </Paper>
      {debug.data && (
        <Paper withBorder p="lg">
          <Title order={5} mb="sm">
            {t('integration.result')}
          </Title>
          <JsonBlock value={debug.data} />
        </Paper>
      )}
    </Stack>
  );
}
