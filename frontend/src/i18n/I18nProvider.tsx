import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import { useAuth } from '../auth/AuthProvider';
import { savePreferences } from '../api/client';
import { createFormats, type DateFormats, browserTimeZone } from './format';
import {
  DEFAULT_LOCALE,
  detectLocale,
  isLocale,
  readStoredLocale,
  storeLocale,
  type Locale,
} from './locale';
import { CATALOGS, type Messages, type PluralForms } from './messages';

/**
 * Keys whose value is a plain string, separated from keys whose value is a set
 * of plural forms.
 *
 * The split is what makes a missed plural a compile error rather than a string
 * reading `[object Object]` in production: `t('groups.count')` will not type
 * check, and `plural('page.title', 1)` will not either.
 */
/** Keys whose catalog entry is a plain string, i.e. what `t` accepts. Exported
 *  so a component that takes a key as a prop can type it. */
export type StringKey = { [K in keyof Messages]: Messages[K] extends string ? K : never }[keyof Messages];
type PluralKey = {
  [K in keyof Messages]: Messages[K] extends PluralForms ? K : never;
}[keyof Messages];

export type TranslateParams = Record<string, string | number>;

export interface I18nContextValue {
  locale: Locale;
  /** Changes the language for this tab and, for a signed-in person, for good. */
  setLocale: (locale: Locale) => void;
  t: (key: StringKey, params?: TranslateParams) => string;
  /** Picks the right grammatical form for `count` and substitutes `{count}`. */
  plural: (key: PluralKey, count: number, params?: TranslateParams) => string;
  /** Date, time, zone and number formatting bound to the current locale and zone. */
  fmt: DateFormats;
  /** IANA zone in use, after falling back to the browser's. */
  timeZone: string;
}

function interpolate(template: string, params?: TranslateParams): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (match, name: string) => {
    const value = params[name];
    // An unknown placeholder is left as written rather than blanked: seeing
    // `{count}` in the UI points straight at the bug, an empty gap does not.
    return value === undefined ? match : String(value);
  });
}

// Isolated component tests and embeds may render a page without the application
// root. Give those trees a real, browser-detected read-only context; the normal
// application always overrides it with I18nProvider below. This keeps i18n from
// turning every leaf-component test into an authentication/provider fixture.
const standaloneLocale = detectLocale();
const standaloneCatalog = CATALOGS[standaloneLocale] ?? CATALOGS[DEFAULT_LOCALE];
const standaloneFormats = createFormats(standaloneLocale, browserTimeZone());
const standalonePluralRules = new Intl.PluralRules(standaloneLocale);
const I18nContext = createContext<I18nContextValue>({
  locale: standaloneLocale,
  setLocale: () => {},
  timeZone: standaloneFormats.timeZone,
  fmt: standaloneFormats,
  t: (key, params) => interpolate(standaloneCatalog[key] as string, params),
  plural: (key, count, params) => {
    const forms = standaloneCatalog[key] as PluralForms;
    const form = forms[standalonePluralRules.select(count)] ?? forms.other;
    return interpolate(form, { count, ...params });
  },
});

export function I18nProvider({ children }: { children: ReactNode }) {
  const { identity } = useAuth();
  const [locale, setLocaleState] = useState<Locale>(() => detectLocale());

  // The stored choice wins on first paint; the saved one wins once the person
  // is known. Only a stored *explicit* choice survives that: someone who set
  // Russian on this laptop but English in their profile is being told two
  // different things, and the profile is the one they can carry between
  // machines. A locale that was merely detected from the browser must not
  // outrank it.
  useEffect(() => {
    const saved = identity?.locale;
    if (isLocale(saved) && saved !== locale && readStoredLocale() === null) {
      setLocaleState(saved);
      storeLocale(saved);
    }
  }, [identity?.locale, locale]);

  const setLocale = useCallback(
    (next: Locale) => {
      setLocaleState(next);
      storeLocale(next);
      // Persisting is best effort and deliberately not awaited: the language
      // has already changed on screen, and a failed write must not make the
      // switch feel broken. It is retried implicitly the next time it is used.
      if (identity?.kind === 'user' && identity.id) {
        void savePreferences({ locale: next }).catch(() => {});
      }
    },
    [identity?.kind, identity?.id],
  );

  const timeZone = identity?.timezone || browserTimeZone();

  const value = useMemo<I18nContextValue>(() => {
    const catalog = CATALOGS[locale] ?? CATALOGS[DEFAULT_LOCALE];
    const fmt = createFormats(locale, timeZone);
    const pluralRules = new Intl.PluralRules(locale);
    return {
      locale,
      setLocale,
      timeZone: fmt.timeZone,
      fmt,
      t: (key, params) => interpolate(catalog[key] as string, params),
      plural: (key, count, params) => {
        const forms = catalog[key] as PluralForms;
        // Intl knows that Russian needs one/few/many where English needs
        // one/other, so the rule lives with the language rather than in an
        // `if (count === 1)` written by hand in every call site.
        const category = pluralRules.select(count);
        const form = forms[category] ?? forms.other;
        return interpolate(form, { count, ...params });
      },
    };
  }, [locale, setLocale, timeZone]);

  // Keeps assistive technology and the browser's own text handling — hyphenation,
  // spellcheck, quotation marks — in step with what is on screen.
  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nContextValue {
  return useContext(I18nContext);
}

/** Shorthand for the common case of needing only the translate function. */
export function useT() {
  return useI18n().t;
}
