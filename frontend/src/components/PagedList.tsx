import { Group, Pagination, Text, TextInput } from '@mantine/core';
import { IconSearch } from '@tabler/icons-react';
import { useEffect, useMemo, useState } from 'react';
import { useDebouncedValue } from '@mantine/hooks';
import { useList, type ListResource, type ResourceType } from '../api/hooks';
import { useI18n } from '../i18n/I18nProvider';

// Rows a list page draws at once.
export const LIST_PAGE_SIZE = 50;

export interface PagedList<T> {
  server?: boolean;
  estimated?: boolean;
  /** The rows to draw: this page of the items that match the search. */
  items: T[];
  /** Every item, and how many match the search. */
  total: number;
  matching: number;
  page: number;
  pages: number;
  setPage: (page: number) => void;
  query: string;
  setQuery: (query: string) => void;
}

/**
 * A whole collection shown a page at a time, with a search over `fields`.
 *
 * The list pages read every item (a page of 500 used to hide the rest), and
 * drawing all of them is what made the pages slow: on a sandbox with 624
 * people the Users page took 3–4.6 s to appear and 6 s to answer a duty toggle,
 * against 0.8 s and 0.6 s for 50 rows — the API answered in 0.05 s either way.
 */
export function usePagedList<T>(items: T[] | undefined, fields: (keyof T)[]): PagedList<T> {
  const [page, setPageState] = useState(1);
  const [query, setQueryState] = useState('');
  const matched = useMemo(() => {
    const all = items ?? [];
    const q = query.trim().toLowerCase();
    if (!q) return all;
    return all.filter((item) => fields.some((field) => String(item[field] ?? '').toLowerCase().includes(q)));
    // fields is a literal at every call site, so it is left out of the deps.
  }, [items, query]);
  const pages = Math.max(1, Math.ceil(matched.length / LIST_PAGE_SIZE));
  const current = Math.min(page, pages);
  return {
    items: matched.slice((current - 1) * LIST_PAGE_SIZE, current * LIST_PAGE_SIZE),
    total: items?.length ?? 0,
    matching: matched.length,
    page: current,
    pages,
    setPage: setPageState,
    query,
    setQuery: (next) => {
      setQueryState(next);
      setPageState(1);
    },
  };
}

/** The search above a list; only once the list is longer than one page. */
export function ListSearch<T>({ list }: { list: PagedList<T> }) {
  const { t } = useI18n();
  if (!list.server && list.total <= LIST_PAGE_SIZE) return null;
  return (
    <Group justify="space-between" mb="sm">
      <TextInput
        leftSection={<IconSearch size={14} />}
        placeholder={t('list.search')}
        aria-label={t('list.search')}
        value={list.query}
        onChange={(event) => list.setQuery(event.currentTarget.value)}
        w={{ base: '100%', sm: 320 }}
      />
      <Text size="sm" c="dimmed">
        {list.estimated ? '≈ ' : ''}
        {list.query && !list.server ? t('list.matching', { matching: list.matching, total: list.total }) : t('list.total', { total: list.total })}
      </Text>
    </Group>
  );
}

/** The pages below a list, and the word when nothing matches. */
export function ListPager<T>({ list }: { list: PagedList<T> }) {
  const { t } = useI18n();
  if (list.query && list.matching === 0) {
    return (
      <Text size="sm" c="dimmed" ta="center" py="md">
        {t('list.noMatches', { query: list.query })}
      </Text>
    );
  }
  if (list.pages <= 1) return null;
  return (
    <Group justify="center" mt="md">
      <Pagination value={list.page} onChange={list.setPage} total={list.pages} />
    </Group>
  );
}


/** Reference tables request only the current server page; search covers all rows. */
export function useReferencePage<R extends ListResource>(resource: R) {
  const [page, setPage] = useState(1);
  const [query, setSearch] = useState('');
  const [search] = useDebouncedValue(query.trim(), 250);
  const result = useList(resource, { limit: LIST_PAGE_SIZE, offset: (page - 1) * LIST_PAGE_SIZE, q: search || undefined });
  const data = result.data;
  useEffect(() => {
    // The last item on a page may have been deleted by a mutation.
    if (data && !data.items.length && page > 1) setPage(value => value - 1);
  }, [data, page]);
  const rows: PagedList<ResourceType[R]> = {
    server: true, estimated: data?.total_estimated,
    items: data?.items ?? [], total: data?.total ?? 0, matching: data?.total ?? 0,
    page, pages: Math.max(1, Math.ceil((data?.total ?? 0) / LIST_PAGE_SIZE), data?.total_estimated && data.items.length === LIST_PAGE_SIZE ? page + 1 : page),
    setPage, query, setQuery: value => { setSearch(value); setPage(1); },
  };
  return { ...result, rows };
}
