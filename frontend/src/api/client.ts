// Thin fetch wrapper around the nxs-anomaly management API (/api/v1).
// The Grafana compat layer (/api/internal/v1) is deliberately not used.

import type { components } from './schema';

const API_KEY_STORAGE = 'nxs-anomaly.api-key';

export class ApiError extends Error {
  /**
   * `detail` is what the server said, verbatim and in English, or empty when it
   * said nothing beyond the status code.
   *
   * It is kept apart from `message` so the UI can lead with a translated
   * sentence for the status and still show the server's own words after it.
   * Translating away a specific reason — "escalation chain has no steps" — in
   * favour of a generic "invalid request" would cost the operator the one piece
   * of information that tells them what to fix.
   */
  constructor(
    readonly status: number,
    message: string,
    readonly detail: string = '',
  ) {
    super(message);
    this.name = 'ApiError';
  }

  get isUnauthorized() {
    return this.status === 401;
  }
}

export function getApiKey(): string | null {
  return localStorage.getItem(API_KEY_STORAGE);
}

export function setApiKey(key: string) {
  localStorage.setItem(API_KEY_STORAGE, key);
}

export function clearApiKey() {
  localStorage.removeItem(API_KEY_STORAGE);
}

/** Base URL of the API. Empty by default: nginx/Vite proxy keeps us same-origin. */
const BASE_URL = (import.meta.env.VITE_API_BASE_URL as string | undefined) ?? '';

type Query = Record<string, string | number | boolean | undefined | null>;

function buildUrl(path: string, query?: Query): string {
  const url = `${BASE_URL}${path}`;
  if (!query) return url;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== null && value !== '') {
      params.set(key, String(value));
    }
  }
  const qs = params.toString();
  return qs ? `${url}?${qs}` : url;
}

async function request<T>(
  method: string,
  path: string,
  options: { query?: Query; body?: unknown } = {},
): Promise<T> {
  const headers: Record<string, string> = {};
  const key = getApiKey();
  if (key) headers['X-API-Key'] = key;
  if (options.body !== undefined) headers['Content-Type'] = 'application/json';

  const response = await fetch(buildUrl(path, options.query), {
    method,
    headers,
    // The session cookie is HttpOnly, so it is never read here — it only has to
    // be sent. 'same-origin' is the fetch default, stated explicitly because
    // the whole session mechanism depends on it.
    credentials: 'same-origin',
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });

  if (response.status === 204) return undefined as T;

  const text = await response.text();
  let payload: unknown = undefined;
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = text;
    }
  }

  if (!response.ok) {
    const detail =
      (payload && typeof payload === 'object' && 'error' in payload
        ? String((payload as { error: unknown }).error)
        : typeof payload === 'string' && payload
          ? payload
          : null) ?? '';
    throw new ApiError(
      response.status,
      detail || `Request failed with status ${response.status}`,
      detail,
    );
  }

  return payload as T;
}

export const api = {
  get: <T>(path: string, query?: Query) => request<T>('GET', path, { query }),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, { body }),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, { body }),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, { body }),
  delete: <T>(path: string) => request<T>('DELETE', path),
};

/**
 * Verifies a candidate API key by issuing a request that requires auth.
 * /health is intentionally not used: it is unauthenticated and would accept
 * any key.
 */
export async function verifyApiKey(candidate: string): Promise<void> {
  const headers: Record<string, string> = {};
  if (candidate) headers['X-API-Key'] = candidate;
  const response = await fetch(buildUrl('/api/v1/users', { limit: 1 }), {
    headers,
    credentials: 'same-origin',
  });
  if (response.status === 401) {
    throw new ApiError(401, 'Invalid API key');
  }
  if (!response.ok) {
    throw new ApiError(response.status, `API unreachable (status ${response.status})`);
  }
}

// ---- identity ----
//
// Both shapes come from docs/openapi.json via `npm run openapi:types`, so a
// change to what /auth/methods or /auth/me returns lands here as a compile
// error rather than as a silently-wrong sign-in screen or permission check.

/** How this deployment expects people to authenticate. */
export type AuthMethods = components['schemas']['AuthMethods'];

/** The principal behind the current request, as the backend sees it. */
export type Identity = components['schemas']['Identity'];

/** The caller's own presentation settings, as saved on their user record. */
export type UserPreferences = components['schemas']['UserPreferences'];

/**
 * Saves the caller's own language (and optionally timezone).
 *
 * This is its own endpoint rather than a PATCH on /users/{id}, which is
 * admin-only: choosing the language you read the product in is not an
 * administrative act, and a viewer must be able to do it unaided.
 */
export function savePreferences(preferences: Partial<UserPreferences>) {
  return api.put<UserPreferences>('/api/v1/auth/preferences', preferences);
}

/** One live browser session, as shown on the Settings page. */
export interface SessionInfo {
  id: string;
  created_at: string;
  expires_at: string;
  request_ip: string;
  user_agent: string;
  /** True for the session making the request. */
  current: boolean;
}

export const authApi = {
  methods: () => api.get<AuthMethods>('/api/v1/auth/methods'),
  me: () => api.get<Identity>('/api/v1/auth/me'),
  login: (login: string, password: string) =>
    api.post<{ role: string; expires_at: string }>('/api/v1/auth/login', { login, password }),
  logout: () => api.post<void>('/api/v1/auth/logout'),
  changePassword: (current_password: string, new_password: string) =>
    api.post<void>('/api/v1/auth/password', { current_password, new_password }),
  /**
   * Single sign-on is a browser redirect, not a fetch: the provider needs a
   * top-level navigation, and the session cookie comes back on the callback.
   */
  sessions: () => api.get<{ items: SessionInfo[] }>('/api/v1/auth/sessions'),
  revokeSession: (id: string) => api.delete<void>(`/api/v1/auth/sessions/${id}`),
  ssoUrl: (returnTo: string) =>
    `${BASE_URL}/api/v1/auth/oidc/login?return_to=${encodeURIComponent(returnTo)}`,
};
