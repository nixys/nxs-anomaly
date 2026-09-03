import type { Locale } from '../locale';
import { en } from './en';
import { ru } from './ru';

export type { PluralForms } from './types';

/**
 * The set of keys the app may translate, derived from the English catalog.
 *
 * Every other locale is declared as `Messages`, which is what turns a forgotten
 * translation into a build failure instead of an English word appearing in the
 * middle of a Russian sentence.
 */
export type Messages = typeof en;

export const CATALOGS: Record<Locale, Messages> = {
  'en-US': en,
  'ru-RU': ru,
};
