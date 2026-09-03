// Domain types for the /api/v1 payloads.
//
// The core entities are NOT written by hand here: they are aliases into
// `schema.d.ts`, which `npm run openapi:types` generates from docs/openapi.json.
// That makes the OpenAPI document the single source of truth — a field that
// changes in the spec becomes a compile error here instead of a runtime
// surprise, and `npm run openapi:check` fails if the generated file drifts from
// the spec. ChatOps and the history/on-call/bulk envelopes are
// modelled too, so nothing below is a hand-written guess about a payload; what
// remains hand-written is client-side shapes (`Health`, which is served outside
// /api/v1) and nothing else.
//
// Every entity is stored as a JSONB document, so unknown extra keys are always
// possible; the generated types carry an index signature for exactly that.

import type { components } from './schema';

type Schemas = components['schemas'];

export interface BaseEntity {
  id: string;
  created_at?: string;
  updated_at?: string;
  [key: string]: unknown;
}

export type Priority = User['priority'];

export const PRIORITIES: Priority[] = ['high', 'medium', 'low'];

/** Permission level. Ordered: each role grants everything the next one does. */
export type Role = User['role'];

/** Selectable roles, widest first, with the "no access" option spelled out. */
export const ROLE_OPTIONS: { value: Role; label: string }[] = [
  { value: '', label: 'No access (roster only)' },
  { value: 'viewer', label: 'Viewer — read only' },
  { value: 'responder', label: 'Responder — acknowledge, resolve, silence' },
  { value: 'editor', label: 'Editor — configuration' },
  { value: 'admin', label: 'Admin — identity and audit' },
];

export type NotificationTargetType = NotificationTarget['type'];

export const NOTIFICATION_TARGET_TYPES: NotificationTargetType[] = [
  'log',
  'webhook',
  'telegram',
  'email',
  'call',
  'slack',
  'mattermost',
];

export type NotificationTarget = Schemas['NotificationTarget'];

// Personal notification policies (BETA-031). Channels a policy step may use — a
// subset of the target types (the ones that reach one person directly).
export type PolicyChannel = PolicyStep['channel'];

export const POLICY_CHANNELS: PolicyChannel[] = ['telegram', 'email', 'webhook', 'call', 'log'];

/** The policy names the contract defines; dropping one from the spec breaks the list below. */
export type PolicyName = keyof NotificationPolicies;
export const POLICY_NAMES: readonly PolicyName[] = ['default', 'important'] as const;

export type PolicyStep = Schemas['PolicyStep'];

export type NotificationPolicies = Schemas['NotificationPolicies'];

export type User = Schemas['User'];

export type Team = Schemas['Team'];

export type Shift = Schemas['Shift'];

export type ShiftRecurrence = Shift['recurrence'];

export const SHIFT_RECURRENCES: ShiftRecurrence[] = ['none', 'daily', 'weekly'];

export type ScheduleOverride = Schemas['ScheduleOverride'];

/** Who performed an action, as stored alongside the entity it changed. */
export type ActorRef = Schemas['ActorRef'];

export type HandoffUnit = Rotation['handoff_unit'];

export const HANDOFF_UNITS: HandoffUnit[] = ['hours', 'days', 'weeks'];

export const RESTRICTION_DAYS = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'] as const;

export type RotationRestriction = Schemas['RotationRestriction'];

export type Rotation = Schemas['Rotation'];

export type Schedule = Schemas['Schedule'];

export type ScheduleSegment = Schemas['ScheduleSegment'];

/** Which layer of Schedule v2 produced a segment; '' means nobody is on call. */
export type ScheduleSource = ScheduleSegment['source'];

export type SchedulePreview = Schemas['SchedulePreview'];

export type ScheduleCoverageItem = Schemas['ScheduleCoverageItem'];

export type ScheduleCoverageReport = Schemas['ScheduleCoverageReport'];

export type StepKind = EscalationStep['kind'];

