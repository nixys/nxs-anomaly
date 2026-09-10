import type { EscalationStep } from '../api/types';
import type { StringKey, TranslateParams } from '../i18n/I18nProvider';

/**
 * A chain, read as a sentence.
 *
 * The card used to show `1. NOTIFY_USER  2. NOTIFY_USER` — the wire names of the
 * step kinds, in the order they run. That is the configuration deciding whether
 * anybody is woken at three in the morning, displayed as constants from the
 * source code, with the one fact that matters — *who* — missing entirely.
 *
 * Each step becomes a short phrase naming its participants; the phrases are
 * joined with arrows. Names are resolved by the caller, which already holds the
 * user, team and schedule lists.
 */
export interface ChainNames {
  user: (id: string) => string;
  team: (id: string) => string;
  schedule: (id: string) => string;
}

export function stepSentence(
  step: EscalationStep,
  names: ChainNames,
  t: (key: StringKey, params?: TranslateParams) => string,
): string {
  const list = (ids: unknown, resolve: (id: string) => string) =>
    (Array.isArray(ids) ? (ids as string[]) : []).map(resolve).join(', ');

  switch (step.kind) {
    case 'WAIT':
      return t('chains.say.wait', { minutes: String(step.delay_minutes ?? 0) });
    case 'NOTIFY_USER': {
      const who = list(step.user_ids, names.user);
      return who ? t('chains.say.notifyUser', { who }) : t('chains.say.notifyUserNobody');
    }
    case 'NOTIFY_SCHEDULE': {
      const who = list(step.schedule_ids, names.schedule);
      return who ? t('chains.say.notifySchedule', { who }) : t('chains.say.notifyScheduleNobody');
    }
    case 'NOTIFY_TEAM': {
      const who = list(step.team_ids, names.team);
      return who ? t('chains.say.notifyTeam', { who }) : t('chains.say.notifyTeamNobody');
    }
    case 'NOTIFY_EMERGENCY':
      return t('chains.say.notifyEmergency');
    case 'NOTIFY_DUTY_USERS':
      return t('chains.say.notifyDuty');
    case 'TRIGGER_WEBHOOK':
      return t('chains.say.webhook');
    case 'CREATE_ISSUE':
      return t('chains.say.issue');
    case 'RESOLVE':
      return t('chains.say.resolve');
    case 'REPEAT':
      return t('chains.say.repeat', { times: String(step.repeat_times ?? 1) });
    default:
      return String(step.kind);
  }
}

/**
 * Whether a step actually reaches somebody.
 *
 * A chain whose notifying steps all name nobody is a chain that pages nobody,
 * and it looks exactly like a working one on the card. This is what lets the UI
 * say so.
 */
export function stepReachesNobody(step: EscalationStep): boolean {
  const empty = (ids: unknown) => !Array.isArray(ids) || ids.length === 0;
  switch (step.kind) {
    case 'NOTIFY_USER':
      return empty(step.user_ids);
    case 'NOTIFY_SCHEDULE':
      return empty(step.schedule_ids);
    case 'NOTIFY_TEAM':
      return empty(step.team_ids);
    default:
      return false;
  }
}

/** True when nothing in the chain can page a person. */
export function chainPagesNobody(steps: EscalationStep[] | null | undefined): boolean {
  const notifying = (steps ?? []).filter((step) => String(step.kind).startsWith('NOTIFY'));
  if (notifying.length === 0) return true;
  return notifying.every(stepReachesNobody);
}
