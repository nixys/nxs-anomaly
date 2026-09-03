import { describe, expect, it } from 'vitest';
import { cleanPolicies, policyStepIssue } from './notification-policy-utils';
import type { NotificationPolicies, PolicyStep } from '../api/types';

const profile = { email: '', telegram_id: '', phone: '' };
const full = { email: 'e@x', telegram_id: 'tg', phone: '+1' };

const step = (s: Partial<PolicyStep>): PolicyStep => ({
  channel: 'telegram',
  target: '',
  wait_minutes: 0,
  ...s,
});

describe('policyStepIssue', () => {
  it('accepts a step with an explicit target regardless of profile', () => {
    expect(policyStepIssue(step({ channel: 'webhook', target: 'https://h/x' }), profile)).toBeNull();
  });

  it('requires a URL for a webhook step with no target', () => {
    expect(policyStepIssue(step({ channel: 'webhook' }), full)).toMatch(/URL/);
  });

  it('flags a profile channel the user has no address for', () => {
    expect(policyStepIssue(step({ channel: 'telegram' }), profile)).toMatch(/Telegram/);
    expect(policyStepIssue(step({ channel: 'email' }), profile)).toMatch(/email/);
    expect(policyStepIssue(step({ channel: 'call' }), profile)).toMatch(/phone/);
  });

  it('accepts a profile channel when the profile has the address', () => {
    expect(policyStepIssue(step({ channel: 'telegram' }), full)).toBeNull();
    expect(policyStepIssue(step({ channel: 'email' }), full)).toBeNull();
    expect(policyStepIssue(step({ channel: 'call' }), full)).toBeNull();
  });

  it('always accepts a log step', () => {
    expect(policyStepIssue(step({ channel: 'log' }), profile)).toBeNull();
  });
});

describe('cleanPolicies', () => {
  it('drops empty policy lists', () => {
    const input: NotificationPolicies = { default: [step({ channel: 'log' })], important: [] };
    const out = cleanPolicies(input);
    expect(out.default).toHaveLength(1);
    expect(out.important).toBeUndefined();
  });

  it('returns an empty object when nothing has steps', () => {
    expect(cleanPolicies({ default: [], important: [] })).toEqual({});
  });
});