export const STEP_KINDS: StepKind[] = [
  'WAIT',
  'NOTIFY_USER',
  'NOTIFY_SCHEDULE',
  'NOTIFY_TEAM',
  'NOTIFY_EMERGENCY',
  'NOTIFY_DUTY_USERS',
  'TRIGGER_WEBHOOK',
  'CREATE_ISSUE',
  'RESOLVE',
  'REPEAT',
];

export type EscalationStep = Schemas['EscalationStep'];

export type EscalationChain = Schemas['EscalationChain'];

export type IntegrationRoute = Schemas['IntegrationRoute'];

export type RouteMatchType = IntegrationRoute['match_type'];

export type NotificationPolicy = Schemas['IntegrationNotificationPolicy'];

export type Integration = Schemas['Integration'];
export type MaintenanceWindow = Schemas['MaintenanceWindow'];

/** Ingestion endpoint kinds the backend exposes under /integrations/v1/. */
export const INTEGRATION_TYPES = [
  'webhook',
  'alertmanager',
  'pagerduty',
  'victorops',
  'grafana-alerting',
] as const;

export type GroupLogEntry = Schemas['GroupLogEntry'];

export type AlertGroup = Schemas['AlertGroup'];

export type AlertGroupStatus = AlertGroup['status'];

export type Alert = Schemas['Alert'];

export type Notification = Schemas['Notification'];

export type NotificationStatus = Notification['status'];

export type DeliveryAttempt = Schemas['DeliveryAttempt'];

export type NotificationBatch = Schemas['NotificationBatch'];

export type ChatopsChannel = Schemas['ChatOpsChannel'];

export type ChatopsMessage = Schemas['ChatOpsMessage'];

/** inbound (a slash command) or outbound (a notification pushed to the channel). */
export type ChatopsDirection = ChatopsMessage['direction'];


/** Envelope returned by GET list endpoints backed by ListCollectionPage. */
export interface Page<T> {
  items: T[];
  total: number;
  limit: number;
  offset: number;
}

/** One entry of GET /api/v1/history. Its collections are nullable: the engine
 * builds them from var-declared slices, so "no alerts" arrives as null, not []. */
export type HistoryItem = Schemas['HistoryItem'];

export type HistoryResponse = Schemas['HistoryPage'];

export type OnCallResponse = Schemas['ScheduleOnCall'];

/** One person on call right now (from GET /api/v1/on-call). */
export type OnCallEntry = Schemas['OnCallEntry'];

/** Aggregate on-call across all schedules, resolved through Schedule v2. */
export type CurrentOnCall = Schemas['CurrentOnCall'];

// Setup readiness (BETA-051). Derived from the spec like the rest: the severity
// union and the check keys are exactly what the engine can emit.
export type ReadinessCheck = Schemas['ReadinessCheck'];

export type ReadinessSeverity = ReadinessCheck['severity'];

export type ReadinessCheckKey = ReadinessCheck['key'];

export type ReadinessAcknowledgement = Schemas['ReadinessAcknowledgement'];

export type Readiness = Schemas['Readiness'];
export type Capabilities = Schemas['Capabilities'];
export type Capability = Schemas['Capability'];
export type CapabilityState = Capability['state'];

export interface Health {
  status: 'ok' | 'degraded';
  db_ok: boolean;
  version: string;
  uptime_seconds: number;
  db_error?: string;
  last_worker_cycle_at?: string;
  worker_cycles_completed?: number;
}

// The three bulk endpoints return three different shapes, and each list is null
// rather than [] when nothing fell into it.
export type BulkResolveResult = Schemas['BulkResolveResult'];

export type BulkAcknowledgeResult = Schemas['BulkAcknowledgeResult'];

export type BulkSilenceResult = Schemas['BulkSilenceResult'];

export type BulkActionResult = BulkResolveResult | BulkAcknowledgeResult | BulkSilenceResult;

export type DebugRouteResult = Schemas['RouteDebug'];

export type NotificationPreview = Schemas['NotificationPreview'];
