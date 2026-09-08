import {
  ActionIcon,
  Alert,
  Badge,
  Box,
  Button,
  Center,
  Code,
  Group,
  Loader,
  Modal,
  Select,
  Stack,
  Text,
  Title,
  Tooltip,
} from '@mantine/core';
import type { MantineColor } from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import {
  IconAlertTriangle,
  IconInbox,
  IconSortAscending,
  IconSortDescending,
  IconTrash,
} from '@tabler/icons-react';
import type { ReactNode } from 'react';
import { useI18n } from '../i18n/I18nProvider';
import type { StringKey } from '../i18n/I18nProvider';
import { describeError } from '../i18n/errors';
import { EMPTY_VALUE, parseDate } from '../i18n/format';
import { useStatusLabel, useSeverityLabel } from '../i18n/domain';
import { SEVERITY_COLOR, SEVERITY_GLYPH, severityLevel } from '../domain/severity';

export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <Group justify="space-between" align="flex-start" wrap="nowrap" mb="lg">
      <Box>
        <Title order={2}>{title}</Title>
        {description && (
          <Text size="sm" c="dimmed" mt={4}>
            {description}
          </Text>
        )}
      </Box>
      {actions && <Group gap="xs">{actions}</Group>}
    </Group>
  );
}

export function LoadingState({ label }: { label?: string }) {
  const { t } = useI18n();
  return (
    <Center py="xl">
      <Stack align="center" gap="xs">
        <Loader size="sm" />
        <Text size="sm" c="dimmed">
          {label ?? t('common.loading')}
        </Text>
      </Stack>
    </Center>
  );
}

export function ErrorState({ error }: { error: unknown }) {
  const { t } = useI18n();
  return (
    <Alert color="red" icon={<IconAlertTriangle size={16} />} title={t('common.loadFailed')}>
      {describeError(error, t)}
    </Alert>
  );
}

/**
 * Nothing here — and what to do about it.
 *
 * "No alert groups match these filters" is true and useless on its own: the
 * reader's next move is either to widen the filter or to create the thing that
 * would fill this list, and the page knows which. `action` is that move, so an
 * empty screen stops being a dead end.
 */
export function EmptyState({
  label,
  hint,
  action,
}: {
  label: string;
  hint?: string;
  action?: ReactNode;
}) {
  return (
    <Center py="xl">
      <Stack align="center" gap={6}>
        <IconInbox size={28} opacity={0.5} />
        <Text size="sm" c="dimmed">
          {label}
        </Text>
        {hint && (
          <Text size="xs" c="dimmed">
            {hint}
          </Text>
        )}
        {action && <Box mt="xs">{action}</Box>}
      </Stack>
    </Center>
  );
}

/**
 * Renders the loading / error / empty / content states of a query in one place
 * so every page handles them identically.
 */
export function QueryState<T>({
  query,
  isEmpty,
  emptyLabel,
  emptyAction,
  children,
}: {
  query: { isPending: boolean; isError: boolean; error: unknown; data: T | undefined };
  isEmpty?: (data: T) => boolean;
  emptyLabel?: string;
  /** What the reader can do about the emptiness, when the page knows. */
  emptyAction?: ReactNode;
  children: (data: T) => ReactNode;
}) {
  const { t } = useI18n();
  if (query.isPending) return <LoadingState />;
  if (query.isError) return <ErrorState error={query.error} />;
  if (query.data === undefined) return <EmptyState label={emptyLabel ?? t('common.noData')} />;
  if (isEmpty?.(query.data)) {
    return <EmptyState label={emptyLabel ?? t('common.nothingYet')} action={emptyAction} />;
  }
  return <>{children(query.data)}</>;
}

const STATUS_COLORS: Record<string, MantineColor> = {
  open: 'red',
  firing: 'red',
  acknowledged: 'yellow',
  resolved: 'teal',
  silenced: 'gray',
  delivered: 'teal',
  delivery_scheduled: 'blue',
  retry_scheduled: 'orange',
  retrying: 'orange',
  delivering: 'blue',
  failed: 'red',
  // Nothing was sent and nothing broke: a channel with no transport here.
  skipped: 'gray',
  batched: 'grape',
  ok: 'teal',
  degraded: 'red',
};

export function StatusBadge({ status }: { status: string | undefined }) {
  const label = useStatusLabel();
  if (!status) return <Text c="dimmed">{EMPTY_VALUE}</Text>;
  return (
    <Badge color={STATUS_COLORS[status] ?? 'gray'} variant="light" tt="none">
      {label(status)}
    </Badge>
  );
}

