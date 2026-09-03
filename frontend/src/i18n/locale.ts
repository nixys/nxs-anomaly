/**
 * Which languages this build speaks, and how the choice is remembered.
 *
 * The locale is stored in two places on purpose. localStorage answers "what
 * language should the very first paint use", before any request has been made —
 * including the sign-in screen, where there is no user yet. The user record
 * answers "what language did this person choose", so the choice survives a new
 * browser, a cleared profile, or a second machine. Neither alone is enough:
 * storage-only forgets the person, record-only starts every session in English
 * and then flips.
 */

export const LOCALES = ['ru-RU', 'en-US'] as const;

export type Locale = (typeof LOCALES)[number];

/**
 * Russian is first because that is the audience this deployment is built for.
 * It is also the fallback when nothing at all is known, so an operator who
 * never touches the switch gets the language the product is being sold in.
 */
export const DEFAULT_LOCALE: Locale = 'ru-RU';

const STORAGE_KEY = 'nxs-anomaly.locale';

export function isLocale(value: unknown): value is Locale {
  return typeof value === 'string' && (LOCALES as readonly string[]).includes(value);
}

/** Human name of a language, written in that language — never translated. */
export const LOCALE_LABELS: Record<Locale, string> = {
  'ru-RU': 'Русский',
  'en-US': 'English',
};

export function readStoredLocale(): Locale | null {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    return isLocale(stored) ? stored : null;
  } catch {
    // Private browsing modes can make localStorage throw on read. A missing
    // preference is not an error worth surfacing: detection covers it.
    return null;
  }
}

export function storeLocale(locale: Locale) {
  try {
    localStorage.setItem(STORAGE_KEY, locale);
  } catch {
    // As above: the language still applies to this tab, it just will not
    // survive a reload. Failing the switch over it would be worse.
  }
}

/**
 * Best guess before anything is known about the person: an explicit earlier
 * choice, then what the browser asks for, then the default.
 *
 * Browser languages are matched on the base tag, so `ru`, `ru-BY` and `ru-RU`
 * all land on Russian — a Russian speaker in Minsk wants the Russian UI, not
 * the American English one.
 */
export function detectLocale(): Locale {
  const stored = readStoredLocale();
  if (stored) return stored;
  const candidates = typeof navigator === 'undefined' ? [] : (navigator.languages ?? [navigator.language]);
  for (const candidate of candidates) {
    if (!candidate) continue;
    const base = candidate.toLowerCase().split('-')[0];
    const match = LOCALES.find((locale) => locale.toLowerCase().split('-')[0] === base);
    if (match) return match;
  }
  return DEFAULT_LOCALE;
}
