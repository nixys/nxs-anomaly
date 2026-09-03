import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import {
  authApi,
  clearApiKey,
  getApiKey,
  setApiKey,
  verifyApiKey,
  type AuthMethods,
  type Identity,
} from '../api/client';

type AuthState = 'checking' | 'authenticated' | 'unauthenticated';

interface AuthContextValue {
  state: AuthState;
  /** True when the backend accepts unauthenticated requests. */
  authDisabled: boolean;
  /** Which sign-in methods this deployment offers; null until known. */
  methods: AuthMethods | null;
  /** Who the backend thinks we are; null when not signed in (or not yet known). */
  identity: Identity | null;
  signInWithPassword: (login: string, password: string) => Promise<void>;
  signInWithApiKey: (key: string) => Promise<void>;
  signOut: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>('checking');
  const [authDisabled, setAuthDisabled] = useState(false);
  const [methods, setMethods] = useState<AuthMethods | null>(null);
  const [identity, setIdentity] = useState<Identity | null>(null);

  // Resolving identity is a single call: /auth/me answers "am I signed in, and
  // as whom" for a session cookie, a stored API key and an anonymous
  // deployment alike, so there is no need to probe each case separately.
  const resolve = useCallback(async (): Promise<boolean> => {
    try {
      const me = await authApi.me();
      setIdentity(me);
      setState('authenticated');
      return true;
    } catch {
      setIdentity(null);
      setState('unauthenticated');
      return false;
    }
  }, []);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      let available: AuthMethods | null = null;
      try {
        available = await authApi.methods();
      } catch {
        // The API is unreachable. Fall through: /auth/me will fail too, and the
        // sign-in screen degrades to offering both methods.
      }
      if (cancelled) return;
      setMethods(available);
      setAuthDisabled(available?.anonymous ?? false);
      if (!(await resolve()) && !cancelled) {
        // A stored key that no longer works is worse than no key: it makes
        // every request fail in a way that looks like a server problem.
        if (getApiKey()) clearApiKey();
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [resolve]);

  const signInWithPassword = useCallback(
    async (login: string, password: string) => {
      await authApi.login(login, password);
      // The session lives in an HttpOnly cookie the browser now holds; nothing
      // is stored in this tab. Any API key left over from before is dropped so
      // the session is unambiguously the credential in use.
      clearApiKey();
      await resolve();
    },
    [resolve],
  );

  const signInWithApiKey = useCallback(
    async (key: string) => {
      await verifyApiKey(key);
      setApiKey(key);
      await resolve();
    },
    [resolve],
  );

  const signOut = useCallback(async () => {
    clearApiKey();
    try {
      await authApi.logout();
    } catch {
      // Sign-out is best-effort on the wire: the local credential is already
      // gone, and a failed revoke must not leave the UI stuck signed in.
    }
    setIdentity(null);
    setState('unauthenticated');
  }, []);

  const value = useMemo(
    () => ({
      state,
      authDisabled,
      methods,
      identity,
      signInWithPassword,
      signInWithApiKey,
      signOut,
    }),
    [state, authDisabled, methods, identity, signInWithPassword, signInWithApiKey, signOut],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider');
  return ctx;
}
