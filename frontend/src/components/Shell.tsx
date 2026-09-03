import {
  ActionIcon,
  AppShell,
  Badge,
  Burger,
  Group,
  Menu,
  NavLink,
  ScrollArea,
  Text,
  Tooltip,
  useMantineColorScheme,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import {
  IconAdjustments,
  IconLock,
  IconCheck,
  IconHeartRateMonitor,
  IconLanguage,
  IconRocket,
  IconAlertTriangle,
  IconBell,
  IconChartHistogram,
  IconHome,
  IconListDetails,
  IconLogout,
  IconMoon,
  IconPlugConnected,
  IconShieldLock,
  IconSitemap,
  IconSun,
  IconUsers,
  IconUsersGroup,
  IconCalendarTime,
  IconTool,
} from '@tabler/icons-react';
import { NavLink as RouterNavLink, Outlet, useLocation } from 'react-router-dom';
import { useAuth } from '../auth/AuthProvider';
import { useCapabilities, useHealth } from '../api/hooks';
import { useI18n } from '../i18n/I18nProvider';
import { useRoleLabel } from '../i18n/domain';
import { LOCALES, LOCALE_LABELS } from '../i18n/locale';
import type { Messages } from '../i18n/messages';
import { Logo } from './Logo';

/**
 * Navigation mirrors Grafana OnCall's page split, adapted to the nxs-anomaly
 * domain. Labels are catalog keys, resolved at render: the sidebar has to
 * change language along with everything else.
 */
const NAV = [
  { to: '/', labelKey: 'nav.home', icon: IconHome, end: true },
  { to: '/alert-groups', labelKey: 'nav.alertGroups', icon: IconAlertTriangle },
  { to: '/alerts', labelKey: 'nav.alerts', icon: IconListDetails },
  { to: '/users', labelKey: 'nav.users', icon: IconUsers },
  { to: '/teams', labelKey: 'nav.teams', icon: IconUsersGroup },
  { to: '/integrations', labelKey: 'nav.integrations', icon: IconPlugConnected },
  { to: '/escalation-chains', labelKey: 'nav.escalationChains', icon: IconSitemap },
  { to: '/schedules', labelKey: 'nav.schedules', icon: IconCalendarTime },
  // Next to schedules rather than down with Settings: planning maintenance is
  // the same kind of act as planning who is on call, and both are done by the
  // same people, ahead of time.
  { to: '/maintenance', labelKey: 'nav.maintenance', icon: IconTool },
  { to: '/notifications', labelKey: 'nav.notifications', icon: IconBell },
  { to: '/insights', labelKey: 'nav.insights', icon: IconChartHistogram },
  // Reading the trail names who did what, which is an administrative act — the
  // API gates it the same way, so showing the link to anyone else would only
  // produce a 403.
  { to: '/audit', labelKey: 'nav.audit', icon: IconShieldLock, adminOnly: true },
  // Setup and readiness sit at the bottom next to Settings: they are about the
  // installation, not about the alerts flowing through it.
  { to: '/setup', labelKey: 'nav.setup', icon: IconRocket },
  { to: '/readiness', labelKey: 'nav.readiness', icon: IconHeartRateMonitor },
  { to: '/settings', labelKey: 'nav.settings', icon: IconAdjustments },
  // Shown only where something is actually gated. An installation that already
  // has every feature does not need a permanent link to a page selling them,
  // and a link that always says "Enterprise" in an enterprise install is noise.
  { to: '/enterprise', labelKey: 'nav.enterprise', icon: IconLock, gatedOnly: true },
] satisfies Array<{
  to: string;
  labelKey: NavLabelKey;
  icon: typeof IconHome;
  end?: boolean;
  adminOnly?: boolean;
  gatedOnly?: boolean;
}>;

type NavLabelKey = Extract<keyof Messages, `nav.${string}`>;

function HealthIndicator() {
  const health = useHealth();
  const { t } = useI18n();
  if (health.isPending) return null;
  if (health.isError) {
    return (
      <Badge color="red" variant="light">
        {t('shell.apiUnreachable')}
      </Badge>
    );
  }
  const data = health.data;
  return (
    <Tooltip
      label={t('shell.healthTooltip', {
        version: data?.version ?? '?',
        db: data?.db_ok ? t('shell.dbOk') : t('shell.dbDown'),
      })}
      withArrow
    >
      <Badge color={data?.status === 'ok' ? 'teal' : 'red'} variant="light">
        {data?.status === 'ok' ? t('shell.healthy') : t('shell.degraded')}
      </Badge>
    </Tooltip>
  );
}

/**
 * The language switch lives in the header, next to the theme toggle, rather
 * than only on the Settings page: somebody who lands on an English UI they
 * cannot read must be able to fix that without first finding — and reading —
 * a settings page.
 */
function LanguageMenu() {
  const { locale, setLocale, t } = useI18n();
  return (
    <Menu withinPortal position="bottom-end">
      <Menu.Target>
        <Tooltip label={t('shell.language')} withArrow>
          <ActionIcon variant="subtle" aria-label={t('shell.languageAria')}>
            <IconLanguage size={18} />
          </ActionIcon>
        </Tooltip>
      </Menu.Target>
      <Menu.Dropdown>
        {LOCALES.map((option) => (
          <Menu.Item
            key={option}
            onClick={() => setLocale(option)}
            leftSection={option === locale ? <IconCheck size={14} /> : <span style={{ width: 14 }} />}
          >
            {LOCALE_LABELS[option]}
          </Menu.Item>
        ))}
      </Menu.Dropdown>
    </Menu>
  );
}

export function Shell() {
  const [opened, { toggle, close }] = useDisclosure();
  const { colorScheme, toggleColorScheme } = useMantineColorScheme();
  const { signOut, authDisabled, identity } = useAuth();
  // The Enterprise link appears only where this build actually withholds
  // something: an installation that has every feature does not need a permanent
  // link to a page about features it already has. Cached hard by the hook —
  // the answer changes when the binary or the environment does, not while
  // somebody is looking at a page.
  const capabilities = useCapabilities();
  const hasGatedFeatures = (capabilities.data?.capabilities ?? []).some(
    (c) => c.state === 'unavailable_in_edition',
  );
  const location = useLocation();
  const { t } = useI18n();
  const roleLabel = useRoleLabel();

  return (
    <AppShell
      header={{ height: 56 }}
      navbar={{ width: 240, breakpoint: 'sm', collapsed: { mobile: !opened } }}
      padding="lg"
    >
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between" wrap="nowrap">
          <Group gap="sm" wrap="nowrap">
            <Burger opened={opened} onClick={toggle} hiddenFrom="sm" size="sm" />
            <Logo />
            <Text fw={600}>nxs-anomaly</Text>
          </Group>
          <Group gap="xs" wrap="nowrap">
            <HealthIndicator />
            {identity && (
              // Who you are acting as is a safety property, not decoration:
              // roles change what the same button does, and an API key session
              // looks nothing like a personal one in the audit trail.
              <Tooltip
                label={t('shell.identityTooltip', {
                  kind: identity.kind,
                  role: roleLabel(identity.role),
                })}
                withArrow
              >
                <Badge variant="light" color="gray" visibleFrom="sm">
                  {identity.display_name}
                </Badge>
              </Tooltip>
            )}
            <LanguageMenu />
            <Tooltip label={t('shell.toggleTheme')} withArrow>
              <ActionIcon
                variant="subtle"
                onClick={toggleColorScheme}
                aria-label={t('shell.themeAria')}
              >
                {colorScheme === 'dark' ? <IconSun size={18} /> : <IconMoon size={18} />}
              </ActionIcon>
            </Tooltip>
            {!authDisabled && (
              <Tooltip label={t('shell.signOut')} withArrow>
                <ActionIcon
                  variant="subtle"
                  onClick={() => void signOut()}
                  aria-label={t('shell.signOut')}
                >
                  <IconLogout size={18} />
                </ActionIcon>
              </Tooltip>
            )}
          </Group>
        </Group>
      </AppShell.Header>

      <AppShell.Navbar p="xs">
        <ScrollArea>
          {NAV.filter(
            (item) =>
              (!item.adminOnly || identity?.permissions.admin) &&
              (!item.gatedOnly || hasGatedFeatures),
          ).map((item) => {
            const Icon = item.icon;
            const active = item.end
              ? location.pathname === item.to
              : location.pathname.startsWith(item.to);
            return (
              <NavLink
                key={item.to}
                component={RouterNavLink}
                to={item.to}
                label={t(item.labelKey)}
                leftSection={<Icon size={18} />}
                active={active}
                onClick={close}
              />
            );
          })}
        </ScrollArea>
      </AppShell.Navbar>

      <AppShell.Main>
        <Outlet />
      </AppShell.Main>
    </AppShell>
  );
}
