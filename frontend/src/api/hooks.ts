import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseQueryOptions,
} from '@tanstack/react-query';
import { useCallback } from 'react';
import { notifications } from '@mantine/notifications';
import { api } from './client';
import { useI18n } from '../i18n/I18nProvider';
import { describeError } from '../i18n/errors';
import type { Messages } from '../i18n/messages';
import type {
  Alert,
  AlertGroup,
  BulkActionResult,
  ChatopsChannel,
  ChatopsMessage,
  DebugRouteResult,
  DeliveryAttempt,
  EscalationChain,
  Health,
  CurrentOnCall,
  HistoryResponse,
  Integration,
  Notification,
  MaintenanceWindow,
  OnCallResponse,
  Page,
  Readiness,
  Report,
  Capabilities,
  Schedule,
  ScheduleCoverageReport,
  SchedulePreview,
  Team,
  User,
} from './types';

/**
 * Shows a failed request as a toast, in the reader's language.
 *
 * A hook rather than a plain function because the wording depends on the
 * current locale, and the locale lives in React context. The returned callback
 * is stable, so it can be handed to a mutation's `onError` without re-creating
 * the mutation on every render.
 */
export function useNotifyError() {
  const { t } = useI18n();
  return useCallback(
    (error: unknown, title?: string) => {
      notifications.show({
        color: 'red',
        title: title ?? t('toast.requestFailed'),
        message: describeError(error, t),
      });
    },
    [t],
  );
}

/** Catalog keys naming an outcome toast — the `toast.` namespace. */
type ToastKey = Extract<keyof Messages, `toast.${string}`>;

export type ListParams = {
  limit?: number;
  offset?: number;
  q?: string;
  has_steps?: boolean;
  status?: string;
  severity?: string;
  integration_id?: string;
  channel?: string;
  user_id?: string;
  alert_group_id?: string;
  route_id?: string;
  /** Comma-separated ids: fetch a named set in one request rather than one per row. */
  ids?: string;
  /** Column to order by; the server validates it against the collection. */
  sort?: string;
  order?: 'asc' | 'desc';
};

/** Collections exposed as paginated list endpoints. */
const LIST_PATHS = {
  users: '/api/v1/users',
  teams: '/api/v1/teams',
  schedules: '/api/v1/schedules',
  'escalation-chains': '/api/v1/escalation-chains',
  integrations: '/api/v1/integrations',
  'alert-groups': '/api/v1/alert-groups',
  alerts: '/api/v1/alerts',
  notifications: '/api/v1/notifications',
  'maintenance-windows': '/api/v1/maintenance-windows',
  reports: '/api/v1/reports',
} as const;

export type ListResource = keyof typeof LIST_PATHS;

export type ResourceType = {
  users: User;
  teams: Team;
  schedules: Schedule;
  'escalation-chains': EscalationChain;
  integrations: Integration;
  'alert-groups': AlertGroup;
  alerts: Alert;
  notifications: Notification;
  'maintenance-windows': MaintenanceWindow;
  reports: Report;
};

export function useList<R extends ListResource>(
  resource: R,
  params: ListParams = {},
  options?: Partial<UseQueryOptions<Page<ResourceType[R]>>>,
) {
  return useQuery<Page<ResourceType[R]>>({
    queryKey: [resource, params],
    queryFn: ({ signal }) => api.get<Page<ResourceType[R]>>(LIST_PATHS[resource], params, signal),
    ...options,
  });
}

export function useItem<R extends ListResource>(resource: R, id: string | undefined) {
  return useQuery<ResourceType[R]>({
    queryKey: [resource, 'item', id],
    queryFn: ({ signal }) => api.get<ResourceType[R]>(`${LIST_PATHS[resource]}/${id}`, undefined, signal),
    enabled: Boolean(id),
  });
}

// The API's largest page.
const MAX_PAGE = 1000;

