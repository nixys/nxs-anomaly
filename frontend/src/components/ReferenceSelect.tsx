import { Loader, MultiSelect, Select, Stack, Text, type MultiSelectProps, type SelectProps, type OptionsFilter } from '@mantine/core';
import { useDebouncedValue } from '@mantine/hooks';
import { useState } from 'react';
import { useList, useReferences } from '../api/hooks';
import { useI18n } from '../i18n/I18nProvider';

type Resource = 'users' | 'teams' | 'schedules' | 'escalation-chains' | 'integrations';

function useOptions(resource: Resource, selected: string[]) {
  const [search, setSearch] = useState('');
  const [debounced] = useDebouncedValue(search.trim(), 250);
  const [opened, setOpened] = useState(false);
  const query = useList(resource, { limit: 50, q: debounced || undefined }, { enabled: opened, staleTime: 30_000 });
  const references = useReferences(resource, selected);
  const options = new Map(selected.map(id => [id, { value: id, label: id }]));
  for (const item of [...(references.data ?? []), ...(query.data?.items ?? [])]) options.set(item.id, { value: item.id, label: item.name || item.id });
  return { search, setSearch, opened, setOpened, query, references, data: [...options.values()] };
}

function ReferenceInput({ resource, multiple, ...props }: (Omit<SelectProps, 'data'> | Omit<MultiSelectProps, 'data'>) & { resource: Resource; multiple: boolean }) {
  const { t } = useI18n();
  const value = props.value;
  const selected = Array.isArray(value) ? value : value ? [value] : [];
  const options = useOptions(resource, selected);
  const failed = options.query.isError || options.references.isError;
  const description = <Stack gap={2}>
    {props.description}
    {failed && <Text size="xs" c="red" role="alert">{t('common.loadFailed')}</Text>}
    {options.opened && (options.query.data?.total ?? 0) > 50 && <Text size="xs" c="dimmed">{t('reference.refine')}</Text>}
  </Stack>;
  const shared = {
    ...props, data: options.data, searchable: true, description,
    searchValue: options.search, onSearchChange: options.setSearch,
    filter: (({ options: items }) => items) satisfies OptionsFilter,
    nothingFoundMessage: failed ? t('common.loadFailed') : options.query.isFetching ? t('common.loading') : t('common.noData'),
    rightSection: options.query.isFetching ? <Loader size={14} /> : props.rightSection,
    onDropdownOpen: () => { options.setOpened(true); props.onDropdownOpen?.(); },
    onDropdownClose: () => { options.setOpened(false); options.setSearch(''); props.onDropdownClose?.(); },
  };
  return multiple ? <MultiSelect {...shared as MultiSelectProps} /> : <Select {...shared as SelectProps} />;
}
export function ReferenceSelect(props: Omit<SelectProps, 'data'> & { resource: Resource }) {
  return <ReferenceInput {...props} multiple={false} />;
}
export function ReferenceMultiSelect(props: Omit<MultiSelectProps, 'data'> & { resource: Resource }) {
  return <ReferenceInput {...props} multiple />;
}
