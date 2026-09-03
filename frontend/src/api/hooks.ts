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
  status?: string;
  severity?: string;
  integration_id?: string;
  channel?: string;
  user_id?: string;
  alert_group_id?: string;
  route_id?: string;
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
} as const;

export type ListResource = keyof typeof LIST_PATHS;

type ResourceType = {
  users: User;
  teams: Team;
  schedules: Schedule;
  'escalation-chains': EscalationChain;
  integrations: Integration;
  'alert-groups': AlertGroup;
  alerts: Alert;
  notifications: Notification;
  'maintenance-windows': MaintenanceWindow;
};

export function useList<R extends ListResource>(
  resource: R,
  params: ListParams = {},
  options?: Partial<UseQueryOptions<Page<ResourceType[R]>>>,
) {
  return useQuery<Page<ResourceType[R]>>({
    queryKey: [resource, params],
    queryFn: () => api.get<Page<ResourceType[R]>>(LIST_PATHS[resource], params),
    ...options,
  });
}

export function useItem<R extends ListResource>(resource: R, id: string | undefined) {
  return useQuery<ResourceType[R]>({
    queryKey: [resource, 'item', id],
    queryFn: () => api.get<ResourceType[R]>(`${LIST_PATHS[resource]}/${id}`),
    enabled: Boolean(id),
  });
}

/** Loads every page of a collection — used for the small reference collections
 *  (users, teams, schedules, chains, integrations) that feed selects. */
export function useAllOf<R extends ListResource>(resource: R) {
  return useQuery<ResourceType[R][]>({
    queryKey: [resource, 'all'],
    queryFn: async () => {
      const page = await api.get<Page<ResourceType[R]>>(LIST_PATHS[resource], { limit: 1000 });
      return page.items ?? [];
    },
    staleTime: 30_000,
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
    queryFn: () => api.get<OnCallResponse>(`/api/v1/schedules/${scheduleId}/on-call`, { at }),
    enabled: Boolean(scheduleId),
  });
}

export function useSchedulePreview(scheduleId: string | undefined, from?: string, to?: string) {
  return useQuery<SchedulePreview>({
    queryKey: ['schedules', 'preview', scheduleId, from, to],
    queryFn: () => api.get<SchedulePreview>(`/api/v1/schedules/${scheduleId}/preview`, { from, to }),
    enabled: Boolean(scheduleId),
  });
}

/** Standing coverage check across every schedule, as the worker sees it. */
export function useScheduleCoverage() {
  return useQuery<ScheduleCoverageReport>({
    queryKey: ['schedules', 'coverage'],
    queryFn: () => api.get<ScheduleCoverageReport>('/api/v1/schedules/coverage'),
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
    queryFn: () => api.get(`/api/v1/alert-groups/${groupId}/timeline`),
    enabled: Boolean(groupId),
  });
}

// ── Notifications & delivery ─────────────────────────────────────────────────

export function useDeliveryAttempts(notificationId: string | undefined) {
  return useQuery<Page<DeliveryAttempt>>({
    queryKey: ['delivery-attempts', notificationId],
    queryFn: () => api.get('/api/v1/delivery-attempts', { notification_id: notificationId }),
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

export function useHistory(params: HistoryParams) {
  return useQuery<HistoryResponse>({
    queryKey: ['history', params],
    queryFn: () => api.get<HistoryResponse>('/api/v1/history', params),
  });
}

/** Who is on call right now across all schedules (Schedule v2, not on_duty). */
export function useOnCall(options?: { refetchInterval?: number }) {
  return useQuery<CurrentOnCall>({
    queryKey: ['on-call'],
    queryFn: () => api.get<CurrentOnCall>('/api/v1/on-call'),
    refetchInterval: options?.refetchInterval,
  });
}

// ── ChatOps ──────────────────────────────────────────────────────────────────

export function useChatopsChannels() {
  return useQuery<Page<ChatopsChannel>>({
    queryKey: ['chatops-channels'],
    queryFn: () => api.get('/api/v1/chatops/channels', { limit: 1000 }),
  });
}

export function useChatopsMessages() {
  return useQuery<Page<ChatopsMessage>>({
    queryKey: ['chatops-messages'],
    queryFn: () => api.get('/api/v1/chatops/messages', { limit: 200 }),
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
    queryFn: () => api.get<Health>('/health'),
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
    queryFn: () => api.get<Readiness>('/api/v1/readiness'),
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
    queryFn: () => api.get<Capabilities>('/api/v1/capabilities'),
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