/**
 * Every item of a collection, page after page.
 *
 * One request with a large limit is not "every item": the API caps a page at
 * 1000 and the list pages asked for 500, and neither said so — on a sandbox
 * with 553 users and 765 integrations, the users and integrations just created
 * were missing from their own pages, with no sign that anything was left out.
 */
async function fetchAll<R extends ListResource>(resource: R, signal: AbortSignal): Promise<ResourceType[R][]> {
  const items: ResourceType[R][] = [];
  for (;;) {
    signal.throwIfAborted();
    const page = await api.get<Page<ResourceType[R]>>(LIST_PATHS[resource], { limit: MAX_PAGE, offset: items.length }, signal);
    const got = page.items ?? [];
    items.push(...got);
    // Past 10 000 rows the API estimates the total (and may only give a lower
    // bound), so only an exact total may end the loop early.
    if (got.length < MAX_PAGE || (!page.total_estimated && page.total != null && items.length >= page.total)) return items;
  }
}

/** Loads every page of a collection — used for the small reference collections
 *  (users, teams, schedules, chains, integrations) that feed selects. */
export function useAllOf<R extends ListResource>(resource: R) {
  return useQuery<ResourceType[R][]>({
    queryKey: [resource, 'all'],
    queryFn: ({ signal }) => fetchAll(resource, signal),
    staleTime: 30_000,
  });
}

/** A whole collection in the shape of one page, for the pages that list one
 *  (users, integrations, chains, teams, schedules, maintenance windows). */
export function useFullList<R extends ListResource>(resource: R) {
  return useQuery({
    // Share the same underlying array with selects; only this observer wraps it.
    queryKey: [resource, 'all'],
    queryFn: ({ signal }) => fetchAll(resource, signal),
    staleTime: 30_000,
    select: (items): Page<ResourceType[R]> => ({ items, total: items.length, limit: items.length, offset: 0 }),
  });
}

/**
 * `successKey` is a catalog key, not a sentence: the toast has to be written in
 * whatever language is selected when the mutation resolves, which is not
 * knowable where these hooks are declared.
 */
function useResourceMutation<TVars, TResult>(
  fn: (vars: TVars) => Promise<TResult>,
  invalidate: string[],
  successKey: ToastKey,
) {
  const client = useQueryClient();
  const { t } = useI18n();
  const notifyError = useNotifyError();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      for (const key of invalidate) client.invalidateQueries({ queryKey: [key] });
      void client.invalidateQueries({ queryKey: ['overview'] });
      notifications.show({ color: 'teal', title: t('toast.done'), message: t(successKey) });
    },
    onError: (error) => notifyError(error),
  });
}

export function useCreate<R extends ListResource>(resource: R) {
  return useResourceMutation<Record<string, unknown>, ResourceType[R]>(
    (body) => api.post<ResourceType[R]>(LIST_PATHS[resource], body),
    [resource],
    'toast.created',
  );
}

export function useUpdate<R extends ListResource>(resource: R) {
  return useResourceMutation<{ id: string; body: Record<string, unknown> }, ResourceType[R]>(
    ({ id, body }) => api.put<ResourceType[R]>(`${LIST_PATHS[resource]}/${id}`, body),
    [resource],
    'toast.saved',
  );
}

export function useDelete<R extends ListResource>(resource: R) {
  return useResourceMutation<string, unknown>(
    (id) => api.delete(`${LIST_PATHS[resource]}/${id}`),
    [resource],
    'toast.deleted',
  );
}

// ── Users ────────────────────────────────────────────────────────────────────

/** PATCH is used for users: the backend whitelists updatable fields there. */
export function useUpdateUser() {
  return useResourceMutation<{ id: string; body: Record<string, unknown> }, User>(
    ({ id, body }) => api.patch<User>(`/api/v1/users/${id}`, body),
    ['users'],
    'toast.saved',
  );
}

export function useToggleDuty() {
  return useResourceMutation<{ id: string; onDuty: boolean }, User>(
    ({ id, onDuty }) => api.post<User>(`/api/v1/users/${id}/${onDuty ? 'duty-on' : 'duty-off'}`),
    ['users'],
    'toast.dutyUpdated',
  );
}

