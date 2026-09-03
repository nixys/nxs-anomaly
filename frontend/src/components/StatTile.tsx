import { Group, Paper, Skeleton, Stack, Text } from '@mantine/core';
import type { MantineColor } from '@mantine/core';

/**
 * Headline number. The accent dot carries the status hue; the number itself stays
 * in text ink so identity never rests on color alone.
 */
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
          <Text fz={30} fw={600} lh={1}>
            {value ?? '—'}
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
