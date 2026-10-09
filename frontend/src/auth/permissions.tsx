import { Alert } from '@mantine/core';
import { useI18n } from '../i18n/I18nProvider';
import { useOptionalAuth } from './AuthProvider';

/** What the backend's roles grant beyond reading; see internal/authz. */
export type Permission = 'respond' | 'edit' | 'admin';

/**
 * Whether the signed-in principal may do `permission`, from /auth/me.
 *
 * The backend stays the authority; this only keeps the UI from offering an
 * action that will come back 403 after somebody has filled in a form. Unknown
 * identity counts as allowed, so a missing flag never hides a working control.
 */
export function useCan(permission: Permission): boolean {
  return useOptionalAuth()?.identity?.permissions?.[permission] ?? true;
}

/**
 * Says once, at the top of a page, that this role can look but not change —
 * the controls below are disabled rather than silently missing.
 */
export function RoleNotice({ need }: { need: Permission }) {
  const { t } = useI18n();
  const identity = useOptionalAuth()?.identity;
  const allowed = useCan(need);
  if (allowed) return null;
  return (
    <Alert color="gray" mb="md" title={t('permissions.readOnlyTitle', { role: identity?.role || '—' })}>
      {t(need === 'respond' ? 'permissions.needRespond' : need === 'edit' ? 'permissions.needEdit' : 'permissions.needAdmin')}
    </Alert>
  );
}
