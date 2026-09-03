import { useState } from 'react';
import {
  ActionIcon,
  Badge,
  Button,
  CopyButton,
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
import { IconCheck, IconCopy, IconPlus, IconSettings } from '@tabler/icons-react';
import { Link } from 'react-router-dom';
import { useAllOf, useCreate, useDelete, useList } from '../api/hooks';
import { INTEGRATION_TYPES, type Integration } from '../api/types';
import { ConfirmDeleteButton, PageHeader, ProvisionedBadge, QueryState } from '../components/common';
import { useI18n } from '../i18n/I18nProvider';
import { EMPTY_VALUE } from '../i18n/format';

/** Ingestion endpoint for an integration, matching registerRoutes in internal/server. */
export function ingestUrl(integration: Integration): string {
  const type = INTEGRATION_TYPES.includes(integration.type as (typeof INTEGRATION_TYPES)[number])
    ? integration.type
    : 'webhook';
  return `${window.location.origin}/integrations/v1/${type}/${integration.key}`;
}

export function CopyField({ value }: { value: string }) {
  const { t } = useI18n();
  return (
    <Group gap={4} wrap="nowrap">
      <Text size="xs" ff="monospace" style={{ wordBreak: 'break-all' }}>
        {value}
      </Text>
      <CopyButton value={value} timeout={1500}>
        {({ copied, copy }) => (
          <Tooltip label={copied ? t('common.copied') : t('common.copy')} withArrow>
            <ActionIcon variant="subtle" onClick={copy} aria-label={t('common.copy')}>
              {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
            </ActionIcon>
          </Tooltip>
        )}
      </CopyButton>
    </Group>
  );
}

export function IntegrationsPage() {
  const { t } = useI18n();
  const integrations = useList('integrations', { limit: 500 });
  const remove = useDelete('integrations');
  const [creating, setCreating] = useState(false);

  return (
    <>
      <PageHeader
        title={t('integrations.title')}
        description={t('integrations.description')}
        actions={
          <Button leftSection={<IconPlus size={16} />} onClick={() => setCreating(true)}>
            {t('integrations.add')}
          </Button>
        }
      />

      <Paper withBorder>
        <QueryState
          query={integrations}
          isEmpty={(data) => data.items.length === 0}
          emptyLabel={t('integrations.empty')}

        >
          {(data) => (
            <Table.ScrollContainer minWidth={900}>
              <Table highlightOnHover verticalSpacing="sm">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t('common.name')}</Table.Th>
                    <Table.Th w={140}>{t('common.type')}</Table.Th>
                    <Table.Th>{t('integrations.ingestionUrl')}</Table.Th>
                    <Table.Th w={100}>{t('integrations.routes')}</Table.Th>
                    <Table.Th w={90} />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {data.items.map((integration) => (
                    <Table.Tr key={integration.id}>
                      <Table.Td>
                        <Stack gap={2}>
                          <Text
                            component={Link}
                            to={`/integrations/${integration.id}`}
                            size="sm"
                            fw={500}
                          >
                            {integration.name}
                          </Text>
                          <ProvisionedBadge by={integration.provisioned_by} />
                          <Text size="xs" c="dimmed">
                            {t('integrations.groupByShort', { labels: (integration.group_by ?? []).join(', ') || EMPTY_VALUE })}
                          </Text>
                        </Stack>
                      </Table.Td>
                      <Table.Td>
                        <Badge variant="light" tt="none">
                          {integration.type}
                        </Badge>
                      </Table.Td>
                      <Table.Td>
                        <CopyField value={ingestUrl(integration)} />
                      </Table.Td>
                      <Table.Td>
                        <Text size="sm">{(integration.routes ?? []).length}</Text>
                      </Table.Td>
                      <Table.Td>
                        <Group gap={4} wrap="nowrap">
                          <Tooltip label={t('integrations.configure')} withArrow>
                            <ActionIcon
                              component={Link}
                              to={`/integrations/${integration.id}`}
                              variant="subtle"
                              aria-label={t('integrations.configureName', { name: integration.name })}
                            >
                              <IconSettings size={16} />
                            </ActionIcon>
                          </Tooltip>
                          <ConfirmDeleteButton
                            label={integration.name}
                            loading={remove.isPending}
                            disabled={Boolean(integration.provisioned_by)}
                            disabledReason={t('common.provisionedHint', { tool: integration.provisioned_by ?? '' })}
                            onConfirm={() => remove.mutate(integration.id)}
                          />
                        </Group>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          )}
        </QueryState>
      </Paper>

      <CreateIntegrationModal opened={creating} onClose={() => setCreating(false)} />
    </>
  );
}

function CreateIntegrationModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const create = useCreate('integrations');
  const chains = useAllOf('escalation-chains');
  const teams = useAllOf('teams');
  const [name, setName] = useState('');
  const [type, setType] = useState<string>('webhook');
  const [groupBy, setGroupBy] = useState('alertname, service');
  const [chainId, setChainId] = useState<string | null>(null);
  const [teamId, setTeamId] = useState<string | null>(null);
  const { t } = useI18n();

  const submit = () => {
    create.mutate(
      {
        name,
        type,
        source_type: type,
        group_by: groupBy
          .split(',')
          .map((part) => part.trim())
          .filter(Boolean),
        team_id: teamId ?? '',
        // Omitting "routes" makes the backend create a single default route
        // pointing at this chain.
        default_chain_id: chainId ?? '',
      },
      {
        onSuccess: () => {
          setName('');
          onClose();
        },
      },
    );
  };

  return (
    <Modal opened={opened} onClose={onClose} title={t('integrations.add')} centered>
      <Stack>
        <TextInput
          label={t('common.name')}
          required
          value={name}
          onChange={(event) => setName(event.currentTarget.value)}
        />
        <Select
          label={t('common.type')}
          data={[...INTEGRATION_TYPES]}
          value={type}
          onChange={(value) => setType(value ?? 'webhook')}
          allowDeselect={false}
        />
        <TextInput
          label={t('integrations.groupBy')}
          description={t('integrations.groupByDescription')}
          value={groupBy}
          onChange={(event) => setGroupBy(event.currentTarget.value)}
        />
        <Select
          label={t('integrations.defaultChain')}
          placeholder={t('common.none')}
          clearable
          searchable
          data={(chains.data ?? []).map((chain) => ({ value: chain.id, label: chain.name }))}
          value={chainId}
          onChange={setChainId}
        />
        <Select
          label={t('integrations.owningTeam')}
          description={t('integrations.owningTeamDescription')}
          placeholder={t('integrations.unassigned')}
          clearable
          searchable
          data={(teams.data ?? []).map((team) => ({ value: team.id, label: team.name as string }))}
          value={teamId}
          onChange={setTeamId}
        />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={submit} loading={create.isPending} disabled={!name.trim()}>
            {t('common.create')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
