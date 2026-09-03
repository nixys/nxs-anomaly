import { describe, expect, it } from 'vitest';
import { createFormats, resolveTimeZone } from './format';

describe('localized date and duration formatting', () => {
  const instant = '2025-12-31T20:45:00Z';

  it('uses the selected locale in the selected timezone', () => {
    const ru = createFormats('ru-RU', 'Asia/Novosibirsk').dateTimeShort(instant);
    const en = createFormats('en-US', 'Asia/Novosibirsk').dateTimeShort(instant);

    expect(ru).toContain('01.01.2026');
    expect(en).toMatch(/01\/01\/2026|1\/1\/2026/);
    expect(ru).not.toBe(en);
  });

  it('formats durations with locale-aware units', () => {
    expect(createFormats('ru-RU', 'UTC').duration(3_900_000)).toContain('ч');
    expect(createFormats('en-US', 'UTC').duration(3_900_000)).toMatch(/hr|hour/);
  });

  it('falls back instead of throwing on an invalid saved timezone', () => {
    expect(() => resolveTimeZone('Mars/Olympus')).not.toThrow();
  });
});
