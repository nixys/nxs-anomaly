import type { MantineColor } from '@mantine/core';

/**
 * The severity vocabulary, in one place.
 *
 * The service stores the word the source sent: Prometheus says `critical`,
 * Zabbix says `high`, a hand-written webhook says `P1`. Ordering, filtering and
 * colour all have to agree on what those words mean, and before this file they
 * did not — the filter offered five words, the badge coloured eight, and the
 * server ranked five, so an alert stored as `high` was painted red, matched no
 * filter, and sorted below `debug`.
 *
 * LEVELS is what the interface offers and what the server filters by; ALIASES
 * says which spelling belongs to which level. This table mirrors
 * `severityAliases` in internal/store/store.go — the server needs it to filter
 * and sort, this file needs it to colour a badge without asking. Change one,
 * change both.
 */
export const SEVERITY_LEVELS = ['critical', 'error', 'warning', 'info', 'debug'] as const;

export type SeverityLevel = (typeof SEVERITY_LEVELS)[number];

const ALIASES: Record<SeverityLevel, string[]> = {
  critical: ['critical', 'crit', 'fatal', 'emergency', 'disaster', 'sev1', 'p1'],
  error: ['error', 'err', 'high', 'major', 'sev2', 'p2'],
  warning: ['warning', 'warn', 'medium', 'average', 'minor', 'sev3', 'p3'],
  info: ['info', 'informational', 'notice', 'low', 'sev4', 'p4'],
  debug: ['debug', 'trace', 'sev5', 'p5'],
};

const LEVEL_OF: Record<string, SeverityLevel> = Object.fromEntries(
  SEVERITY_LEVELS.flatMap((level) => ALIASES[level].map((alias) => [alias, level])),
) as Record<string, SeverityLevel>;

/**
 * The level a stored severity belongs to, or `null` for a word this service
 * does not model.
 *
 * `null` is deliberate rather than a fallback to `info`: an unrecognised
 * severity shows as itself in a neutral badge, which is honest and is a visible
 * prompt to add the spelling here. Guessing a level for it would put an
 * incident in a queue position nobody asked for.
 */
export function severityLevel(value: string | null | undefined): SeverityLevel | null {
  if (!value) return null;
  return LEVEL_OF[value.trim().toLowerCase()] ?? null;
}

/** How much a severity matters, highest first. Unknown words rank last. */
export function severityRank(value: string | null | undefined): number {
  const level = severityLevel(value);
  return level === null ? 0 : SEVERITY_LEVELS.length - SEVERITY_LEVELS.indexOf(level);
}

/**
 * Colour per level, not per spelling — `high` and `error` are one incident
 * class and must not read as two.
 */
export const SEVERITY_COLOR: Record<SeverityLevel, MantineColor> = {
  critical: 'red',
  error: 'orange',
  warning: 'yellow',
  info: 'blue',
  debug: 'gray',
};

/**
 * A shape per level, carried alongside the colour.
 *
 * Colour alone fails the two readers who most need this column: someone with a
 * colour vision deficiency, and anyone reading a screenshot pasted into a chat
 * that stripped the theme. The glyph is redundant for everyone else, which is
 * the point.
 */
export const SEVERITY_GLYPH: Record<SeverityLevel, string> = {
  critical: '▲',
  error: '◆',
  warning: '■',
  info: '●',
  debug: '·',
};

/** The CSS colour of a level, for the stripe on a list row. */
export function severityStripe(value: string | null | undefined): string {
  const level = severityLevel(value);
  if (level === null) return 'transparent';
  return `var(--mantine-color-${SEVERITY_COLOR[level]}-6)`;
}