// ── Schedules ────────────────────────────────────────────────────────────────

export function useScheduleOnCall(scheduleId: string | undefined, at?: string) {
  return useQuery<OnCallResponse>({
    queryKey: ['schedules', 'on-call', scheduleId, at],
    queryFn: ({ signal }) => api.get<OnCallResponse>(`/api/v1/schedules/${scheduleId}/on-call`, { at }, signal),
    enabled: Boolean(scheduleId),
  });
}

export function useSchedulePreview(scheduleId: string | undefined, from?: string, to?: string) {
  return useQuery<SchedulePreview>({
    queryKey: ['schedules', 'preview', scheduleId, from, to],
    queryFn: ({ signal }) => api.get<SchedulePreview>(`/api/v1/schedules/${scheduleId}/preview`, { from, to }, signal),
    enabled: Boolean(scheduleId),
  });
}

/** Standing coverage check across every schedule, as the worker sees it. */
export function useScheduleCoverage() {
  return useQuery<ScheduleCoverageReport>({
    queryKey: ['schedules', 'coverage'],
    queryFn: ({ signal }) => api.get<ScheduleCoverageReport>('/api/v1/schedules/coverage', undefined, signal),
    staleTime: 30_000,
  });
}

export function useCreateOverride() {
  return useResourceMutation<{ id: string; body: Record<string, unknown> }, unknown>(
    ({ id, body }) => api.post(`/api/v1/schedules/${id}/override`, body),
    ['schedules'],
    'toast.overrideAdded',
  );
}

export function useUpdateOverride() {
  return useResourceMutation<
    { id: string; overrideId: string; body: Record<string, unknown> },
    unknown
  >(
    ({ id, overrideId, body }) => api.put(`/api/v1/schedules/${id}/overrides/${overrideId}`, body),
    ['schedules'],
    'toast.overrideUpdated',
  );
}

export function useDeleteOverride() {
  return useResourceMutation<{ id: string; overrideId: string }, unknown>(
    ({ id, overrideId }) => api.delete(`/api/v1/schedules/${id}/overrides/${overrideId}`),
    ['schedules'],
    'toast.overrideRemoved',
  );
}

// ── Integrations ─────────────────────────────────────────────────────────────

export function useRotateKey() {
  return useResourceMutation<string, Integration>(
    (id) => api.post<Integration>(`/api/v1/integrations/${id}/rotate-key`),
    ['integrations'],
    'toast.keyRotated',
  );
}

export function useDebugRoute() {
  const notifyError = useNotifyError();
  return useMutation({
    mutationFn: ({ key, payload }: { key: string; payload: unknown }) =>
      api.post<DebugRouteResult>(`/api/v1/routes/debug/${key}`, payload),
    onError: (error) => notifyError(error),
  });
}

// ── Alert groups ─────────────────────────────────────────────────────────────

type GroupAction = 'acknowledge' | 'unacknowledge' | 'resolve' | 'unresolve';

export function useGroupAction() {
  return useResourceMutation<{ id: string; action: GroupAction }, AlertGroup>(
    ({ id, action }) => api.post<AlertGroup>(`/api/v1/alert-groups/${id}/${action}`),
    ['alert-groups'],
    'toast.groupUpdated',
  );
}

export function useSilenceGroup() {
  return useResourceMutation<{ id: string; durationMinutes: number }, AlertGroup>(
    ({ id, durationMinutes }) =>
      api.post<AlertGroup>(`/api/v1/alert-groups/${id}/silence`, {
        duration_minutes: durationMinutes,
      }),
    ['alert-groups'],
    'toast.groupSilenced',
  );
}

type BulkAction = 'bulk-resolve' | 'bulk-acknowledge' | 'bulk-silence';

