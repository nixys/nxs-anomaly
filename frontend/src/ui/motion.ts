/**
 * The motion vocabulary of this product, in one place.
 *
 * Two rules decide everything here. Movement is allowed where it answers "what
 * just changed and why" — a row that arrived, a status that flipped, a panel
 * that appeared because something is selected. Movement is forbidden where a
 * person compares values: the severity stripe, the level badge and the numbers
 * in a table never animate, because a moving thing cannot be compared with a
 * still one.
 *
 * Durations are short by default. The one long animation is the arrival
 * highlight, and it is long on purpose: it has to survive long enough to be
 * noticed by someone reading another part of the screen.
 */
export const DURATION = {
  /** Hover, focus, colour changes — below this it reads as a rendering glitch. */
  hover: 120,
  /** A badge or icon changing meaning in place. */
  swap: 180,
  /** A panel appearing or leaving. */
  panel: 200,
  /** Progressive disclosure: height plus opacity. */
  disclose: 220,
  /** The sidebar rail, and anything else that resizes layout. */
  layout: 260,
  /** A number counting to its new value. */
  count: 400,
  /** The arrival highlight on a new row. */
  arrival: 2400,
} as const;

/** Decelerating curve: things settle rather than stop. */
export const EASE_SETTLE = 'cubic-bezier(0.22, 0.68, 0.24, 1)';
/** Plain ease for colour and opacity, where a curve would not be noticed. */
export const EASE_PLAIN = 'ease';

/**
 * A transition string, or `undefined` when the viewer asked for less motion.
 *
 * `undefined` rather than a shorter duration: reduced motion is an accessibility
 * setting, and for some people it is a medical one. Halving an animation still
 * animates.
 */
export function transition(
  properties: string | string[],
  ms: number,
  options: { reduce?: boolean; ease?: string; delay?: number } = {},
): string | undefined {
  if (options.reduce) return undefined;
  const ease = options.ease ?? EASE_SETTLE;
  const delay = options.delay ? ` ${options.delay}ms` : '';
  const list = Array.isArray(properties) ? properties : [properties];
  return list.map((property) => `${property} ${ms}ms ${ease}${delay}`).join(', ');
}
