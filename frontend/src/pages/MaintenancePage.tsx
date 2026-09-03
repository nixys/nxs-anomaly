import { useState } from 'react';
import {
  Badge,
  Button,
  Group,
  Modal,
  MultiSelect,
  Paper,
  Stack,
  Table,
  Text,
  TextInput,
  Textarea,
} from '@mantine/core';
import { IconPlus } from '@tabler/icons-react';
import { useAllOf, useCreate, useDelete, useList } from '../api/hooks';
import type { MaintenanceWindow } from '../api/types';
import { AbsoluteTime, ConfirmDeleteButton, PageHeader, ProvisionedBadge, QueryState } from '../components/common';
import { useI18n } from '../i18n/I18nProvider';

/**
 * Planned work. During a window the covered integrations' new alert groups are
 * recorded and silenced rather than paged, and the dead-man switch stops
 * reporting those sources as silent.
 */
export function MaintenancePage() {
  const { t } = useI18n();
  const windows = useList('maintenance-windows', { limit: 500 });
  const remove = useDelete('maintenance-windows');
  const integrations = useAllOf('integrations');
  const [creating, setCreating] = useState(false);

  const integrationName = new Map(
    (integrations.data ?? []).map((integration) => [integration.id, integration.name]),
  );

  return (
    <>
      <PageHeader
        title={t('maintenance.title')}
        description={t('maintenance.description')}
        actions={
          <Button leftSection={<IconPlus size={16} />} onClick={() => setCreating(true)}>
            {t('maintenance.plan')}
          </Button>
        }
      />

      <Paper withBorder>
        <QueryState
          query={windows}
          isEmpty={(data) => data.items.length === 0}
          emptyLabel={t('maintenance.empty')}
        >
          {(data) => (
            <Table.ScrollContainer minWidth={800}>
              <Table highlightOnHover verticalSpacing="sm">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th w={110}>{t('maintenance.state')}</Table.Th>
                    <Table.Th>{t('common.name')}</Table.Th>
                    <Table.Th>{t('maintenance.covers')}</Table.Th>
                    <Table.Th w={200}>{t('maintenance.from')}</Table.Th>
                    <Table.Th w={200}>{t('maintenance.until')}</Table.Th>
                    <Table.Th w={60} />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {data.items.map((window) => (
                    <Table.Tr key={window.id}>
                      <Table.Td>
                        <StateBadge window={window} />
                      </Table.Td>
                      <Table.Td>
                        <Stack gap={2}>
                          <Group gap={6}>
                            <Text size="sm" fw={500}>
                              {window.name}
                            </Text>
                            <ProvisionedBadge by={window.provisioned_by} />
                          </Group>
                          {window.reason && (
                            <Text size="xs" c="dimmed">
                              {window.reason}
                            </Text>
                          )}
                        </Stack>
                      </Table.Td>
                      <Table.Td>
                        {/* Named, never "everything": a window that covered the
                            whole deployment by accident would page nobody, and
                            nobody being paged is not a visible symptom. */}
                        <Group gap={4}>
                          {window.integration_ids.map((id) => (
                            <Badge key={id} variant="light" tt="none">
                              {integrationName.get(id) ?? id}
                            </Badge>
                          ))}
                        </Group>
                      </Table.Td>
                      <Table.Td>
                        <AbsoluteTime value={window.starts_at} />
                      </Table.Td>
                      <Table.Td>
                        <AbsoluteTime value={window.ends_at} />
                      </Table.Td>
                      <Table.Td>
                        <ConfirmDeleteButton
                          label={window.name}
                          loading={remove.isPending}
                          disabled={Boolean(window.provisioned_by)}
                          disabledReason={t('common.provisionedHint', { tool: window.provisioned_by ?? '' })}
                          onConfirm={() => remove.mutate(window.id)}
                        />
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </Table.ScrollContainer>
          )}
        </QueryState>
      </Paper>

      <CreateWindowModal opened={creating} onClose={() => setCreating(false)} />
    </>
  );
}

/**
 * StateBadge says whether a window is suppressing anything right now, which is
 * the only question somebody has when an alert did not arrive.
 */
function StateBadge({ window }: { window: MaintenanceWindow }) {
  const { t } = useI18n();
  const now = Date.now();
  const starts = Date.parse(window.starts_at);
  const ends = Date.parse(window.ends_at);
  if (now >= starts && now < ends) {
    return <Badge color="orange">{t('maintenance.active')}</Badge>;
  }
  if (now < starts) {
    return (
      <Badge variant="light" color="blue">
        {t('maintenance.planned')}
      </Badge>
    );
  }
  return (
    <Badge variant="light" color="gray">
      {t('maintenance.finished')}
    </Badge>
  );
}

function CreateWindowModal({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const create = useCreate('maintenance-windows');
  const integrations = useAllOf('integrations');
  const [name, setName] = useState('');
  const [reason, setReason] = useState('');
  const [integrationIds, setIntegrationIds] = useState<string[]>([]);
  const [startsAt, setStartsAt] = useState('');
  const [endsAt, setEndsAt] = useState('');
  const { t } = useI18n();

  function submit() {
    create.mutate(
      {
        name,
        reason,
        integration_ids: integrationIds,
        // datetime-local has no zone; the browser's own offset is what the
        // person typing meant, and the API stores UTC.
        starts_at: new Date(startsAt).toISOString(),
        ends_at: new Date(endsAt).toISOString(),
      },
      {
        onSuccess: () => {
          setName('');
          setReason('');
          setIntegrationIds([]);
          setStartsAt('');
          setEndsAt('');
          onClose();
        },
      },
    );
  }

  const complete = name && integrationIds.length > 0 && startsAt && endsAt;

  return (
    <Modal opened={opened} onClose={onClose} title={t('maintenance.modalTitle')} size="lg">
      <Stack gap="md">
        <TextInput
          label={t('common.name')}
          placeholder={t('maintenance.namePlaceholder')}
          value={name}
          onChange={(event) => setName(event.currentTarget.value)}
        />
        <Textarea
          label={t('common.reason')}
          placeholder={t('maintenance.reasonPlaceholder')}
          value={reason}
          onChange={(event) => setReason(event.currentTarget.value)}
          autosize
          minRows={2}
        />
        <MultiSelect
          label={t('nav.integrations')}
          description={t('maintenance.sourcesDescription')}
          data={(integrations.data ?? []).map((integration) => ({
            value: integration.id,
            label: integration.name,
          }))}
          value={integrationIds}
          onChange={setIntegrationIds}
          searchable
        />
        <Group grow>
          <TextInput
            label={t('maintenance.from')}
            type="datetime-local"
            value={startsAt}
            onChange={(event) => setStartsAt(event.currentTarget.value)}
          />
          <TextInput
            label={t('maintenance.until')}
            type="datetime-local"
            value={endsAt}
            onChange={(event) => setEndsAt(event.currentTarget.value)}
          />
        </Group>
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button onClick={submit} loading={create.isPending} disabled={!complete}>
            {t('maintenance.plan')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