export function useBulkGroupAction() {
  return useResourceMutation<
    { action: BulkAction; groupIds: string[]; durationMinutes?: number },
    BulkActionResult
  >(
    ({ action, groupIds, durationMinutes }) =>
      api.post<BulkActionResult>(`/api/v1/alert-groups/${action}`, {
        group_ids: groupIds,
        ...(durationMinutes === undefined ? {} : { duration_minutes: durationMinutes }),
      }),
    ['alert-groups'],
    'toast.bulkApplied',
  );
}

export function useGroupTimeline(groupId: string | undefined) {
  return useQuery<{ id: string; timeline: AlertGroup['logs'] }>({
    queryKey: ['alert-groups', 'timeline', groupId],
    queryFn: ({ signal }) => api.get(`/api/v1/alert-groups/${groupId}/timeline`, undefined, signal),
    enabled: Boolean(groupId),
  });
}

// ── Notifications & delivery ─────────────────────────────────────────────────

export function useDeliveryAttempts(notificationId: string | undefined) {
  return useQuery<Page<DeliveryAttempt>>({
    queryKey: ['delivery-attempts', notificationId],
    queryFn: ({ signal }) => api.get('/api/v1/delivery-attempts', { notification_id: notificationId }, signal),
    enabled: Boolean(notificationId),
  });
}

// ── History / insights ───────────────────────────────────────────────────────

export type HistoryParams = {
  from?: string;
  to?: string;
  integration?: string;
  severity?: string;
  status?: string;
  channel?: string;
  user?: string;
  team?: string;
  limit?: number;
  offset?: number;
};

export interface InsightsBucket {
  day: string;
  opened: number;
  resolved: number;
  delivered: number;
  failed: number;
}

export interface InsightsSummary {
  from: string;
  to: string;
  groups_by_status: Record<string, number>;
  /** Keyed by severity level, so `high` and `error` are one number. */
  groups_by_level: Record<string, number>;
  notifications_by_state: Record<string, number>;
  trend: InsightsBucket[];
}

/**
 * The whole insights screen in one request.
 *
 * It replaces twelve separate list queries whose only purpose was their `total`
 * — twelve round trips to draw six numbers — and it carries the daily buckets
 * those queries could not produce at all.
 */
export function useInsightsSummary(params: { from?: string; to?: string; integration_id?: string }) {
  return useQuery<InsightsSummary>({
    queryKey: ['insights-summary', params],
    queryFn: ({ signal }) => api.get('/api/v1/insights/summary', params, signal),
  });
}

export function useHistory(params: HistoryParams) {
  return useQuery<HistoryResponse>({
    queryKey: ['history', params],
    queryFn: ({ signal }) => api.get<HistoryResponse>('/api/v1/history', params, signal),
  });
}

/** Who is on call right now across all schedules (Schedule v2, not on_duty). */
export function useOnCall(options?: { refetchInterval?: number }) {
  return useQuery<CurrentOnCall>({
    queryKey: ['on-call'],
    queryFn: ({ signal }) => api.get<CurrentOnCall>('/api/v1/on-call', undefined, signal),
    refetchInterval: options?.refetchInterval,
  });
}

// ── ChatOps ──────────────────────────────────────────────────────────────────

export function useChatopsChannels() {
  return useQuery<Page<ChatopsChannel>>({
    queryKey: ['chatops-channels'],
    queryFn: ({ signal }) => api.get('/api/v1/chatops/channels', { limit: 1000 }, signal),
  });
}

export function useChatopsMessages() {
  return useQuery<Page<ChatopsMessage>>({
    queryKey: ['chatops-messages'],
    queryFn: ({ signal }) => api.get('/api/v1/chatops/messages', { limit: 200 }, signal),
  });
}

export function useCreateChatopsChannel() {
  return useResourceMutation<Record<string, unknown>, ChatopsChannel>(
    (body) => api.post('/api/v1/chatops/channels', body),
    ['chatops-channels'],
    'toast.channelCreated',
  );
}

