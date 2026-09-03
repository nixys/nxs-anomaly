import { describe, expect, it } from 'vitest';
import { formatDuration, formatInZone, toIso, toLocalInput } from './schedule-utils';

describe('toIso', () => {
  it('converts a datetime-local value to UTC ISO', () => {
    const iso = toIso('2026-06-01T12:30');
    // The input carries no zone, so the result depends on the runner's zone;
    // what must hold is that it is a valid instant naming that wall clock.
    const parsed = new Date(iso);
    expect(Number.isNaN(parsed.getTime())).toBe(false);
    expect(iso.endsWith('Z')).toBe(true);
    expect(parsed.getHours()).toBe(12);
    expect(parsed.getMinutes()).toBe(30);
  });

  it('returns an empty string for empty input', () => {
    expect(toIso('')).toBe('');
  });

  it('passes an unparseable value through instead of inventing a date', () => {
    expect(toIso('not a date')).toBe('not a date');
  });
});

describe('toLocalInput', () => {
  it('round-trips with toIso', () => {
    const original = '2026-06-01T12:30';
    expect(toLocalInput(toIso(original))).toBe(original);
  });

  it('pads every field to the width the input requires', () => {
    const value = toLocalInput(new Date(2026, 0, 2, 3, 4).toISOString());
    expect(value).toBe('2026-01-02T03:04');
  });

  it('returns an empty string for missing or invalid values', () => {
    expect(toLocalInput(undefined)).toBe('');
    expect(toLocalInput('')).toBe('');
    expect(toLocalInput('nonsense')).toBe('');
  });
});

describe('formatDuration', () => {
  it('reports hours below two days and days above', () => {
    expect(formatDuration(3600)).toBe('1h');
    expect(formatDuration(8 * 3600)).toBe('8h');
    expect(formatDuration(47 * 3600)).toBe('47h');
    expect(formatDuration(48 * 3600)).toBe('2d');
    expect(formatDuration(7 * 24 * 3600)).toBe('7d');
  });
});

describe('formatInZone', () => {
  it('renders an instant in the schedule timezone, not the viewer one', () => {
    // 08:00 UTC is 10:00 in Berlin (CEST) and 17:00 in Tokyo.
    expect(formatInZone('2026-06-01T08:00:00Z', 'Europe/Berlin')).toBe('2026-06-01 10:00');
    expect(formatInZone('2026-06-01T08:00:00Z', 'Asia/Tokyo')).toBe('2026-06-01 17:00');
    expect(formatInZone('2026-06-01T08:00:00Z', 'UTC')).toBe('2026-06-01 08:00');
  });

  it('keeps the wall clock across a DST change in that zone', () => {
    // The same 10:00 Berlin handoff before and after the March transition.
    expect(formatInZone('2026-03-27T09:00:00Z', 'Europe/Berlin')).toBe('2026-03-27 10:00');
    expect(formatInZone('2026-03-30T08:00:00Z', 'Europe/Berlin')).toBe('2026-03-30 10:00');
  });

  it('falls back instead of throwing on an unknown zone', () => {
    expect(formatInZone('2026-06-01T08:00:00Z', 'Mars/Olympus')).toMatch(/^2026-06-01 /);
  });

  it('handles missing and invalid input', () => {
    expect(formatInZone(undefined, 'UTC')).toBe('—');
    expect(formatInZone('nonsense', 'UTC')).toBe('nonsense');
  });
});
