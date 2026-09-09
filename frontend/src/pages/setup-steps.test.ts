import { describe, expect, it } from 'vitest';
import { buildSetupSteps, firstOpenStep, setupProgress, type SetupCounts } from './setup-steps';
import type { Readiness, ReadinessCheck } from '../api/types';

const EMPTY_COUNTS: SetupCounts = {
  teams: 0,
  users: 0,
  schedules: 0,
  hasNonEmptyChain: false,
  integrations: 0,
};
const FULL_COUNTS: SetupCounts = {
  teams: 1,
  users: 2,
  schedules: 1,
  hasNonEmptyChain: true,
  integrations: 1,
};

function check(
  key: ReadinessCheck['key'],
  severity: ReadinessCheck['severity'],
  extra: Partial<ReadinessCheck> = {},
): ReadinessCheck {
  return { key, title: key, severity, detail: `${key} is ${severity}`, items: [], ...extra };
}

function readiness(checks: ReadinessCheck[], overrides: Partial<Readiness> = {}): Readiness {
  return {
    checked_at: '2026-07-27T00:00:00Z',
    checks,
    blockers: checks.filter((c) => c.severity === 'blocker').length,
    warnings: checks.filter((c) => c.severity === 'warning').length,
    ready: checks.every((c) => c.severity !== 'blocker'),
    production_ready: checks.every((c) => c.severity !== 'blocker'),
    blocker_fingerprint: '',
    ...overrides,
  };
}

const HEALTHY = readiness([
  check('database', 'ok'),
  check('worker', 'ok'),
  check('integrations', 'ok'),
  check('routing', 'ok'),
  check('notification_targets', 'ok'),
  check('schedule_coverage', 'ok'),
  check('backup', 'ok'),
]);

describe('buildSetupSteps', () => {
  it('marks everything but the test step done for a healthy, populated install', () => {
    const steps = buildSetupSteps(HEALTHY, FULL_COUNTS);
    const byKey = Object.fromEntries(steps.map((s) => [s.key, s.status]));

    expect(byKey.people).toBe('done');
    expect(byKey.providers).toBe('done');
    expect(byKey.schedule).toBe('done');
    expect(byKey.chain).toBe('done');
    expect(byKey.integration).toBe('done');
    // Nothing records that a test alert succeeded, so the step never claims it.
    expect(byKey.test).toBe('todo');
  });

  it('does not tick a step just because a check is ok with nothing configured', () => {
    // The trap this guards: with no users, "everyone can be reached" is
    // trivially true, and a naive mapping would report the step as done.
    const steps = buildSetupSteps(HEALTHY, EMPTY_COUNTS);
    const byKey = Object.fromEntries(steps.map((s) => [s.key, s.status]));

    expect(byKey.people).toBe('todo');
    expect(byKey.providers).toBe('todo');
    expect(byKey.schedule).toBe('todo');
    expect(byKey.integration).toBe('todo');
  });

  it('does not mark the chain step done for an empty draft chain', () => {
    // The server accepts a chain with zero steps; counting its existence alone
    // would tick "who gets paged, and when" while nobody is actually named.
    const step = buildSetupSteps(HEALTHY, { ...FULL_COUNTS, hasNonEmptyChain: false }).find(
      (s) => s.key === 'chain',
    );
    expect(step?.status).toBe('todo');
  });

  it('reports a blocking check as blocked and carries its detail and items', () => {
    const report = readiness([
      check('notification_targets', 'blocker', {
        detail: 'these people can be selected to page but no transport can reach them.',
        items: ['Alice: only unconfigured channels: telegram'],
      }),
    ]);
    const step = buildSetupSteps(report, FULL_COUNTS).find((s) => s.key === 'providers');

    expect(step?.status).toBe('blocked');
    expect(step?.detail).toContain('no transport can reach them');
    expect(step?.items).toEqual(['Alice: only unconfigured channels: telegram']);
  });

  it('treats a warning as unfinished rather than done', () => {
    const report = readiness([check('schedule_coverage', 'warning', { detail: 'a schedule has holes.' })]);
    const step = buildSetupSteps(report, FULL_COUNTS).find((s) => s.key === 'schedule');

    expect(step?.status).toBe('todo');
    expect(step?.detail).toBe('a schedule has holes.');
  });

  it('falls back to what exists while the readiness report is still loading', () => {
    const steps = buildSetupSteps(undefined, FULL_COUNTS);
    const byKey = Object.fromEntries(steps.map((s) => [s.key, s.status]));

    expect(byKey.people).toBe('done');
    expect(byKey.providers).toBe('done');
    expect(steps.every((s) => s.detail === undefined)).toBe(true);
  });

  it('omits the detail of a passing check so the UI shows no stale complaint', () => {
    const step = buildSetupSteps(HEALTHY, FULL_COUNTS).find((s) => s.key === 'providers');
    expect(step?.detail).toBeUndefined();
  });
});

describe('setupProgress', () => {
  it('counts only finished, scored steps — the test step never caps it under 100%', () => {
    const progress = setupProgress(buildSetupSteps(HEALTHY, FULL_COUNTS));
    expect(progress).toEqual({ done: 5, total: 5, percent: 100 });
  });

  it('is zero for a fresh installation', () => {
    expect(setupProgress(buildSetupSteps(HEALTHY, EMPTY_COUNTS)).done).toBe(0);
  });
});

describe('firstOpenStep', () => {
  it('prefers a blocked step over an merely unfinished one', () => {
    const report = readiness([
      check('routing', 'blocker', { detail: 'a route reaches nobody.' }),
    ]);
    // people/providers/schedule are unfinished (no counts), integration is blocked.
    const step = firstOpenStep(buildSetupSteps(report, FULL_COUNTS));
    expect(step?.key).toBe('integration');
  });

  it('falls back to the first unfinished step when nothing blocks', () => {
    expect(firstOpenStep(buildSetupSteps(HEALTHY, EMPTY_COUNTS))?.key).toBe('people');
  });
});
