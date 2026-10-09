import { describe, expect, it } from 'vitest';
import {
  formatCalendarDay,
  formatDuration,
  formatHour,
  formatInZone,
  splitAtMidnights,
  toIso,
  toLocalInput,
} from './schedule-utils';

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

describe('times in the schedule zone', () => {
  it('reads a datetime-local value as a wall clock in the schedule zone', () => {
    expect(toIso('2026-10-08T10:00', 'Europe/Berlin')).toBe('2026-10-08T08:00:00.000Z');
    expect(toIso('2026-10-08T10:00', 'Asia/Kathmandu')).toBe('2026-10-08T04:15:00.000Z');
    expect(toLocalInput('2026-10-08T04:15:00.000Z', 'Asia/Kathmandu')).toBe('2026-10-08T10:00');
  });

  it('round-trips across the night Berlin leaves DST', () => {
    const iso = toIso('2026-10-25T05:00', 'Europe/Berlin');
    expect(iso).toBe('2026-10-25T04:00:00.000Z');
    expect(toLocalInput(iso, 'Europe/Berlin')).toBe('2026-10-25T05:00');
  });

  it('leaves an unsaved edit as typed', () => {
    expect(toLocalInput('2026-10-08T10:00', 'Asia/Kolkata')).toBe('2026-10-08T10:00');
  });
});

describe('splitAtMidnights', () => {
  it('cuts at the local midnight of a half-hour zone', () => {
    // 15:30 Kolkata to 03:00 next day; the old UTC-hour arithmetic cut at 00:30.
    const pieces = splitAtMidnights('2026-10-08T10:00:00Z', '2026-10-08T21:30:00Z', 'Asia/Kolkata');
    expect(pieces).toEqual([
      { day: '2026-10-08', from: 15.5, to: 24 },
      { day: '2026-10-09', from: 0, to: 3 },
    ]);
  });

  it('cuts at midnight, not 23:00, on the 25-hour day Berlin leaves DST', () => {
    // 2026-10-25 02:30 CEST to 2026-10-26 06:00 CET.
    const pieces = splitAtMidnights('2026-10-25T00:30:00Z', '2026-10-26T05:00:00Z', 'Europe/Berlin');
    expect(pieces).toEqual([
      { day: '2026-10-25', from: 2.5, to: 24 },
      { day: '2026-10-26', from: 0, to: 6 },
    ]);
  });

  it('keeps minutes and covers a whole day', () => {
    expect(splitAtMidnights('2026-03-29T00:00:00Z', '2026-03-29T00:10:00Z', 'UTC')).toEqual([
      { day: '2026-03-29', from: 0, to: 10 / 60 },
    ]);
    expect(splitAtMidnights('2026-10-07T22:00:00Z', '2026-10-08T22:00:00Z', 'Europe/Berlin')).toEqual([
      { day: '2026-10-08', from: 0, to: 24 },
    ]);
  });
});

describe('calendar labels', () => {
  it('formats minutes in the tooltip', () => {
    expect(formatHour(15.5)).toBe('15:30');
    expect(formatHour(24)).toBe('24:00');
  });

  it('formats a calendar date without moving it through a zone', () => {
    expect(formatCalendarDay('2026-10-09', 'en-CA')).toBe('2026-10-09');
  });
});
