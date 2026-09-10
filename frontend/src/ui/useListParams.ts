import { useCallback, useMemo } from 'react';
import { useSearchParams } from 'react-router-dom';

/**
 * Filters, sorting and paging of a list, kept in the address bar.
 *
 * A filtered list is the thing people hand over mid-incident — "look at these
 * three" — and it can only be handed over if it is a link. Keeping it in the
 * address also survives a reload and the browser's back button, which component
 * state does not.
 *
 * The list page owns the meaning of each key; this hook only owns the mechanics,
 * so every list behaves the same way: an empty value drops the key rather than
 * writing `?status=`, and changing a filter returns to the first page, because
 * page 7 of a different query is a different list.
 */
export function useListParams(defaults: Record<string, string> = {}) {
  const [params, setParams] = useSearchParams();

  const get = useCallback(
    (key: string) => params.get(key) ?? defaults[key] ?? null,
    [params, defaults],
  );

  const patch = useCallback(
    (next: Record<string, string | null>, options: { keepPage?: boolean } = {}) => {
      setParams(
        (prev) => {
          const updated = new URLSearchParams(prev);
          for (const [key, value] of Object.entries(next)) {
            if (value === null || value === '') updated.delete(key);
            else updated.set(key, value);
          }
          if (!options.keepPage) updated.delete('page');
          return updated;
        },
        // replace: a filter change is not a place to come back to with Back;
        // the page the person came from is.
        { replace: true },
      );
    },
    [setParams],
  );

  const page = Math.max(1, Number(get('page') ?? 1) || 1);
  const clear = useCallback(() => setParams(new URLSearchParams(), { replace: true }), [setParams]);

  /** True when anything narrows the list — what separates "nothing matched" from "nothing exists". */
  const filtered = useMemo(() => {
    for (const [key, value] of params.entries()) {
      if (key !== 'page' && key !== 'sort' && key !== 'order' && key !== 'tab' && value !== '') {
        return true;
      }
    }
    return false;
  }, [params]);

  return { params, get, patch, page, clear, filtered };
}
