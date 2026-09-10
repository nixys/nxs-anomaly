import { Box, Group, Paper, Stack, Text, Tooltip } from '@mantine/core';
import type { ScheduleSegment } from '../api/types';
import { useI18n } from '../i18n/I18nProvider';

/**
 * Four weeks of a rota as a grid, one row per day.
 *
 * The same intervals were already on the page as a table of start/end pairs, and
 * a table is the wrong instrument for the question people bring here: *where are
 * the holes, and who is standing two shifts in a row*. Both are shapes. A hole is
 * a gap in a band; a double shift is one colour running across two rows.
 *
 * The bands are positioned by time of day, so a night shift crosses the right
 * edge and continues on the next row — which is what it does in life.
 */
export function ScheduleCalendar({
  segments,
  timezone,
  names,
}: {
  segments: ScheduleSegment[];
  timezone: string;
  names: (ids: string[]) => string;
}) {
  const { t, fmt } = useI18n();
  if (segments.length === 0) return null;

  // Colour per person, taken from the palette the rest of the product already
  // uses. Identity never rests on colour alone: every band carries the name in
  // its tooltip, and gaps are labelled.
  const palette = ['blue', 'grape', 'teal', 'indigo', 'cyan', 'pink', 'violet'];
  const colorOf = new Map<string, string>();
  const colorFor = (ids: string[]) => {
    const key = ids.join(',');
    if (!colorOf.has(key)) colorOf.set(key, palette[colorOf.size % palette.length]);
    return colorOf.get(key) as string;
  };

  const dayKey = (date: Date) =>
    new Intl.DateTimeFormat('en-CA', { timeZone: timezone, dateStyle: 'short' }).format(date);
  const hourOfDay = (date: Date) => {
    const parts = new Intl.DateTimeFormat('en-GB', {
      timeZone: timezone,
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    }).formatToParts(date);
    const hour = Number(parts.find((p) => p.type === 'hour')?.value ?? '0');
    const minute = Number(parts.find((p) => p.type === 'minute')?.value ?? '0');
    return hour + minute / 60;
  };

  // Split every segment at midnight so each piece belongs to exactly one row.
  type Piece = { day: string; from: number; to: number; segment: ScheduleSegment };
  const pieces: Piece[] = [];
  for (const segment of segments) {
    let cursor = new Date(segment.start);
    const end = new Date(segment.end);
    let guard = 0;
    while (cursor < end && guard < 80) {
      guard += 1;
      const day = dayKey(cursor);
      const from = hourOfDay(cursor);
      const midnight = new Date(cursor);
      midnight.setUTCHours(midnight.getUTCHours() + Math.ceil(24 - from));
      midnight.setUTCMinutes(0, 0, 0);
      const sliceEnd = midnight < end ? midnight : end;
      const to = dayKey(sliceEnd) === day ? hourOfDay(sliceEnd) : 24;
      pieces.push({ day, from, to: to <= from ? 24 : to, segment });
      cursor = sliceEnd;
    }
  }

  const days = Array.from(new Set(pieces.map((piece) => piece.day))).sort();

  return (
    <Paper withBorder p="lg">
      <Group justify="space-between" mb="sm">
        <Text fw={600} size="sm">
          {t('schedule.calendar')}
        </Text>
        <Text size="xs" c="dimmed">
          {t('schedule.timesIn', { timezone })}
        </Text>
      </Group>

      {/* Hour ruler: only 00, 06, 12, 18 are labelled — a label every hour turns
          the header into a fence. */}
      <Group gap={0} pl={92} mb={4} wrap="nowrap">
        {[0, 6, 12, 18].map((hour) => (
          <Text key={hour} size="xs" c="dimmed" style={{ width: '25%' }}>
            {String(hour).padStart(2, '0')}:00
          </Text>
        ))}
      </Group>

      <Stack gap={3}>
        {days.map((day) => (
          <Group key={day} gap="sm" wrap="nowrap">
            <Text size="xs" c="dimmed" w={84} style={{ flexShrink: 0 }}>
              {fmt.date(day)}
            </Text>
            <Box
              style={{
                position: 'relative',
                flex: 1,
                height: 18,
                borderRadius: 3,
                background: 'var(--mantine-color-default-hover)',
              }}
            >
              {pieces
                .filter((piece) => piece.day === day)
                .map((piece, index) => {
                  const covered = piece.segment.user_ids.length > 0;
                  const who = covered ? names(piece.segment.user_ids) : t('schedule.gap');
                  return (
                    <Tooltip
                      key={index}
                      withArrow
                      label={`${who} · ${String(Math.floor(piece.from)).padStart(2, '0')}:00 — ${String(Math.ceil(piece.to)).padStart(2, '0')}:00${piece.segment.source ? ` · ${piece.segment.source}` : ''}`}
                    >
                      <Box
                        style={{
                          position: 'absolute',
                          left: `${(piece.from / 24) * 100}%`,
                          width: `${Math.max(0.8, ((piece.to - piece.from) / 24) * 100)}%`,
                          top: 0,
                          bottom: 0,
                          borderRadius: 3,
                          background: covered
                            ? `var(--mantine-color-${colorFor(piece.segment.user_ids)}-6)`
                            : 'transparent',
                          // An override is the same shift covered by somebody
                          // else, so it reads as the same band with a different
                          // surface rather than as a different kind of thing.
                          backgroundImage:
                            covered && piece.segment.source === 'override'
                              ? 'repeating-linear-gradient(45deg, rgba(255,255,255,.35) 0 3px, transparent 3px 6px)'
                              : undefined,
                          border: covered ? undefined : '1px dashed var(--mantine-color-orange-6)',
                        }}
                      />
                    </Tooltip>
                  );
                })}
            </Box>
          </Group>
        ))}
      </Stack>

      <Group gap="lg" mt="sm">
        <Group gap={6}>
          <Box w={12} h={10} style={{ borderRadius: 2, background: 'var(--mantine-color-blue-6)' }} />
          <Text size="xs" c="dimmed">
            {t('schedule.legendShift')}
          </Text>
        </Group>
        <Group gap={6}>
          <Box
            w={12}
            h={10}
            style={{
              borderRadius: 2,
              background: 'var(--mantine-color-blue-6)',
              backgroundImage:
                'repeating-linear-gradient(45deg, rgba(255,255,255,.35) 0 3px, transparent 3px 6px)',
            }}
          />
          <Text size="xs" c="dimmed">
            {t('schedule.legendOverride')}
          </Text>
        </Group>
        <Group gap={6}>
          <Box
            w={12}
            h={10}
            style={{ borderRadius: 2, border: '1px dashed var(--mantine-color-orange-6)' }}
          />
          <Text size="xs" c="dimmed">
            {t('schedule.legendGap')}
          </Text>
        </Group>
      </Group>
    </Paper>
  );
}
