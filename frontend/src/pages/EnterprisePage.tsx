import { Alert, Badge, Card, Group, Stack, Text } from "@mantine/core";
import {
  IconInfoCircle,
  IconLock,
  IconSettings,
  IconCheck,
} from "@tabler/icons-react";
import { PageHeader, QueryState } from "../components/common";
import { useCapabilities } from "../api/hooks";
import type { Capability, CapabilityState } from "../api/types";
import { useI18n } from "../i18n/I18nProvider";

/**
 * What this installation can do, and — where it cannot — which of the two
 * reasons applies.
 *
 * The page exists because the alternative is worse. A feature that is simply
 * absent from the interface reads as a product that does not have it at all,
 * and the person who needs it goes looking somewhere else. Shown blocked, with
 * the reason, it is a question somebody can answer: an unconfigured feature
 * goes to whoever runs the deployment, an edition-gated one does not.
 */

const STATE: Record<
  CapabilityState,
  { color: string; icon: typeof IconCheck }
> = {
  available: { color: "teal", icon: IconCheck },
  not_configured: { color: "yellow", icon: IconSettings },
  unavailable_in_edition: { color: "gray", icon: IconLock },
};

/** Ordered so the page opens on what is missing, not on what already works. */
const ORDER: CapabilityState[] = [
  "unavailable_in_edition",
  "not_configured",
  "available",
];

function FeatureCard({ capability }: { capability: Capability }) {
  const { t } = useI18n();
  const state = STATE[capability.state] ?? STATE.not_configured;
  const Icon = state.icon;
  // `string` keys intentionally: a server newer than this interface may report
  // a feature this catalog has no name for, and the raw key plus the server's
  // own sentence still says more than an empty card would.
  const names: Partial<Record<string, string>> = {
    sso: t("enterprise.feature.sso"),
    team_scoping: t("enterprise.feature.team_scoping"),
    analytics_stream: t("enterprise.feature.analytics_stream"),
  };
  const stateLabels: Partial<Record<string, string>> = {
    available: t("enterprise.state.available"),
    not_configured: t("enterprise.state.not_configured"),
    unavailable_in_edition: t("enterprise.state.unavailable_in_edition"),
  };
  const name = names[capability.name] ?? capability.name;

  return (
    <Card withBorder padding="md">
      <Stack gap="xs">
        <Group justify="space-between" wrap="nowrap">
          <Group gap="xs" wrap="nowrap">
            <Icon size={18} />
            <Text fw={600}>{name}</Text>
          </Group>
          <Badge color={state.color} variant="light">
            {stateLabels[capability.state] ?? capability.state}
          </Badge>
        </Group>
        {/* The server writes this sentence: it knows which variable is unset. */}
        <Text size="sm" c="dimmed">
          {capability.detail}
        </Text>
      </Stack>
    </Card>
  );
}

export function EnterprisePage() {
  const { t } = useI18n();
  const query = useCapabilities();

  return (
    <Stack gap="lg">
      <PageHeader
        title={t("enterprise.title")}
        description={t("enterprise.subtitle")}
      />

      <QueryState query={query}>
        {(data) => {
          const capabilities = [...(data.capabilities ?? [])].sort(
            (a, b) => ORDER.indexOf(a.state) - ORDER.indexOf(b.state),
          );
          const gated = capabilities.some(
            (c) => c.state === "unavailable_in_edition",
          );
          return (
            <Stack gap="md">
              <Group gap="xs">
                <Text size="sm" c="dimmed">
                  {t("enterprise.editionLabel")}
                </Text>
                <Badge variant="light">{data.edition}</Badge>
              </Group>

              <Text size="sm">{t("enterprise.intro")}</Text>

              <Stack gap="sm">
                {capabilities.map((c) => (
                  <FeatureCard key={c.name} capability={c} />
                ))}
              </Stack>

              {/* Only where something is actually gated: an installation that
                  already has every feature does not need to be sold one. */}
              {gated && (
                <Alert
                  color="blue"
                  icon={<IconInfoCircle size={16} />}
                  title={t("enterprise.howToRequest")}
                >
                  {t("enterprise.howToRequestBody")}
                </Alert>
              )}
            </Stack>
          );
        }}
      </QueryState>
    </Stack>
  );
}
