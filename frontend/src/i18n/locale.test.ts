import { afterEach, describe, expect, it, vi } from 'vitest';
import { DEFAULT_LOCALE, detectLocale } from './locale';

function stubBrowser(languages: string[], stored: string | null = null) {
  vi.stubGlobal('navigator', { languages, language: languages[0] });
  vi.stubGlobal('localStorage', { getItem: () => stored, setItem: () => {} });
}

describe('detectLocale', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('keeps an explicit earlier choice over the browser', () => {
    stubBrowser(['en-US'], 'ru-RU');
    expect(detectLocale()).toBe('ru-RU');
  });

  it('follows a supported browser language, matched on the base tag', () => {
    stubBrowser(['ru-BY']);
    expect(detectLocale()).toBe('ru-RU');
    stubBrowser(['en-GB']);
    expect(detectLocale()).toBe('en-US');
  });

  it('falls back to the edition default when the browser names neither language', () => {
    stubBrowser(['de-DE', 'fr']);
    expect(detectLocale()).toBe(DEFAULT_LOCALE);
  });
});

describe('DEFAULT_LOCALE', () => {
  // Community is published for everyone and falls back to English; Enterprise is
  // sold in Russian. The community cut strips the marked line, so this one test
  // checks both editions and that the cut removed the override.
  it('matches the edition', () => {
    let expected = 'en-US';
    expect(DEFAULT_LOCALE).toBe(expected);
  });
});
