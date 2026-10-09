/**
 * Time conversions shared by the schedule pages.
 *
 * They live outside the component so the round trip between what the backend
 * stores (ISO 8601, UTC) and what a `datetime-local` input speaks (wall clock,
 * no zone) can be tested directly — it is the part that silently corrupts an
 * override window when it is wrong.
 */

// What a datetime-local input holds: a wall clock with no zone.
const WALL_CLOCK = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/;

const pad = (n: number) => String(n).padStart(2, '0');

/** `YYYY-MM-DDTHH:mm` of an instant in `timezone`, or in the browser's zone. */
function wallClockOf(date: Date, timezone: string | undefined): string {
  if (timezone) {
    try {
      return new Intl.DateTimeFormat('sv-SE', {
        timeZone: timezone,
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        hour12: false,
      })
        .format(date)
        .replace(' ', 'T')
        .slice(0, 16);
    } catch {
      // Unknown zone name: fall through to the browser's zone.
    }
  }
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}`;
}

const wallClockAsUtc = (value: string) => Date.parse(`${value}:00Z`);

/**
 * ISO string accepted by utils.ParseDatetime from what a datetime-local input
 * holds, read as a wall clock in `timezone` — the schedule's zone, the same one
 * the preview and the calendar are drawn in. Without a zone it is the browser's.
 * A value that is not a bare wall clock (an ISO string nobody edited) passes
 * through as the instant it already is.
 */
export function toIso(localValue: string, timezone?: string): string {
  if (!localValue) return '';
  if (!WALL_CLOCK.test(localValue) || !timezone) {
    const date = new Date(localValue);
    return Number.isNaN(date.getTime()) ? localValue : date.toISOString();
  }
  const target = wallClockAsUtc(localValue);
  // The zone's offset depends on the instant, which is what we are solving
  // for; two rounds settle it, DST transitions included.
  const offsetAt = (ms: number) => wallClockAsUtc(wallClockOf(new Date(ms), timezone)) - ms;
  let ms = target - offsetAt(target);
  ms = target - offsetAt(ms);
  return new Date(ms).toISOString();
}

/**
 * ISO string back to the `YYYY-MM-DDTHH:mm` a datetime-local input expects, as
 * a wall clock in `timezone` (the browser's when absent). A value that already
 * is a wall clock — an edit not yet saved — is returned unchanged.
 */
export function toLocalInput(iso: string | undefined, timezone?: string): string {
  if (!iso) return '';
  if (WALL_CLOCK.test(iso)) return iso;
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  return wallClockOf(date, timezone);
}

/** Segment length for the preview table: hours up to two days, then days. */
export function formatDuration(seconds: number): string {
  const hours = Math.round(seconds / 3600);
  if (hours < 48) return `${hours}h`;
  return `${Math.round(hours / 24)}d`;
}

/**
 * Wall-clock time in the schedule's own timezone.
 *
 * The preview is a statement about the schedule, and a schedule in
 * Europe/Berlin hands over at 10:00 Berlin whoever is reading it. Rendering
 * those instants in the viewer's zone made a correct schedule look wrong to
 * anyone travelling or working remotely. Falls back to the viewer's zone if the
 * runtime does not know the zone name.
 */
export function formatInZone(
  iso: string | undefined,
  timezone: string | undefined,
  locale = 'sv-SE',
): string {
  if (!iso) return '—';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  try {
    return new Intl.DateTimeFormat(locale, {
      timeZone: timezone || undefined,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    }).format(date);
  } catch {
    return new Intl.DateTimeFormat(locale, {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    }).format(date);
  }
}

/** One row's share of a segment: a calendar day in the zone and hours 0–24. */
export type DayPiece = { day: string; from: number; to: number };

/**
 * Cuts an interval at every local midnight of `timezone`, for the calendar's
 * one-row-per-day grid.
 *
 * The next midnight is found on the zone's own clock. Adding whole UTC hours to
 * reach it gave 00:30 in Asia/Kolkata and 23:00 on the night Berlin leaves DST.
 * Hours are wall-clock hours, minutes kept: a 10-minute override is 1/144 of a
 * row, not an hour.
 */
export function splitAtMidnights(start: string, end: string, timezone: string): DayPiece[] {
  const dayFmt = new Intl.DateTimeFormat('en-CA', { timeZone: timezone, dateStyle: 'short' });
  const timeFmt = new Intl.DateTimeFormat('en-GB', {
    timeZone: timezone,
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
  const dayKey = (date: Date) => dayFmt.format(date);
  const hourOfDay = (date: Date) => {
    const parts = timeFmt.formatToParts(date);
    const hour = Number(parts.find((p) => p.type === 'hour')?.value ?? '0') % 24;
    const minute = Number(parts.find((p) => p.type === 'minute')?.value ?? '0');
    return hour + minute / 60;
  };
  const shift = (date: Date, hours: number) => new Date(date.getTime() + hours * 3_600_000);

  const nextMidnight = (cursor: Date) => {
    const day = dayKey(cursor);
    let at = shift(cursor, 24 - hourOfDay(cursor));
    at.setUTCSeconds(0, 0);
    // A DST day is 23 or 25 hours long, so the first guess can fall short of
    // midnight or past it; correct on the zone's clock.
    for (let i = 0; i < 3; i += 1) {
      const hour = hourOfDay(at);
      if (dayKey(at) === day) {
        at = shift(at, 24 - hour);
      } else if (hour > 0) {
        const back = shift(at, -hour);
        // A zone that skips midnight starts its day at 01:00; that is the edge.
        if (dayKey(back) === day) break;
        at = back;
      } else {
        break;
      }
    }
    return at;
  };

  const pieces: DayPiece[] = [];
  let cursor = new Date(start);
  const stop = new Date(end);
  // Backend previews are capped at 180 days; the guard only stops a malformed
  // segment from spinning.
  for (let guard = 0; cursor < stop && guard < 400; guard += 1) {
    const day = dayKey(cursor);
    const from = hourOfDay(cursor);
    const midnight = nextMidnight(cursor);
    const sliceEnd = midnight < stop ? midnight : stop;
    const to = dayKey(sliceEnd) === day ? hourOfDay(sliceEnd) : 24;
    pieces.push({ day, from, to: to <= from ? 24 : to });
    cursor = sliceEnd;
  }
  return pieces;
}

/** `HH:MM` of a wall-clock hour in 0–24, as the calendar's tooltip shows it. */
export function formatHour(hours: number): string {
  const total = Math.round(hours * 60);
  return `${String(Math.floor(total / 60)).padStart(2, '0')}:${String(total % 60).padStart(2, '0')}`;
}

/**
 * A calendar date (`YYYY-MM-DD`, already a day in the schedule's zone) in the
 * reader's locale. Formatted in UTC on purpose: it is a date, not an instant,
 * and passing it through the viewer's zone moved it to the day before for
 * anyone west of Greenwich.
 */
export function formatCalendarDay(day: string, locale: string): string {
  const date = new Date(`${day}T00:00:00Z`);
  if (Number.isNaN(date.getTime())) return day;
  return new Intl.DateTimeFormat(locale, {
    timeZone: 'UTC',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).format(date);
}
