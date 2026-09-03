/**
 * Translating the vocabulary the API speaks.
 *
 * Alert-group statuses, severities, channels, roles and priorities arrive as
 * stable wire values (`retry_scheduled`, `critical`, `responder`). They are the
 * words an operator judges an incident by, so they are translated rather than
 * shown raw or prettified by replacing underscores with spaces.
 *
 * An unknown value falls through to itself rather than to a placeholder. A new
 * status added by the backend should appear in the UI as its wire name — odd
 * but honest, and immediately obvious as something to add here — rather than as
 * "unknown", which hides it.
 */

import { useCallback } from 'react';
import { useI18n } from './I18nProvider';
import type { Messages } from './messages';

type Key = keyof Messages;

function useLookup(prefix: string) {
  const { t } = useI18n();
  return useCallback(
    (value: string | null | undefined): string => {
      if (value === null || value === undefined) return '';
      const key = `${prefix}${value}` as Key;
      // The catalog is a plain object, so a missing key is `undefined` rather
      // than a throw — which is what lets an unrecognised wire value pass
      // through unchanged.
      const translated = (t as (k: Key) => string | undefined)(key);
      return translated ?? value;
    },
    [t, prefix],
  );
}

/** `open` → «открыта`; `retry_scheduled` → «повтор запланирован». */
export function useStatusLabel() {
  return useLookup('status.');
}

export function useSeverityLabel() {
  return useLookup('severity.');
}

/** `telegram` → «Telegram»; `call` → «звонок». */
export function useChannelLabel() {
  return useLookup('channel.');
}

/** `''` (no role) is a real value here, and has its own entry. */
export function useRoleLabel() {
  return useLookup('role.');
}

export function usePriorityLabel() {
  return useLookup('priority.');
}
