import type { Readiness, ReadinessCheck, ReadinessCheckKey, ReadinessSeverity } from '../api/types';
import type { Messages } from '../i18n/messages';

/**
 * The setup wizard's state is derived, never stored.
 *
 * A stored checklist drifts: someone ticks "schedule configured", later deletes
 * the schedule, and the wizard keeps claiming the step is done. Every step here
 * answers its question from the same readiness report the Readiness page shows,
 * so the wizard cannot disagree with the product about what is configured.
 */

export type StepStatus = 'done' | 'blocked' | 'todo';

/** Catalog keys naming a wizard step — the `setup.step.` namespace. */
export type SetupStepKey = Extract<keyof Messages, `setup.step.${string}`>;

export interface SetupStep {
  key: string;
  /**
   * Catalog keys rather than sentences.
   *
   * This module is pure logic over the readiness report and is unit-tested as
   * such; holding finished English here would make the wizard the one part of
   * the UI that cannot change language, and would put translations inside the
   * tests that assert step *status*.
   */
  labelKey: SetupStepKey;
  /** What the operator gets out of finishing this step. */
  descriptionKey: SetupStepKey;
  status: StepStatus;
  /**
   * Why the step is not done, taken verbatim from the readiness check that
   * judged it. Not translated: it is the backend's own explanation, naming
   * concrete objects — see `i18n/errors.ts` for the same reasoning.
   */
  detail?: string;
  /** Where to go to do it. Absent for steps with nothing to link to. */
  to?: string;
  /** The specific objects at fault, when the check named any. */
  items: string[];
}

export interface SetupCounts {
  teams: number;
  users: number;
  schedules: number;
  chains: number;
  integrations: number;
}

function findCheck(readiness: Readiness | undefined, key: ReadinessCheckKey): ReadinessCheck | undefined {
  return readiness?.checks?.find((c) => c.key === key);
}

function severity(readiness: Readiness | undefined, key: ReadinessCheckKey): ReadinessSeverity | undefined {
  return findCheck(readiness, key)?.severity;
}

/**
 * Maps a readiness check onto a step, with a count as the "has anything been
 * created at all" fallback.
 *
 * The distinction matters: a check can be `ok` simply because nothing exists to
 * be wrong yet. "No chain names anyone" is not the same as "everyone named is
 * reachable", and a wizard that ticks the step either way teaches people to
 * ignore it.
 */
function stepStatus(
  readiness: Readiness | undefined,
  key: ReadinessCheckKey,
  created: boolean,
): StepStatus {
  const sev = severity(readiness, key);
  if (sev === 'blocker') return 'blocked';
  if (!created) return 'todo';
  if (sev === 'warning') return 'todo';
  return 'done';
}

/**
 * Builds the wizard steps. `readiness` may be undefined while the report is
 * loading; steps then fall back to "has anything been created", which is the
 * honest answer available without it.
 */
export function buildSetupSteps(readiness: Readiness | undefined, counts: SetupCounts): SetupStep[] {
  const detailOf = (key: ReadinessCheckKey) => {
    const check = findCheck(readiness, key);
    if (!check || check.severity === 'ok') return undefined;
    return check.detail;
  };
  const itemsOf = (key: ReadinessCheckKey) => findCheck(readiness, key)?.items ?? [];

  return [
    {
      key: 'people',
      labelKey: 'setup.step.people',
      descriptionKey: 'setup.step.peopleDescription',
      // No readiness check owns "a team exists": a single-team pilot needs none,
      // so this step is judged purely on whether anyone has been created.
      status: counts.users > 0 ? 'done' : 'todo',
      to: '/users',
      items: [],
    },
    {
      key: 'providers',
      labelKey: 'setup.step.providers',
      descriptionKey: 'setup.step.providersDescription',
      status: stepStatus(readiness, 'notification_targets', counts.users > 0),
      detail: detailOf('notification_targets'),
      to: '/users',
      items: itemsOf('notification_targets'),
    },
    {
      key: 'schedule',
      labelKey: 'setup.step.schedule',
      descriptionKey: 'setup.step.scheduleDescription',
      status: stepStatus(readiness, 'schedule_coverage', counts.schedules > 0),
      detail: detailOf('schedule_coverage'),
      to: '/schedules',
      items: itemsOf('schedule_coverage'),
    },
    {
      key: 'chain',
      labelKey: 'setup.step.chain',
      descriptionKey: 'setup.step.chainDescription',
      status: counts.chains > 0 ? 'done' : 'todo',
      to: '/escalation-chains',
      items: [],
    },
    {
      key: 'integration',
      labelKey: 'setup.step.integration',
      descriptionKey: 'setup.step.integrationDescription',
      status: stepStatus(readiness, 'routing', counts.integrations > 0),
      detail: detailOf('routing') ?? detailOf('integrations'),
      to: '/integrations',
      items: itemsOf('routing'),
    },
    {
      key: 'test',
      labelKey: 'setup.step.test',
      descriptionKey: 'setup.step.testDescription',
      // Deliberately never "done": there is no state that records a successful
      // test, and inventing one would be exactly the stored-checklist lie this
      // module avoids. It stays actionable, which is also how it should read —
      // testing the path is worth repeating.
      status: 'todo',
      to: '/integrations',
      items: [],
    },
  ];
}

/** How far along setup is, for the progress indicator. */
export function setupProgress(steps: SetupStep[]): { done: number; total: number; percent: number } {
  const done = steps.filter((s) => s.status === 'done').length;
  const total = steps.length;
  return { done, total, percent: total === 0 ? 0 : Math.round((done / total) * 100) };
}

/** The first step that still needs attention, so the wizard can open there. */
export function firstOpenStep(steps: SetupStep[]): SetupStep | undefined {
  return steps.find((s) => s.status === 'blocked') ?? steps.find((s) => s.status === 'todo');
}
