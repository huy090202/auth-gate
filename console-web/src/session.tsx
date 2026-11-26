import { Button, Loader, Stack, Text } from '@mantine/core';
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Navigate, useNavigate } from 'react-router';
import { ApiError, api, onApiFailure } from './lib/api';
import { SiteHeader } from './controls';
import { explain } from './lib/explain';
import { useStrings } from './lib/i18n';
import { useToast } from './toast';

export type Passkey = { credentialId: string; createdAt: string; lastUsedAt?: string | null };
export type Me = {
  username: string;
  role: 'master' | 'admin';
  stage: 'half' | 'enroll' | 'full';
  mustChangePassword: boolean;
  /** Site-relative URL of the admin's picture; absent when there is none. */
  avatarUrl?: string;
  // A boolean for a half session or an enrolling one that has proved nothing yet: passkey
  // metadata stays hidden from a session that has only proved the password.
  factors: { pin: boolean; webauthn: Passkey[] | boolean };
};
export type Status = 'loading' | 'anon' | 'half' | 'enroll' | 'full' | 'error';

/** How many passkeys, whether the list or only the "has any" flag came back. */
export const passkeyCount = (f: Me['factors']) => (Array.isArray(f.webauthn) ? f.webauthn.length : f.webauthn ? 1 : 0);
export const hasFactor = (f: Me['factors']) => f.pin || passkeyCount(f) > 0;

type Ctx = { me: Me | null; status: Status; error: unknown; reload: () => Promise<void>; retry: () => void; clear: () => void };
const SessionCtx = createContext<Ctx | null>(null);

/**
 * Holds the current session (GET /api/me) for the whole app, and is the one place that reacts
 * to a failed call: a 401 `unauthorized` means the session is gone (expired, signed out, or the
 * admin was reset elsewhere), so it clears state and goes to sign-in; the two stage refusals go
 * to the screen that finishes the stage.
 */
export function SessionProvider({ children }: { children: ReactNode }) {
  const nav = useNavigate();
  const [me, setMe] = useState<Me | null>(null);
  const [status, setStatus] = useState<Status>('loading');
  const [error, setError] = useState<unknown>(null);
  const loaded = useRef(false);
  const toast = useToast();

  // The toasts belong to the user who is leaving: the sign-in page must not show their notes.
  const clear = useCallback(() => {
    loaded.current = false;
    toast.clear();
    setMe(null);
    setError(null);
    setStatus('anon');
  }, [toast]);

  const reload = useCallback(async () => {
    try {
      const m = await api<Me>('/api/me');
      loaded.current = true;
      setMe(m);
      setStatus(m.stage);
    } catch (e) {
      // Only `unauthorized` is the hook's to handle (it has cleared state). Any other failure of /me,
      // a 401 with another code included, must reach the error branch or the caller.
      if (e instanceof ApiError && e.status === 401 && e.code === 'unauthorized') return;
      if (loaded.current) throw e; // a page already showing keeps showing; the caller reports
      setMe(null);
      setError(e);
      setStatus('error');
    }
  }, []);

  const retry = useCallback(() => {
    setStatus('loading');
    void reload();
  }, [reload]);

  useEffect(() => {
    void reload().catch(() => {});
  }, [reload]);

  useEffect(
    () =>
      onApiFailure((e) => {
        if (e.status === 401 && e.code === 'unauthorized') {
          clear();
          nav('/sign-in', { replace: true });
          return;
        }
        const toSecondFactor = e.status === 401 && e.code === 'second_factor_required';
        const toEnroll = e.status === 403 && e.code === 'enrollment_required';
        // /api/me answers every stage, so a stage refusal from it is a broken backend, not a cue
        // to refetch it (that could loop): reload()'s own catch shows it as an error.
        if ((!toSecondFactor && !toEnroll) || e.path === '/api/me') return;
        // Refresh first, then navigate: the target screen must see the new status or it bounces back.
        void (async () => {
          try {
            await reload();
          } catch {
            // A failed refresh means the new status is unknown: stay put (reload() has shown the error
            // if no page was up) rather than navigate on a stale status and bounce back.
            return;
          }
          nav(toSecondFactor ? '/sign-in/second-factor' : '/enroll', { replace: true });
        })();
      }),
    [clear, nav, reload],
  );

  const value = useMemo(() => ({ me, status, error, reload, retry, clear }), [me, status, error, reload, retry, clear]);
  return <SessionCtx.Provider value={value}>{children}</SessionCtx.Provider>;
}

export function useMe(): { me: Me | null; status: Status; error: unknown; reload: () => Promise<void>; retry: () => void; clear: () => void } {
  const c = useContext(SessionCtx);
  if (!c) throw new Error('useMe outside SessionProvider');
  return c;
}

/** `header`: also draw the plain header, for guarded screens, whose own frame is not there yet. */
export function Loading({ header }: { header?: boolean }) {
  const t = useStrings();
  return (
    <>
      {header && <SiteHeader />}
      <div className="auth-stage">
        <Loader size="sm" aria-label={t.loading} />
      </div>
    </>
  );
}

/** Where a session of this status belongs. */
export function homeFor(status: Status): string {
  switch (status) {
    case 'half':
      return '/sign-in/second-factor';
    case 'enroll':
      return '/enroll';
    case 'full':
      return '/clients';
    default:
      return '/sign-in';
  }
}

export function LoadFailed({ error, onRetry, header }: { error: unknown; onRetry: () => void; header?: boolean }) {
  const t = useStrings();
  return (
    <>
      {header && <SiteHeader />}
      <div className="auth-stage">
        <Stack gap="sm" align="flex-start" className="auth-card glass">
          <Text c="red.7" fz="sm" role="alert">{explain(error, t)}</Text>
          <Button size="sm" variant="light" onClick={onRetry}>{t.common.retry}</Button>
        </Stack>
      </div>
    </>
  );
}

/** Renders children only for a full session; otherwise sends the user to the step they are on. */
export function SessionGuard({ role, children }: {
  role?: 'master';
  children: (s: { me: Me; reload: () => Promise<void> }) => ReactNode;
}) {
  const { me, status, error, reload, retry } = useMe();
  if (status === 'loading') return <Loading header />;
  if (status === 'error') return <LoadFailed error={error} onRetry={retry} header />;
  if (status !== 'full' || !me) return <Navigate to={homeFor(status)} replace />;
  if (role === 'master' && me.role !== 'master') return <Navigate to="/clients" replace />;
  return <>{children({ me, reload })}</>;
}
