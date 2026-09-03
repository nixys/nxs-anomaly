import { useState } from 'react';
import {
  ActionIcon,
  Avatar,
  Button,
  Group,
  Modal,
  MultiSelect,
  Paper,
  Stack,
  Table,
  Text,
  TextInput,
  Tooltip,
} from '@mantine/core';
import { IconPencil, IconUsersPlus } from '@tabler/icons-react';
import { useAllOf, useCreate, useDelete, useList, useUpdate } from '../api/hooks';
import type { Team } from '../api/types';
import { ConfirmDeleteButton, PageHeader, ProvisionedBadge, QueryState } from '../components/common';
import { EMPTY_VALUE } from '../i18n/format';
import { useI18n } from '../i18n/I18nProvider';

export function TeamsPage() {
  const { t } = useI18n();
  const teams = useList('teams', { limit: 500 });
  const users = useAllOf('users');
  const remove = useDelete('teams');
  const [editing, setEditing] = useState<Team | null>(null);
  const [creating, setCreating] = useState(false);

  const userName = new Map((users.data ?? []).map((user) => [user.id, user.name]));

  return (
    <>
      <PageHeader
        title={t('teams.title')}
        description={t('teams.description')}
        actions={
          <Button leftSection={<IconUsersPlus size={16} />} onClick={() => setCreating(true)}>
            {t('teams.add')}
          </Button>
        }
      />

      <Paper withBorder>
        <QueryState
          query={teams}
          isEmpty={(data) => data.items.length === 0}
          emptyLabel={t('teams.empty')}
        >
          {(data) => (
            <Table highlightOnHover verticalSpacing="sm">
              <Table.Thead>
                <Table.Tr>
                  <Table.Th>{t('common.name')}</Table.Th>
                  <Table.Th>{t('teams.members')}</Table.Th>
                  <Table.Th w={90} />
                </Table.Tr>
              </Table.Thead>
              <Table.Tbody>
                {data.items.map((team) => (
                  <Table.Tr key={team.id}>
                    <Table.Td>
                      <Group gap={6}>
                        <Text size="sm" fw={500}>
                          {team.name}
                        </Text>
                        <ProvisionedBadge by={team.provisioned_by} />
                      </Group>
                    </Table.Td>
                    <Table.Td>
                      <Group gap={6}>
                        {(team.member_ids ?? []).length === 0 && <Text c="dimmed">{EMPTY_VALUE}</Text>}
                        {(team.member_ids ?? []).map((memberId) => (
                          <Tooltip key={memberId} label={userName.get(memberId) ?? memberId} withArrow>
                            <Avatar size="sm" radius="xl" color="blue">
                              {(userName.get(memberId) ?? '?').slice(0, 2).toUpperCase()}
                            </Avatar>
                          </Tooltip>
                        ))}
                      </Group>
                    </Table.Td>
                    <Table.Td>
                      <Group gap={4} wrap="nowrap">
                        <ActionIcon
                          variant="subtle"
                          onClick={() => setEditing(team)}
                          disabled={Boolean(team.provisioned_by)}
                          aria-label={t('teams.edit', { name: team.name })}
                        >
                          <IconPencil size={16} />
                        </ActionIcon>
                        <ConfirmDeleteButton
                          label={team.name}
                          loading={remove.isPending}
                          disabled={Boolean(team.provisioned_by)}
                          disabledReason={t('common.provisionedHint', { tool: team.provisioned_by ?? '' })}
                          onConfirm={() => remove.mutate(team.id)}
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

      <TeamModal opened={creating} onClose={() => setCreating(false)} />
      <TeamModal opened={editing !== null} team={editing} onClose={() => setEditing(null)} />
    </>
  );
}

function TeamModal({
  opened,
  team,
  onClose,
}: {
  opened: boolean;
  team?: Team | null;
  onClose: () => void;
}) {
  const users = useAllOf('users');
  const create = useCreate('teams');
  const update = useUpdate('teams');
  const { t } = useI18n();

  const [name, setName] = useState(team?.name ?? '');
  const [memberIds, setMemberIds] = useState<string[]>(team?.member_ids ?? []);
  const [key, setKey] = useState(team?.id ?? 'new');

  if (opened && (team?.id ?? 'new') !== key) {
    setKey(team?.id ?? 'new');
    setName(team?.name ?? '');
    setMemberIds(team?.member_ids ?? []);
  }

  const submit = () => {
    const body = { name, member_ids: memberIds };
    const options = { onSuccess: onClose };
    if (team) update.mutate({ id: team.id, body }, options);
    else create.mutate(body, options);
  };

  return (
    <Modal
      opened={opened}
      onClose={onClose}
      title={team ? t('teams.edit', { name: team.name }) : t('teams.add')}
      centered
    >
      <Stack>
        <TextInput
          label={t('common.name')}
          required
          value={name}
          onChange={(event) => setName(event.currentTarget.value)}
        />
        <MultiSelect
          label={t('teams.members')}
          searchable
          data={(users.data ?? []).map((user) => ({ value: user.id, label: user.name }))}
          value={memberIds}
          onChange={setMemberIds}
        />
        <Group justify="flex-end">
          <Button variant="default" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button
            onClick={submit}
            loading={create.isPending || update.isPending}
            disabled={!name.trim()}
          >
            {team ? t('common.save') : t('common.create')}
          </Button>
        </Group>
      </Stack>
    </Modal>
  );
}
