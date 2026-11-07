import { Button, Loader, Stack, Text } from '@mantine/core';
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Navigate, useNavigate } from 'react-router';
import { ApiError, api, onApiFailure } from './lib/api';
import { explain } from './lib/explain';
import { useStrings } from './lib/i18n';

export type Passkey = { credentialId: string; createdAt: string; lastUsedAt?: string | null };
export type Me = {
  username: string;
  role: 'master' | 'admin';
  stage: 'half' | 'enroll' | 'full';
  mustChangePassword: boolean;
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

  const clear = useCallback(() => {
    loaded.current = false;
    setMe(null);
    setError(null);
    setStatus('anon');
  }, []);

  const reload = useCallback(async () => {
    try {
      const m = await api<Me>('/api/me');
      loaded.current = true;
      setMe(m);
      setStatus(m.stage);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return; // the failure hook has cleared state
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
        } else if (e.status === 401 && e.code === 'second_factor_required') {
          void reload().catch(() => {});
          nav('/sign-in/second-factor', { replace: true });
        } else if (e.status === 403 && e.code === 'enrollment_required') {
          void reload().catch(() => {});
          nav('/enroll', { replace: true });
        }
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

export function Loading() {
  const t = useStrings();
  return (
    <div className="auth-stage">
      <Loader size="sm" aria-label={t.loading} />
    </div>
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

export function LoadFailed({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const t = useStrings();
  return (
    <div className="auth-stage">
      <Stack gap="sm" align="flex-start" className="auth-card glass">
        <Text c="red.7" fz="sm" role="alert">{explain(error, t)}</Text>
        <Button size="sm" variant="light" onClick={onRetry}>{t.common.retry}</Button>
      </Stack>
    </div>
  );
}

/** Renders children only for a full session; otherwise sends the user to the step they are on. */
export function SessionGuard({ role, children }: {
  role?: 'master';
  children: (s: { me: Me; reload: () => Promise<void> }) => ReactNode;
}) {
  const { me, status, error, reload, retry } = useMe();
  if (status === 'loading') return <Loading />;
  if (status === 'error') return <LoadFailed error={error} onRetry={retry} />;
  if (status !== 'full' || !me) return <Navigate to={homeFor(status)} replace />;
  if (role === 'master' && me.role !== 'master') return <Navigate to="/clients" replace />;
  return <>{children({ me, reload })}</>;
}
