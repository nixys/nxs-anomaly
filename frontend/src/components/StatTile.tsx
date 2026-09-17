import { useEffect, useRef, useState } from 'react';
import { Group, Paper, Skeleton, Stack, Text } from '@mantine/core';
import { useReducedMotion } from '@mantine/hooks';
import { DURATION } from '../ui/motion';
import type { MantineColor } from '@mantine/core';

/**
 * Headline number. The accent dot carries the status hue; the number itself stays
 * in text ink so identity never rests on color alone.
 */
/**
 * Counts to a new value instead of replacing it.
 *
 * "7 open" becoming "6 open" while nobody watches is indistinguishable from a
 * different number having been there all along. A short count makes the change
 * itself the thing that is visible — which, on a screen whose whole job is "what
 * needs attention right now", is the information.
 *
 * Only whole steps, and only for real numbers: a value that is a word is set
 * directly, and a viewer who asked for less motion gets the final number at once.
 */
function useCountUp(value: number | string | undefined): number | string | undefined {
  const reduce = useReducedMotion();
  const [shown, setShown] = useState(value);
  const frame = useRef<number>();

  useEffect(() => {
    if (typeof value !== 'number' || reduce) {
      setShown(value);
      return;
    }
    // The first number — typically the answer to the query that was loading —
    // is set as is: there is nothing on screen to count from. Returning here
    // without setting it left the tile at "—" forever.
    if (typeof shown !== 'number') {
      setShown(value);
      return;
    }
    const from = shown;
    if (from === value) return;
    const started = performance.now();
    const step = (now: number) => {
      const progress = Math.min(1, (now - started) / DURATION.count);
      // Same decelerating shape as everything else that moves here.
      const eased = 1 - Math.pow(1 - progress, 3);
      setShown(Math.round(from + (value - from) * eased));
      if (progress < 1) frame.current = requestAnimationFrame(step);
    };
    frame.current = requestAnimationFrame(step);
    return () => {
      if (frame.current) cancelAnimationFrame(frame.current);
    };
    // `shown` is deliberately not a dependency: it changes on every frame.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value, reduce]);

  return shown;
}

export function StatTile({
  label,
  value,
  hint,
  color,
  loading,
}: {
  label: string;
  value: number | string | undefined;
  hint?: string;
  color?: MantineColor;
  loading?: boolean;
}) {
  const shown = useCountUp(value);
  return (
    <Paper withBorder p="md">
      <Stack gap={6}>
        <Group gap={8} wrap="nowrap">
          {color && (
            <span
              aria-hidden
              style={{
                width: 8,
                height: 8,
                borderRadius: 999,
                background: `var(--mantine-color-${color}-6)`,
                flexShrink: 0,
              }}
            />
          )}
          <Text size="xs" c="dimmed" tt="uppercase" fw={600}>
            {label}
          </Text>
        </Group>
        {loading ? (
          <Skeleton height={30} width={70} />
        ) : (
          <Text fz={30} fw={600} lh={1} style={{ fontVariantNumeric: 'tabular-nums' }}>
            {shown ?? '—'}
          </Text>
        )}
        {hint && (
          <Text size="xs" c="dimmed">
            {hint}
          </Text>
        )}
      </Stack>
    </Paper>
  );
}

export interface DistributionRow {
  label: string;
  value: number;
  color: MantineColor;
}

/**
 * Horizontal magnitude comparison. Every row is labeled and shows its value, so
 * the bars add speed of comparison rather than carrying the meaning themselves.
 */
export function DistributionBars({ rows }: { rows: DistributionRow[] }) {
  const max = Math.max(1, ...rows.map((row) => row.value));
  return (
    <Stack gap="xs">
      {rows.map((row) => (
        <Group key={row.label} gap="sm" wrap="nowrap">
          <Text size="sm" w={130} style={{ flexShrink: 0 }}>
            {row.label}
          </Text>
          <div
            style={{
              flex: 1,
              height: 10,
              borderRadius: 4,
              background: 'var(--mantine-color-default-border)',
              overflow: 'hidden',
            }}
          >
            <div
              style={{
                width: `${(row.value / max) * 100}%`,
                minWidth: row.value > 0 ? 4 : 0,
                height: '100%',
                borderRadius: 4,
                background: `var(--mantine-color-${row.color}-6)`,
              }}
            />
          </div>
          <Text size="sm" w={64} ta="right" style={{ flexShrink: 0, fontVariantNumeric: 'tabular-nums' }}>
            {row.value}
          </Text>
        </Group>
      ))}
    </Stack>
  );
}