/**
 * Severity as one badge: colour and shape from the level, text from the word the
 * source actually sent.
 *
 * Both halves matter. The level is what ranks and filters, so `high` and
 * `error` have to look identical; the spelling is what the source said, so
 * showing `error` where the payload said `high` would quietly rewrite the
 * evidence somebody is about to quote in a chat.
 */
export function SeverityBadge({ severity }: { severity: string | undefined }) {
  const label = useSeverityLabel();
  const { t } = useI18n();
  if (!severity) return <Text c="dimmed">{EMPTY_VALUE}</Text>;
  const level = severityLevel(severity);
  if (level === null) {
    return (
      <Tooltip label={t('severity.unmodelled')} withArrow>
        <Badge color="gray" variant="outline" tt="none" styles={{ label: { fontVariant: 'normal' } }}>
          {severity}
        </Badge>
      </Tooltip>
    );
  }
  return (
    <Badge
      color={SEVERITY_COLOR[level]}
      variant="outline"
      tt="none"
      leftSection={<span aria-hidden>{SEVERITY_GLYPH[level]}</span>}
    >
      {label(severity.toLowerCase())}
    </Badge>
  );
}

/**
 * "2 hours ago", with the exact moment — and its zone — on hover.
 *
 * The relative form is what a responder reads; the tooltip is what they quote
 * to a colleague, which is why it names the zone. Without it, two people in
 * different offices comparing the same incident have no way to notice they are
 * reading two different wall clocks.
 */
export function RelativeTime({ value }: { value: string | null | undefined }) {
  const { fmt } = useI18n();
  if (!value) return <Text c="dimmed">{EMPTY_VALUE}</Text>;
  return (
    <Tooltip label={`${fmt.dateTime(value)} ${fmt.zoneLabel(value)}`} withArrow>
      <Text size="sm" style={{ whiteSpace: 'nowrap' }}>
        {fmt.relative(value)}
      </Text>
    </Tooltip>
  );
}

/**
 * An exact timestamp, always carrying its zone.
 *
 * The zone is shown rather than implied: these are rendered in the viewer's own
 * timezone, and an unlabelled `31.12.2025, 23:45` looks identical whether it is
 * Moscow time or Novosibirsk time.
 */
/**
 * How long an incident has been running, or how long it ran.
 *
 * "Last alert 42 seconds ago" answers when the source last spoke; it does not
 * answer the question a responder scanning a queue is actually asking, which is
 * how long this has been burning. An hour-old open incident and a minute-old
 * one look identical without it.
 */
export function IncidentAge({
  createdAt,
  resolvedAt,
}: {
  createdAt: string | null | undefined;
  resolvedAt?: string | null;
}) {
  const { t, fmt } = useI18n();
  const started = parseDate(createdAt);
  if (!started) return <Text c="dimmed">{EMPTY_VALUE}</Text>;
  const ended = parseDate(resolvedAt) ?? new Date();
  const elapsed = fmt.duration(ended.getTime() - started.getTime());
  if (resolvedAt) {
    return (
      <Text size="sm" c="dimmed">
        {t('groups.ranFor', { duration: elapsed })}
      </Text>
    );
  }
  return (
    <Text size="sm" fw={500}>
      {t('groups.burningFor', { duration: elapsed })}
    </Text>
  );
}

export function AbsoluteTime({
  value,
  withZone = true,
}: {
  value: string | null | undefined;
  /** Off where a whole column shares one zone, stated once in the header. */
  withZone?: boolean;
}) {
  const { fmt } = useI18n();
  if (!value) return <Text c="dimmed">{EMPTY_VALUE}</Text>;
  return (
    <Tooltip label={`${fmt.dateTime(value)} ${fmt.zoneLabel(value)}`} withArrow>
      <Text size="sm" style={{ whiteSpace: 'nowrap' }}>
        {fmt.dateTime(value)}
        {withZone ? ` ${fmt.zoneLabel(value)}` : ''}
      </Text>
    </Tooltip>
  );
}

// The API sends `null`, not `{}`, for an alert or group carrying no labels, so
// the prop admits it explicitly rather than relying on the `?? {}` below to
// quietly absorb a value the type claimed was impossible.
export function Labels({ labels }: { labels: Record<string, string> | null | undefined }) {
  const entries = Object.entries(labels ?? {});
  if (entries.length === 0) return <Text c="dimmed">{EMPTY_VALUE}</Text>;
  return (
    <Group gap={4}>
      {entries.map(([key, value]) => (
        <Badge key={key} variant="default" tt="none" size="sm" style={{ fontWeight: 400 }}>
          {key}={String(value)}
        </Badge>
      ))}
    </Group>
  );
}

