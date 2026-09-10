import { Group, Paper, Stack, Text } from '@mantine/core';
import { useI18n } from '../i18n/I18nProvider';

export interface TrendSeries {
  key: string;
  label: string;
  /** A Mantine colour token name; these are state colours, not a categorical set. */
  color: string;
  values: number[];
}

/**
 * Two series of the same unit, per day, as grouped bars.
 *
 * Why bars and not a line: the buckets are days, there are between seven and
 * thirty of them, and a day is a discrete thing that either had incidents or did
 * not — a line would draw a slope between Tuesday and Wednesday that nothing in
 * the data claims. Why two charts on the page rather than one: incidents and
 * notifications are different units, and a single chart with two scales is a
 * chart that can be made to say anything.
 *
 * Colour carries state, not identity — opened/resolved and delivered/failed are
 * the same status hues the badges use elsewhere in the product, so nothing new
 * has to be learned to read them. Identity never rests on colour alone: every
 * series is in the legend, every bar carries its numbers in a tooltip, and the
 * exact values are one line below in the summary.
 */
export function TrendChart({
  title,
  days,
  series,
  emptyLabel,
}: {
  title: string;
  days: string[];
  series: [TrendSeries, TrendSeries];
  emptyLabel: string;
}) {
  const { fmt } = useI18n();
  const max = Math.max(1, ...series.flatMap((s) => s.values));
  const total = series.reduce((sum, s) => sum + s.values.reduce((a, b) => a + b, 0), 0);

  // Geometry in a fixed viewBox; the SVG scales to its container. Room is left
  // below the baseline for the two date labels, and above the tallest bar for
  // the peak label, so nothing is drawn outside the box.
  const width = 720;
  const height = 132;
  const padBottom = 18;
  const padTop = 14;
  const plot = height - padBottom - padTop;
  const slot = width / Math.max(1, days.length);
  const barWidth = Math.max(2, Math.min(14, slot / 2 - 2));

  return (
    <Paper withBorder p="lg">
      <Group justify="space-between" mb="xs" align="baseline">
        <Text fw={600} size="sm">
          {title}
        </Text>
        <Group gap="md">
          {series.map((s) => (
            <Group gap={6} key={s.key} wrap="nowrap">
              <span
                aria-hidden
                style={{
                  width: 9,
                  height: 9,
                  borderRadius: 2,
                  background: `var(--mantine-color-${s.color}-6)`,
                }}
              />
              <Text size="xs" c="dimmed">
                {s.label} · {s.values.reduce((a, b) => a + b, 0)}
              </Text>
            </Group>
          ))}
        </Group>
      </Group>

      {total === 0 ? (
        <Text size="sm" c="dimmed" py="lg" ta="center">
          {emptyLabel}
        </Text>
      ) : (
        <svg
          viewBox={`0 0 ${width} ${height}`}
          width="100%"
          height={height}
          role="img"
          aria-label={`${title}: ${series.map((s) => `${s.label} ${s.values.join(', ')}`).join('; ')}`}
        >
          {/* Baseline only: a grid would compete with bars this short. */}
          <line
            x1="0"
            y1={height - padBottom}
            x2={width}
            y2={height - padBottom}
            stroke="var(--mantine-color-default-border)"
            strokeWidth="1"
          />
          {days.map((day, index) => {
            const x = index * slot;
            return (
              <g key={day}>
                {series.map((s, seriesIndex) => {
                  const value = s.values[index] ?? 0;
                  const barHeight = value === 0 ? 0 : Math.max(2, (value / max) * plot);
                  return (
                    <rect
                      key={s.key}
                      x={x + slot / 2 - barWidth - 1 + seriesIndex * (barWidth + 2)}
                      y={height - padBottom - barHeight}
                      width={barWidth}
                      height={barHeight}
                      rx="2"
                      fill={`var(--mantine-color-${s.color}-6)`}
                    />
                  );
                })}
                {/* One hit target per day, covering the full height: the bars
                    themselves are too thin to hover reliably. */}
                <rect x={x} y="0" width={slot} height={height} fill="transparent">
                  <title>
                    {`${fmt.date(day)} — ${series.map((s) => `${s.label}: ${s.values[index] ?? 0}`).join(', ')}`}
                  </title>
                </rect>
              </g>
            );
          })}
          <text
            x="0"
            y={height - 4}
            fontSize="11"
            fill="var(--mantine-color-dimmed)"
            fontFamily="var(--mantine-font-family-monospace)"
          >
            {fmt.date(days[0])}
          </text>
          <text
            x={width}
            y={height - 4}
            textAnchor="end"
            fontSize="11"
            fill="var(--mantine-color-dimmed)"
            fontFamily="var(--mantine-font-family-monospace)"
          >
            {fmt.date(days[days.length - 1])}
          </text>
          <text
            x="0"
            y={padTop - 3}
            fontSize="11"
            fill="var(--mantine-color-dimmed)"
            fontFamily="var(--mantine-font-family-monospace)"
          >
            {`max ${max}`}
          </text>
        </svg>
      )}
    </Paper>
  );
}

/** The values behind the chart, for anyone who needs the numbers rather than the shape. */
export function TrendTable({ days, series }: { days: string[]; series: TrendSeries[] }) {
  const { fmt } = useI18n();
  return (
    <Stack gap={2}>
      {days.map((day, index) => (
        <Group key={day} gap="sm" wrap="nowrap">
          <Text size="xs" c="dimmed" w={110} style={{ flexShrink: 0 }}>
            {fmt.date(day)}
          </Text>
          {series.map((s) => (
            <Text size="xs" key={s.key} w={120} style={{ fontVariantNumeric: 'tabular-nums' }}>
              {s.label}: {s.values[index] ?? 0}
            </Text>
          ))}
        </Group>
      ))}
    </Stack>
  );
}
