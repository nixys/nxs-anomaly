/**
 * Time conversions shared by the schedule pages.
 *
 * They live outside the component so the round trip between what the backend
 * stores (ISO 8601, UTC) and what a `datetime-local` input speaks (wall clock,
 * no zone) can be tested directly — it is the part that silently corrupts an
 * override window when it is wrong.
 */

/** ISO string accepted by utils.ParseDatetime, as produced by datetime-local inputs. */
export function toIso(localValue: string): string {
  if (!localValue) return '';
  const date = new Date(localValue);
  return Number.isNaN(date.getTime()) ? localValue : date.toISOString();
}

/** ISO string back to the `YYYY-MM-DDTHH:mm` a datetime-local input expects. */
export function toLocalInput(iso: string | undefined): string {
  if (!iso) return '';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(
    date.getHours(),
  )}:${pad(date.getMinutes())}`;
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