export function MonoId({ id }: { id: string | null | undefined }) {
  if (!id) return <Text c="dimmed">{EMPTY_VALUE}</Text>;
  return (
    <Tooltip label={id} withArrow>
      <Code style={{ whiteSpace: 'nowrap' }}>{id.length > 18 ? `${id.slice(0, 16)}…` : id}</Code>
    </Tooltip>
  );
}

export function ConfirmDeleteButton({
  label,
  onConfirm,
  loading,
  disabled,
  disabledReason,
}: {
  label: string;
  onConfirm: () => void;
  loading?: boolean;
  /** Set for objects the API will refuse to delete, e.g. Terraform-managed ones. */
  disabled?: boolean;
  disabledReason?: string;
}) {
  const [opened, { open, close }] = useDisclosure(false);
  const { t } = useI18n();
  return (
    <>
      <Tooltip label={disabled ? (disabledReason ?? t('common.delete')) : t('common.delete')} withArrow>
        {/* A disabled ActionIcon does not emit pointer events, so the tooltip
            needs a wrapper that still does — otherwise the one control that
            explains why it is disabled is the one that cannot say so. */}
        <Box component="span" style={{ display: 'inline-flex' }}>
          <ActionIcon
            color="red"
            variant="subtle"
            onClick={open}
            disabled={disabled}
            aria-label={t('common.deleteAria', { name: label })}
          >
            <IconTrash size={16} />
          </ActionIcon>
        </Box>
      </Tooltip>
      <Modal opened={opened} onClose={close} title={t('common.confirmDeletion')} centered>
        <Stack>
          <Text size="sm">{t('common.confirmDeleteBody', { name: label })}</Text>
          <Group justify="flex-end">
            <Button variant="default" onClick={close}>
              {t('common.cancel')}
            </Button>
            <Button
              color="red"
              loading={loading}
              onClick={() => {
                onConfirm();
                close();
              }}
            >
              {t('common.delete')}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}

export function JsonBlock({ value }: { value: unknown }) {
  return (
    <Code block style={{ maxHeight: 420, overflow: 'auto' }}>
      {JSON.stringify(value, null, 2)}
    </Code>
  );
}

/**
 * Column picker plus a direction toggle for a paginated listing.
 *
 * The ordering lives in the query rather than in the browser: a page holds 25
 * rows out of thousands, so sorting what arrived would only reorder the slice
 * the server happened to pick — which is the bug this replaces, not a fix for
 * it.
 */
export function SortControl({
  options,
  field,
  desc,
  onChange,
  width = 190,
}: {
  options: Array<{ value: string; labelKey: StringKey }>;
  field: string;
  desc: boolean;
  onChange: (next: { field: string; desc: boolean }) => void;
  width?: number;
}) {
  const { t } = useI18n();
  return (
    <Group gap={4} align="flex-end">
      <Select
        label={t('common.sort')}
        data={options.map((option) => ({ value: option.value, label: t(option.labelKey) }))}
        value={field}
        onChange={(value) => value && onChange({ field: value, desc })}
        allowDeselect={false}
        w={width}
      />
      <Tooltip label={desc ? t('common.sortNewest') : t('common.sortOldest')} withArrow>
        <ActionIcon
          variant="default"
          size={36}
          aria-label={t('common.sortDirection')}
          onClick={() => onChange({ field, desc: !desc })}
        >
          {desc ? <IconSortDescending size={18} /> : <IconSortAscending size={18} />}
        </ActionIcon>
      </Tooltip>
    </Group>
  );
}

/**
 * Marks an object an infrastructure-as-code tool owns.
 *
 * It is rendered wherever the object's name is, not next to the controls it
 * disables: somebody looking for why they cannot edit something reads the row,
 * and a badge tucked beside a greyed-out button is found only after the
 * confusion it was supposed to prevent.
 */
export function ProvisionedBadge({ by }: { by: string | null | undefined }) {
  const { t } = useI18n();
  if (!by) return null;
  return (
    <Tooltip label={t('common.provisionedHint', { tool: by })} withArrow>
      <Badge variant="light" color="grape" tt="none">
        {t('common.provisionedBy', { tool: by })}
      </Badge>
    </Tooltip>
  );
}

/**
 * The banner form of ProvisionedBadge, for a detail page where the object fills
 * the screen and its edit controls are spread over several tabs.
 */
export function ProvisionedNotice({ by }: { by: string | null | undefined }) {
  const { t } = useI18n();
  if (!by) return null;
  return (
    <Alert color="grape" mb="md" title={t('common.provisionedBy', { tool: by })}>
      {t('common.provisionedHint', { tool: by })}
    </Alert>
  );
}
