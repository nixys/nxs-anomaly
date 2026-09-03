/**
 * Date, time, zone and number formatting.
 *
 * Everything goes through `Intl`, which already knows both locales, rather than
 * through hand-written format strings: `31.12.2025, 23:45` and `12/31/2025,
 * 11:45 PM` are the same call with a different tag.
 *
 * The zone is always stated next to an absolute time. The old UI rendered every
 * timestamp in whatever zone the laptop happened to be in and said nothing
 * about it, which is precisely the failure that matters for paging: a responder
 * in Kaliningrad reading a Novosibirsk colleague's handoff time has no way to
 * tell it is not their own.
 */

import type { Locale } from './locale';

/** The zone the browser is in, used when the person has not chosen one. */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

/**
 * Falls back to the browser zone when the given name is empty or unknown.
 *
 * An unknown zone must not throw: the name comes from a user record that an API
 * client could have written anything into, and a broken profile field should
 * degrade the timestamp, not blank the page.
 */
export function resolveTimeZone(timeZone: string | null | undefined): string {
  if (!timeZone) return browserTimeZone();
  try {
    new Intl.DateTimeFormat('en-US', { timeZone }).format(0);
    return timeZone;
  } catch {
    return browserTimeZone();
  }
}

export function parseDate(value: string | number | Date | null | undefined): Date | null {
  if (value === null || value === undefined || value === '') return null;
  const date = value instanceof Date ? value : new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

export interface DateFormats {
  /** `31.12.2025, 23:45:07` — the default for a logged event. */
  dateTime(value: string | number | Date | null | undefined): string;
  /** Without seconds, for anything a person types or picks. */
  dateTimeShort(value: string | number | Date | null | undefined): string;
  date(value: string | number | Date | null | undefined): string;
  time(value: string | number | Date | null | undefined): string;
  /** `2 ч назад` / `2 hr. ago`, computed from the largest fitting unit. */
  relative(value: string | number | Date | null | undefined): string;
  /** A duration in milliseconds as `1 ч 5 мин` / `1 hr 5 min`. */
  duration(ms: number | null | undefined): string;
  number(value: number, options?: Intl.NumberFormatOptions): string;
  percent(fraction: number, fractionDigits?: number): string;
  /** Short zone name for the zone in use, e.g. `MSK`, `GMT+7`. */
  zoneLabel(value?: string | number | Date | null): string;
  /** The IANA name actually in use, after fallback. */
  timeZone: string;
}

/** Placeholder shown wherever a value is absent — one dash everywhere. */
export const EMPTY_VALUE = '—';

const RELATIVE_UNITS: Array<[Intl.RelativeTimeFormatUnit, number]> = [
  ['year', 365 * 24 * 60 * 60 * 1000],
  ['month', 30 * 24 * 60 * 60 * 1000],
  ['week', 7 * 24 * 60 * 60 * 1000],
  ['day', 24 * 60 * 60 * 1000],
  ['hour', 60 * 60 * 1000],
  ['minute', 60 * 1000],
  ['second', 1000],
];

export function createFormats(locale: Locale, timeZone: string): DateFormats {
  const zone = resolveTimeZone(timeZone);
  const dateTime = new Intl.DateTimeFormat(locale, {
    timeZone: zone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
  const dateTimeShort = new Intl.DateTimeFormat(locale, {
    timeZone: zone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
  const dateOnly = new Intl.DateTimeFormat(locale, {
    timeZone: zone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  });
  const timeOnly = new Intl.DateTimeFormat(locale, {
    timeZone: zone,
    hour: '2-digit',
    minute: '2-digit',
  });
  const zoneOnly = new Intl.DateTimeFormat(locale, { timeZone: zone, timeZoneName: 'short' });
  const relative = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' });
  const millisecondFormat = new Intl.NumberFormat(locale, {
    style: 'unit',
    unit: 'millisecond',
    unitDisplay: 'short',
  });

  const wrap =
    (formatter: Intl.DateTimeFormat) =>
    (value: string | number | Date | null | undefined): string => {
      const date = parseDate(value);
      // An unparseable timestamp is shown verbatim rather than as an empty
      // dash: if the API ever sends something unexpected, the operator should
      // see it, not a hole where a time used to be.
      if (!date) return value ? String(value) : EMPTY_VALUE;
      return formatter.format(date);
    };

  return {
    timeZone: zone,
    dateTime: wrap(dateTime),
    dateTimeShort: wrap(dateTimeShort),
    date: wrap(dateOnly),
    time: wrap(timeOnly),
    relative(value) {
      const date = parseDate(value);
      if (!date) return value ? String(value) : EMPTY_VALUE;
      const diff = date.getTime() - Date.now();
      const abs = Math.abs(diff);
      for (const [unit, ms] of RELATIVE_UNITS) {
        if (abs >= ms) return relative.format(Math.round(diff / ms), unit);
      }
      return relative.format(0, 'second');
    },
    duration(ms) {
      if (ms === null || ms === undefined || !Number.isFinite(ms)) return EMPTY_VALUE;
      const totalSeconds = Math.round(Math.abs(ms) / 1000);
      if (totalSeconds < 1) return millisecondFormat.format(Math.round(ms));
      const days = Math.floor(totalSeconds / 86400);
      const hours = Math.floor((totalSeconds % 86400) / 3600);
      const minutes = Math.floor((totalSeconds % 3600) / 60);
      const seconds = totalSeconds % 60;
      const parts: string[] = [];
      const push = (value: number, unit: string) => {
        parts.push(
          new Intl.NumberFormat(locale, {
            style: 'unit',
            unit,
            unitDisplay: 'short',
          } as Intl.NumberFormatOptions).format(value),
        );
      };
      if (days) push(days, 'day');
      if (hours) push(hours, 'hour');
      if (days) return parts.join(' ');
      // Minutes are shown alongside hours even at zero so `2 ч 0 мин` cannot be
      // misread as "exactly two hours, seconds unknown".
      if (hours || minutes) push(minutes, 'minute');
      if (!hours) push(seconds, 'second');
      return parts.join(' ');
    },
    number(value, options) {
      return new Intl.NumberFormat(locale, options).format(value);
    },
    percent(fraction, fractionDigits = 0) {
      return new Intl.NumberFormat(locale, {
        style: 'percent',
        minimumFractionDigits: fractionDigits,
        maximumFractionDigits: fractionDigits,
      }).format(fraction);
    },
    zoneLabel(value) {
      const date = parseDate(value) ?? new Date();
      const part = zoneOnly.formatToParts(date).find((p) => p.type === 'timeZoneName');
      return part?.value ?? zone;
    },
  };
}

/**
 * `YYYY-MM-DDTHH:mm` in a specific zone, which is what `<input type="datetime-local">`
 * and Mantine's date inputs expect.
 *
 * `sv-SE` is used as a formatting trick, not as a language: it is the locale
 * whose short format is already ISO-ordered, so the parts come out in the right
 * order without reassembling them by hand.
 */
export function toLocalInputValue(value: string | number | Date, timeZone: string): string {
  const date = parseDate(value);
  if (!date) return '';
  const zone = resolveTimeZone(timeZone);
  const parts = new Intl.DateTimeFormat('sv-SE', {
    timeZone: zone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(date);
  return parts.replace(' ', 'T').slice(0, 16);
}
