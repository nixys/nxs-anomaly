import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useAllOf, useFullList } from './hooks';

const get = vi.fn();

vi.mock('./client', () => ({
  api: { get: (...args: unknown[]) => get(...args), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() },
  ApiError: class ApiError extends Error {},
  getApiKey: () => 'test-key',
}));

vi.mock('@mantine/notifications', () => ({ notifications: { show: vi.fn(), hide: vi.fn() } }));

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

// 1001 people: one full page of the API's largest size and one more.
function twoPages() {
  const first = Array.from({ length: 1000 }, (_, i) => ({ id: `p${i}` }));
  get.mockImplementation((_path: string, params?: { offset?: number }) =>
    Promise.resolve((params?.offset ?? 0) === 0 ? { items: first, total: 1001 } : { items: [{ id: 'latecomer' }], total: 1001 }),
  );
}

beforeEach(() => vi.clearAllMocks());

// A sandbox with 574 people showed 500 on the Users page and nothing said
// anyone was missing; the selects stopped at the API's page of 1000.
describe('reading a whole collection', () => {
  it('useFullList reads page after page into one list', async () => {
    twoPages();
    const { result } = renderHook(() => useFullList('users'), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.data!.items).toHaveLength(1001);
    expect(result.current.data!.total).toBe(1001);
    expect(get).toHaveBeenCalledWith('/api/v1/users', { limit: 1000, offset: 1000 }, expect.any(AbortSignal));
  });

  it('useAllOf, which feeds the selects, reads past the first 1000 too', async () => {
    twoPages();
    const { result } = renderHook(() => useAllOf('users'), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.data!.map((u) => u.id)).toContain('latecomer');
  });

  // Past 10 000 rows the API answers total=10001 as a lower bound; stopping
  // there loaded 11 000 of 12 000.
  it('reads past an estimated total', async () => {
    get.mockImplementation((_path: string, params?: { offset?: number }) => {
      const offset = params?.offset ?? 0;
      const items = Array.from({ length: Math.max(0, Math.min(1000, 12000 - offset)) }, (_, i) => ({ id: `p${offset + i}` }));
      return Promise.resolve({ items, total: 10001, total_estimated: true, total_lower_bound: true });
    });
    const { result } = renderHook(() => useAllOf('users'), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.data).toHaveLength(12000);
  });

  it('stops after a short page', async () => {
    get.mockResolvedValue({ items: [{ id: 'a' }, { id: 'b' }], total: 2 });
    const { result } = renderHook(() => useFullList('users'), { wrapper });
    await waitFor(() => expect(result.current.data).toBeDefined());
    expect(result.current.data!.items).toHaveLength(2);
    expect(get).toHaveBeenCalledTimes(1);
  });
});


it('shares reference requests between a list and its selects', async () => {
  twoPages();
  const { result } = renderHook(() => ({ list: useFullList('users'), options: useAllOf('users') }), { wrapper });
  await waitFor(() => expect(result.current.list.data?.items).toHaveLength(1001));
  expect(result.current.options.data).toBe(result.current.list.data?.items);
  expect(get).toHaveBeenCalledTimes(2);
});

it('continues full pages when the server omits total', async () => {
  get.mockResolvedValueOnce({ items: Array.from({ length: 1000 }, (_, i) => ({ id: String(i) })) });
  get.mockResolvedValueOnce({ items: [{ id: 'last' }] });
  const { result } = renderHook(() => useAllOf('users'), { wrapper });
  await waitFor(() => expect(result.current.data).toHaveLength(1001));
});

it('aborts an abandoned collection read and never starts its next page', async () => {
  let finish!: (page: unknown) => void;
  let signal!: AbortSignal;
  get.mockImplementation((_path, _params, requestSignal) => {
    signal = requestSignal;
    return new Promise(resolve => { finish = resolve; });
  });
  const { unmount } = renderHook(() => useAllOf('users'), { wrapper });
  await waitFor(() => expect(get).toHaveBeenCalledTimes(1));
  unmount();
  expect(signal.aborted).toBe(true);
  finish({ items: Array.from({ length: 1000 }, (_, i) => ({ id: String(i) })), total: 2000 });
  await new Promise(resolve => setTimeout(resolve, 0));
  expect(get).toHaveBeenCalledTimes(1);
});
