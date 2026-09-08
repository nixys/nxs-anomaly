import { describe, expect, it } from 'vitest';
import { SEVERITY_COLOR, SEVERITY_LEVELS, severityLevel, severityRank } from './severity';

// The defect this file exists for: an alert stored as "high" was painted red by
// one table, ranked 0 by another and offered by no filter at all. All three now
// come from this module.
describe('severity vocabulary', () => {
  it('maps the spellings sources actually send onto levels', () => {
    expect(severityLevel('high')).toBe('error');
    expect(severityLevel('medium')).toBe('warning');
    expect(severityLevel('low')).toBe('info');
    expect(severityLevel('P1')).toBe('critical');
    expect(severityLevel(' Critical ')).toBe('critical');
  });

  it('leaves a word it does not model alone instead of guessing', () => {
    // Guessing would place an incident in a queue position nobody asked for;
    // null is what makes the badge show the raw word in neutral grey.
    expect(severityLevel('wobbly')).toBeNull();
    expect(severityLevel('')).toBeNull();
    expect(severityLevel(undefined)).toBeNull();
  });

  it('ranks by what a level means, not by how it spells', () => {
    expect(severityRank('high')).toBeGreaterThan(severityRank('debug'));
    expect(severityRank('high')).toBeGreaterThan(severityRank('warning'));
    expect(severityRank('critical')).toBeGreaterThan(severityRank('high'));
    expect(severityRank('debug')).toBeGreaterThan(severityRank('wobbly'));
  });

  it('gives aliases of one level the same rank and colour', () => {
    expect(severityRank('high')).toBe(severityRank('error'));
    const level = severityLevel('high');
    expect(level && SEVERITY_COLOR[level]).toBe(SEVERITY_COLOR.error);
  });

  it('offers exactly the five levels the server filters by', () => {
    expect([...SEVERITY_LEVELS]).toEqual(['critical', 'error', 'warning', 'info', 'debug']);
  });
});