export function useUpdateChatopsChannel() {
  return useResourceMutation<{ id: string; body: Record<string, unknown> }, ChatopsChannel>(
    ({ id, body }) => api.put(`/api/v1/chatops/channels/${id}`, body),
    ['chatops-channels'],
    'toast.channelSaved',
  );
}

export function useDeleteChatopsChannel() {
  return useResourceMutation<string, unknown>(
    (id) => api.delete(`/api/v1/chatops/channels/${id}`),
    ['chatops-channels'],
    'toast.channelDeleted',
  );
}

// ── Ops ──────────────────────────────────────────────────────────────────────

export function useHealth() {
  return useQuery<Health>({
    queryKey: ['health'],
    queryFn: ({ signal }) => api.get<Health>('/health', undefined, signal),
    refetchInterval: 15_000,
    retry: false,
  });
}

// ── Setup readiness (BETA-051) ───────────────────────────────────────────────

/**
 * Whether this installation could actually page someone. Polled rather than
 * cached hard: the setup wizard is watched while things are being configured,
 * and a stale "not ready" is the one answer that makes people stop trusting it.
 */
export function useReadiness() {
  return useQuery<Readiness>({
    queryKey: ['readiness'],
    queryFn: ({ signal }) => api.get<Readiness>('/api/v1/readiness', undefined, signal),
    refetchInterval: 30_000,
  });
}

/**
 * What this installation can do, and — when it cannot — whether that is because
 * the edition lacks the feature or because nobody configured it. Cached hard:
 * the answer changes when the binary or the environment changes, neither of
 * which happens while somebody is looking at a page.
 */
export function useCapabilities() {
  return useQuery<Capabilities>({
    queryKey: ['capabilities'],
    queryFn: ({ signal }) => api.get<Capabilities>('/api/v1/capabilities', undefined, signal),
    staleTime: 5 * 60_000,
  });
}

export function useAcknowledgeReadiness() {
  return useResourceMutation<{ reason: string }, Readiness>(
    (body) => api.post<Readiness>('/api/v1/readiness/acknowledge', body),
    ['readiness'],
    'toast.blockersAcknowledged',
  );
}

export function useRunEscalations() {
  return useResourceMutation<void, unknown>(
    () => api.post('/api/v1/escalations/run'),
    ['alert-groups', 'notifications'],
    'toast.escalationTriggered',
  );
}

/** Resolve only referenced IDs, in bounded batches; never fall back to all rows. */
export function useReferences<R extends ListResource>(resource: R, ids: string[]) {
  const wanted = [...new Set(ids.filter(Boolean))].sort();
  return useQuery({
    queryKey: [resource, 'references', wanted],
    enabled: wanted.length > 0,
    staleTime: 30_000,
    queryFn: async ({ signal }) => {
      const items: ResourceType[R][] = [];
      for (let offset = 0; offset < wanted.length; offset += 500) {
        signal.throwIfAborted();
        const page = await api.get<Page<ResourceType[R]>>(LIST_PATHS[resource], { ids: wanted.slice(offset, offset + 500).join(','), limit: 500 }, signal);
        items.push(...page.items);
      }
      return items;
    },
  });
}

/** IDs embedded in the currently rendered records (shifts, chain steps, etc.). */
export function referenceIds(value: unknown, fields: string[]): string[] {
  if (Array.isArray(value)) return value.flatMap(item => referenceIds(item, fields));
  if (!value || typeof value !== 'object') return [];
  return Object.entries(value).flatMap(([key, item]) => fields.includes(key)
    ? (Array.isArray(item) ? item : [item]).filter((id): id is string => typeof id === 'string' && !!id)
    : referenceIds(item, fields));
}


export type Overview = import('./schema').components['schemas']['Overview'];
export function useOverview() {
  return useQuery({
    queryKey: ['overview'],
    queryFn: ({ signal }) => api.get<Overview>('/api/v1/overview', undefined, signal),
    refetchInterval: 15_000,
  });
}
