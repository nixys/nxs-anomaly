import {
  ActionIcon,
  AppShell,
  Badge,
  Box,
  Burger,
  Divider,
  Group,
  Menu,
  NavLink,
  ScrollArea,
  Text,
  Tooltip,
  UnstyledButton,
  useMantineColorScheme,
} from '@mantine/core';
import { useDisclosure, useHotkeys, useLocalStorage, useMediaQuery, useReducedMotion } from '@mantine/hooks';
import {
  IconAdjustments,
  IconLock,
  IconCheck,
  IconHeartRateMonitor,
  IconLanguage,
  IconLayoutSidebarLeftCollapse,
  IconLayoutSidebarLeftExpand,
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
import { CommandPalette, type Command } from './CommandPalette';

/**
 * Navigation mirrors Grafana OnCall's page split, adapted to the nxs-anomaly
 * domain. Labels are catalog keys, resolved at render: the sidebar has to
 * change language along with everything else.
 */
const NAV = [
  { to: '/', labelKey: 'nav.home', icon: IconHome, end: true, section: 'nav.sectionRespond' },
  { to: '/alert-groups', labelKey: 'nav.alertGroups', icon: IconAlertTriangle , section: 'nav.sectionRespond' },
  { to: '/alerts', labelKey: 'nav.alerts', icon: IconListDetails , section: 'nav.sectionRespond' },
  { to: '/users', labelKey: 'nav.users', icon: IconUsers , section: 'nav.sectionOnCall' },
  { to: '/teams', labelKey: 'nav.teams', icon: IconUsersGroup , section: 'nav.sectionOnCall' },
  { to: '/integrations', labelKey: 'nav.integrations', icon: IconPlugConnected , section: 'nav.sectionRouting' },
  { to: '/escalation-chains', labelKey: 'nav.escalationChains', icon: IconSitemap , section: 'nav.sectionRouting' },
  { to: '/schedules', labelKey: 'nav.schedules', icon: IconCalendarTime , section: 'nav.sectionOnCall' },
  // Next to schedules rather than down with Settings: planning maintenance is
  // the same kind of act as planning who is on call, and both are done by the
  // same people, ahead of time.
  { to: '/maintenance', labelKey: 'nav.maintenance', icon: IconTool , section: 'nav.sectionOnCall' },
  { to: '/notifications', labelKey: 'nav.notifications', icon: IconBell , section: 'nav.sectionRespond' },
  { to: '/insights', labelKey: 'nav.insights', icon: IconChartHistogram , section: 'nav.sectionReview' },
  // Reading the trail names who did what, which is an administrative act — the
  // API gates it the same way, so showing the link to anyone else would only
  // produce a 403.
  { to: '/audit', labelKey: 'nav.audit', icon: IconShieldLock, adminOnly: true , section: 'nav.sectionReview' },
  // Setup and readiness sit at the bottom next to Settings: they are about the
  // installation, not about the alerts flowing through it.
  { to: '/setup', labelKey: 'nav.setup', icon: IconRocket , section: 'nav.sectionInstallation' },
  { to: '/readiness', labelKey: 'nav.readiness', icon: IconHeartRateMonitor , section: 'nav.sectionInstallation' },
  { to: '/settings', labelKey: 'nav.settings', icon: IconAdjustments , section: 'nav.sectionInstallation' },
  // Shown only where something is actually gated. An installation that already
  // has every feature does not need a permanent link to a page selling them,
  // and a link that always says "Enterprise" in an enterprise install is noise.
  { to: '/enterprise', labelKey: 'nav.enterprise', icon: IconLock, gatedOnly: true , section: 'nav.sectionInstallation' },
] satisfies Array<{
  to: string;
  labelKey: NavLabelKey;
  icon: typeof IconHome;
  end?: boolean;
  adminOnly?: boolean;
  gatedOnly?: boolean;
  section: NavLabelKey;
}>;

/**
 * The order the sections appear in, which is the order of a shift: what is
 * burning now, who answers it, how alerts reach them, what happened afterwards,
 * and the installation itself.
 *
 * Seventeen equal-weight links were a list nobody read to the end. The pages
 * did not change — only the claim that they are all the same kind of thing.
 */
const SECTIONS = [
  'nav.sectionRespond',
  'nav.sectionOnCall',
  'nav.sectionRouting',
  'nav.sectionReview',
  'nav.sectionInstallation',
] as const satisfies readonly NavLabelKey[];

/**
 * The two widths the sidebar moves between: the full list, and a rail of icons.
 *
 * The rail is 64 rather than the 48 an icon strictly needs, so the icons keep
 * the same distance from the left edge as they have when expanded — the labels
 * slide out from behind them instead of the whole column shifting sideways,
 * which is what makes the movement read as one object narrowing rather than two
 * things moving at once.
 */
const NAV_WIDTH_EXPANDED = 240;
const NAV_WIDTH_RAIL = 64;

/**
 * How the sidebar moves.
 *
 * One duration for the width and a shorter one for the labels, deliberately not
 * synchronised: the text fades out in the first third of the collapse and fades
 * in over the last third of the expansion, so it is never being squeezed while
 * it is still readable. The curve decelerates hard at the end, which is what
 * separates a panel that settles from one that stops.
 */
const NAV_MOTION = {
  width: 260,
  labelOut: 110,
  labelIn: 160,
  labelInDelay: 90,
  curve: 'cubic-bezier(0.22, 0.68, 0.24, 1)',
};

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
  // Whether the sidebar is a rail. Remembered per browser, because it is a
  // statement about this screen — a 13" laptop wants the space back, a wall
  // display does not — and read synchronously so the first paint is already the
  // width the person left it at instead of sliding into place on every load.
  const [railed, setRailed] = useLocalStorage({
    key: 'nxs-anomaly:nav-railed',
    defaultValue: false,
    getInitialValueInEffect: false,
  });
  // Below the breakpoint the navbar is a drawer the burger opens, and a 64px
  // drawer is not a navigation. The rail is a desktop affordance only.
  const narrow = useMediaQuery('(max-width: 48em)', false);
  const reduceMotion = useReducedMotion();
  const rail = railed && !narrow;
  // mod+B is the shortcut editors have taught everyone for exactly this panel.
  useHotkeys([['mod+B', () => setRailed((value) => !value)]]);

  const motion = (property: string, ms: number, delay = 0) =>
    reduceMotion ? undefined : `${property} ${ms}ms ${NAV_MOTION.curve} ${delay}ms`;

  // Labels leave quickly and arrive late, so they are never mid-fade while the
  // column is at its most cramped.
  const labelStyle = {
    display: 'block',
    whiteSpace: 'nowrap' as const,
    opacity: rail ? 0 : 1,
    transform: rail ? 'translateX(-6px)' : 'translateX(0)',
    transition: reduceMotion
      ? undefined
      : rail
        ? `${motion('opacity', NAV_MOTION.labelOut)}, ${motion('transform', NAV_MOTION.labelOut)}`
        : `${motion('opacity', NAV_MOTION.labelIn, NAV_MOTION.labelInDelay)}, ${motion('transform', NAV_MOTION.labelIn, NAV_MOTION.labelInDelay)}`,
  };
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
  const { t, setLocale } = useI18n();
  const roleLabel = useRoleLabel();

  const visibleNav = NAV.filter(
    (item) =>
      (!item.adminOnly || identity?.permissions.admin) &&
      (!item.gatedOnly || hasGatedFeatures),
  );

  // The palette offers exactly what the sidebar offers, plus the two switches
  // people otherwise hunt for in the header.
  const commands: Command[] = [
    ...visibleNav.map((item) => ({
      id: item.to,
      label: t(item.labelKey),
      to: item.to,
      hint: t(item.section),
    })),
    {
      id: 'theme',
      label: t('shell.toggleTheme'),
      run: toggleColorScheme,
      hint: t('nav.sectionInstallation'),
    },
    ...LOCALES.map((option) => ({
      id: `locale-${option}`,
      label: LOCALE_LABELS[option],
      run: () => setLocale(option),
      hint: t('shell.language'),
    })),
  ];

  return (
    <AppShell
      header={{ height: 56 }}
      navbar={{
        width: rail ? NAV_WIDTH_RAIL : NAV_WIDTH_EXPANDED,
        breakpoint: 'sm',
        collapsed: { mobile: !opened },
      }}
      padding="lg"
      transitionDuration={reduceMotion ? 0 : NAV_MOTION.width}
      transitionTimingFunction={NAV_MOTION.curve}
      styles={{
        // The width and the content's left edge are one movement, so they run
        // on one duration and one curve. Without the second transition the page
        // body jumps to its new margin while the sidebar is still travelling.
        navbar: {
          transition: motion('width', NAV_MOTION.width),
          overflow: 'hidden',
        },
        main: { transition: motion('padding-inline-start', NAV_MOTION.width) },
      }}
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

      <AppShell.Navbar p="xs" style={{ display: 'flex', flexDirection: 'column' }}>
        <ScrollArea style={{ flex: 1 }} type={rail ? 'never' : 'auto'}>
          {SECTIONS.map((section) => {
            const items = visibleNav.filter((item) => item.section === section);
            if (items.length === 0) return null;
            return (
              <Box key={section} mb="xs">
                {/* The section heading and the rule that stands in for it are
                    stacked in the same box and cross-fade, so the grouping
                    survives the collapse and nothing above or below it moves. */}
                <Box pos="relative" h={26}>
                  <Text
                    size="xs"
                    c="dimmed"
                    fw={600}
                    tt="uppercase"
                    px="sm"
                    pt="xs"
                    style={labelStyle}
                  >
                    {t(section)}
                  </Text>
                  <Divider
                    pos="absolute"
                    left={12}
                    right={12}
                    top={16}
                    style={{
                      opacity: rail ? 1 : 0,
                      transition: motion('opacity', rail ? NAV_MOTION.labelIn : NAV_MOTION.labelOut),
                    }}
                  />
                </Box>
                {items.map((item) => {
                  const Icon = item.icon;
                  const label = t(item.labelKey);
                  const active = item.end
                    ? location.pathname === item.to
                    : location.pathname.startsWith(item.to);
                  return (
                    <Tooltip
                      key={item.to}
                      label={label}
                      position="right"
                      withArrow
                      openDelay={220}
                      // Only in the rail: with the label right there, a tooltip
                      // repeating it is noise that follows the pointer.
                      disabled={!rail}
                    >
                      <NavLink
                        component={RouterNavLink}
                        to={item.to}
                        aria-label={label}
                        label={<span style={labelStyle}>{label}</span>}
                        leftSection={<Icon size={18} />}
                        active={active}
                        onClick={close}
                        style={{ borderRadius: 'var(--mantine-radius-sm)' }}
                      />
                    </Tooltip>
                  );
                })}
              </Box>
            );
          })}
        </ScrollArea>

        <Divider my={4} />
        <Tooltip
          label={rail ? t('shell.navExpand') : t('shell.navCollapse')}
          position="right"
          withArrow
          openDelay={220}
        >
          <UnstyledButton
            onClick={() => setRailed((value) => !value)}
            aria-label={rail ? t('shell.navExpand') : t('shell.navCollapse')}
            aria-expanded={!rail}
            p="sm"
            style={{ borderRadius: 'var(--mantine-radius-sm)' }}
          >
            <Group gap="sm" wrap="nowrap">
              {rail ? (
                <IconLayoutSidebarLeftExpand size={18} />
              ) : (
                <IconLayoutSidebarLeftCollapse size={18} />
              )}
              <Text size="sm" c="dimmed" style={labelStyle}>
                {t('shell.navCollapse')}
              </Text>
            </Group>
          </UnstyledButton>
        </Tooltip>
      </AppShell.Navbar>

      <AppShell.Main>
        <CommandPalette commands={commands} />
        <Outlet />
      </AppShell.Main>
    </AppShell>
  );
}
