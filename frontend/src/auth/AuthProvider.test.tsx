import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, renderHook, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, authApi, getApiKey, setApiKey } from '../api/client';
import { AuthProvider, useAuth } from './AuthProvider';

// An outage of /auth/me used to land on the sign-in screen and wipe the stored
// API key, so the person paged at night had to find the key again once the API
// came back. Only a 401 means the credential is wrong.

let queryClient: QueryClient;

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}><AuthProvider>{children}</AuthProvider></QueryClientProvider>;
}

beforeEach(() => {
  queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.restoreAllMocks();
  localStorage.clear();
  vi.spyOn(authApi, 'methods').mockResolvedValue({
    password: true,
    api_key: true,
    anonymous: false,
    oidc: false,
    sso_available: false,
  } as Awaited<ReturnType<typeof authApi.methods>>);
});

describe('resolving who is signed in', () => {
  it('keeps the stored key and offers a retry when the API is unavailable', async () => {
    setApiKey('stored-key');
    const me = vi.spyOn(authApi, 'me').mockRejectedValue(new ApiError(503, 'unavailable'));
    const { result } = renderHook(() => useAuth(), { wrapper });
    await waitFor(() => expect(result.current.state).toBe('unavailable'));
    expect(getApiKey()).toBe('stored-key');

    me.mockResolvedValue({ role: 'admin' } as Awaited<ReturnType<typeof authApi.me>>);
    await act(() => result.current.retry());
    expect(result.current.state).toBe('authenticated');
  });

  it('treats a 401 as signed out and drops the stale key', async () => {
    setApiKey('stale-key');
    vi.spyOn(authApi, 'me').mockRejectedValue(new ApiError(401, 'unauthorized'));
    const { result } = renderHook(() => useAuth(), { wrapper });
    await waitFor(() => expect(result.current.state).toBe('unauthenticated'));
    expect(getApiKey()).toBeNull();
  });
});


describe('session cache isolation', () => {
  it('clears cached data on sign-out even if logout fails', async () => {
    vi.spyOn(authApi, 'me').mockResolvedValue({ role: 'admin' } as Awaited<ReturnType<typeof authApi.me>>);
    vi.spyOn(authApi, 'logout').mockRejectedValue(new Error('offline'));
    const { result } = renderHook(() => useAuth(), { wrapper });
    await waitFor(() => expect(result.current.state).toBe('authenticated'));
    queryClient.setQueryData(['users', 'all'], [{ id: 'previous-user' }]);
    await act(() => result.current.signOut());
    expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
    expect(result.current.state).toBe('unauthenticated');
  });

  it('clears stale data before a new password session becomes visible', async () => {
    vi.spyOn(authApi, 'me').mockResolvedValue({ role: 'viewer' } as Awaited<ReturnType<typeof authApi.me>>);
    vi.spyOn(authApi, 'login').mockResolvedValue(undefined as never);
    const { result } = renderHook(() => useAuth(), { wrapper });
    await waitFor(() => expect(result.current.state).toBe('authenticated'));
    queryClient.setQueryData(['alert-groups'], { items: [{ id: 'private-group' }] });
    await act(() => result.current.signInWithPassword('next', 'password'));
    expect(queryClient.getQueryData(['alert-groups'])).toBeUndefined();
  });

  it('keeps cached data on a temporary outage, but clears it on a 401', async () => {
    const me = vi.spyOn(authApi, 'me').mockRejectedValue(new ApiError(503, 'offline'));
    queryClient.setQueryData(['users', 'all'], [{ id: 'cached' }]);
    const { result } = renderHook(() => useAuth(), { wrapper });
    await waitFor(() => expect(result.current.state).toBe('unavailable'));
    expect(queryClient.getQueryData(['users', 'all'])).toBeDefined();
    me.mockRejectedValue(new ApiError(401, 'expired'));
    await act(() => result.current.retry());
    expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
  });
});
