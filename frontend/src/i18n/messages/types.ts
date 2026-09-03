/**
 * The grammatical forms a counted message can take.
 *
 * The names come from CLDR, which is what `Intl.PluralRules` reports. English
 * uses `one` and `other`; Russian uses `one` (1, 21, 31…), `few` (2–4, 22–24…)
 * and `many` (0, 5–20, 25–30…). `other` is required because it is the form
 * `Intl` falls back to, and because fractional counts select it in both
 * languages.
 */
export interface PluralForms {
  zero?: string;
  one?: string;
  two?: string;
  few?: string;
  many?: string;
  other: string;
}
