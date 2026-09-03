import { POLICY_NAMES, type NotificationPolicies, type PolicyStep } from '../api/types';

export type Profile = { email: string; telegram_id: string; phone: string };

export const EMPTY_STEP: PolicyStep = { channel: 'telegram', target: '', wait_minutes: 5 };

/**
 * policyStepIssue returns a human-readable problem with a step, or null when it
 * is deliverable. It mirrors resolveChannelTarget on the backend: a webhook step
 * needs an explicit URL, and a profile channel needs either an override target
 * or the matching profile field.
 */
export function policyStepIssue(step: PolicyStep, profile: Profile): string | null {
  if (step.target.trim()) return null;
  switch (step.channel) {
    case 'webhook':
      return 'Webhook step needs a target URL';
    case 'email':
      return profile.email.trim() ? null : 'No email on this user; set one or add a target';
    case 'telegram':
      return profile.telegram_id.trim()
        ? null
        : 'No Telegram ID on this user; set one or add a target';
    case 'call':
      return profile.phone.trim() ? null : 'No phone on this user; set one or add a target';
    case 'log':
      return null;
  }
}

/** cleanPolicies drops empty policy lists so an untouched editor submits nothing. */
export function cleanPolicies(policies: NotificationPolicies): NotificationPolicies {
  const out: NotificationPolicies = {};
  for (const name of POLICY_NAMES) {
    const steps = policies[name];
    if (steps && steps.length > 0) out[name] = steps;
  }
  return out;
}
