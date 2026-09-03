import { Center, Loader } from '@mantine/core';
import { Suspense, lazy } from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';
import { EnterprisePage } from './pages/EnterprisePage';
import { useAuth } from './auth/AuthProvider';
import { SignInScreen } from './auth/SignInScreen';
import { Shell } from './components/Shell';
import { HomePage } from './pages/HomePage';
import { AlertGroupsPage } from './pages/AlertGroupsPage';
import { AlertGroupDetailPage } from './pages/AlertGroupDetailPage';

// The three pages above are loaded up front because they are where somebody
// woken by a page lands: the overview, the group list, and the one group they
// were notified about. Everything below is configuration and review, reached by
// a deliberate click on a laptop, so it is fetched when first opened — which
// keeps the initial download to what a responder needs on a phone.
const AlertsPage = lazy(() =>
  import('./pages/AlertsPage').then((m) => ({ default: m.AlertsPage })),
);
const UsersPage = lazy(() =>
  import('./pages/UsersPage').then((m) => ({ default: m.UsersPage })),
);
const TeamsPage = lazy(() =>
  import('./pages/TeamsPage').then((m) => ({ default: m.TeamsPage })),
);
const IntegrationsPage = lazy(() =>
  import('./pages/IntegrationsPage').then((m) => ({
    default: m.IntegrationsPage,
  })),
);
const IntegrationDetailPage = lazy(() =>
  import('./pages/IntegrationDetailPage').then((m) => ({
    default: m.IntegrationDetailPage,
  })),
);
const EscalationChainsPage = lazy(() =>
  import('./pages/EscalationChainsPage').then((m) => ({
    default: m.EscalationChainsPage,
  })),
);
const SchedulesPage = lazy(() =>
  import('./pages/SchedulesPage').then((m) => ({ default: m.SchedulesPage })),
);
const ScheduleDetailPage = lazy(() =>
  import('./pages/ScheduleDetailPage').then((m) => ({
    default: m.ScheduleDetailPage,
  })),
);
const NotificationsPage = lazy(() =>
  import('./pages/NotificationsPage').then((m) => ({
    default: m.NotificationsPage,
  })),
);
const InsightsPage = lazy(() =>
  import('./pages/InsightsPage').then((m) => ({ default: m.InsightsPage })),
);
const AuditPage = lazy(() =>
  import('./pages/AuditPage').then((m) => ({ default: m.AuditPage })),
);
const ReadinessPage = lazy(() =>
  import('./pages/ReadinessPage').then((m) => ({ default: m.ReadinessPage })),
);
const SettingsPage = lazy(() =>
  import('./pages/SettingsPage').then((m) => ({ default: m.SettingsPage })),
);
const MaintenancePage = lazy(() =>
  import('./pages/MaintenancePage').then((m) => ({ default: m.MaintenancePage })),
);
const SetupPage = lazy(() =>
  import('./pages/SetupPage').then((m) => ({ default: m.SetupPage })),
);

export function App() {
  const { state } = useAuth();

  if (state === 'checking') {
    return (
      <Center mih="100vh">
        <Loader />
      </Center>
    );
  }

  if (state === 'unauthenticated') {
    return <SignInScreen />;
  }

  return (
    <Routes>
      <Route element={<Shell />}>
        <Route index element={<HomePage />} />
        <Route path="alert-groups" element={<AlertGroupsPage />} />
        <Route path="alert-groups/:id" element={<AlertGroupDetailPage />} />
        <Route
          path="alerts"
          element={
            <Deferred>
              <AlertsPage />
            </Deferred>
          }
        />
        <Route
          path="users"
          element={
            <Deferred>
              <UsersPage />
            </Deferred>
          }
        />
        <Route
          path="teams"
          element={
            <Deferred>
              <TeamsPage />
            </Deferred>
          }
        />
        <Route
          path="integrations"
          element={
            <Deferred>
              <IntegrationsPage />
            </Deferred>
          }
        />
        <Route
          path="integrations/:id"
          element={
            <Deferred>
              <IntegrationDetailPage />
            </Deferred>
          }
        />
        <Route
          path="escalation-chains"
          element={
            <Deferred>
              <EscalationChainsPage />
            </Deferred>
          }
        />
        <Route
          path="schedules"
          element={
            <Deferred>
              <SchedulesPage />
            </Deferred>
          }
        />
        <Route
          path="schedules/:id"
          element={
            <Deferred>
              <ScheduleDetailPage />
            </Deferred>
          }
        />
        <Route
          path="notifications"
          element={
            <Deferred>
              <NotificationsPage />
            </Deferred>
          }
        />
        <Route
          path="insights"
          element={
            <Deferred>
              <InsightsPage />
            </Deferred>
          }
        />
        <Route
          path="audit"
          element={
            <Deferred>
              <AuditPage />
            </Deferred>
          }
        />
        <Route
          path="maintenance"
          element={
            <Deferred>
              <MaintenancePage />
            </Deferred>
          }
        />
        <Route
          path="setup"
          element={
            <Deferred>
              <SetupPage />
            </Deferred>
          }
        />
        <Route
          path="readiness"
          element={
            <Deferred>
              <ReadinessPage />
            </Deferred>
          }
        />
        <Route
          path="settings"
          element={
            <Deferred>
              <SettingsPage />
            </Deferred>
          }
        />
        <Route path="enterprise" element={<EnterprisePage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}

/** Deferred shows the same spinner as the rest of the app while a page arrives. */
function Deferred({ children }: { children: React.ReactNode }) {
  return (
    <Suspense
      fallback={
        <Center mih={200}>
          <Loader />
        </Center>
      }
    >
      {children}
    </Suspense>
  );
}
