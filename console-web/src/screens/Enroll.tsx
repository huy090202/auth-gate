import { Badge, Button, Group, Text } from '@mantine/core';
import { useState } from 'react';
import { Navigate, useNavigate } from 'react-router';
import { consoleTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { fmt, useStrings } from '../lib/i18n';
import { Loading, LoadFailed, hasFactor, homeFor, useMe } from '../session';
import { Feedback, PasswordForm, PinForm, registerPasskey, useAction, withRestart } from './factors';

/** First sign-in: replace the temporary password, then add a first second factor. */
export function Enroll() {
  const t = useStrings();
  useTitle(consoleTitle(t.screens.enroll));
  const nav = useNavigate();
  const { me, status, error: loadError, reload, retry, clear } = useMe();
  const pwd = useAction();
  const factor = useAction();
  const [signingOut, setSigningOut] = useState(false);

  if (status === 'loading') return <Loading />;
  if (status === 'error') return <LoadFailed error={loadError} onRetry={retry} />;
  if (status !== 'enroll' || !me) return <Navigate to={homeFor(status)} replace />;

  // The backend moves the session to full once both steps are done; reload() picks that up
  // and the guard above sends the user on.
  const refreshed = reload;
  const needPassword = me.mustChangePassword;
  const needFactor = !hasFactor(me.factors);
  const total = (needPassword ? 1 : 0) + (needFactor ? 1 : 0);
  // The step the user is on: the password first, because that is what the temporary one asks.
  const current = needPassword ? 'password' : 'factor';

  const signOut = async () => {
    setSigningOut(true);
    try {
      await api('/api/auth/logout', { method: 'POST' });
    } catch {
      /* already gone */
    }
    clear();
    nav('/sign-in', { replace: true });
  };

  const stepHead = (n: number, title: string) => (
    <div className="step-head">
      <h2>{title}</h2>
      <Badge variant="default" size="sm">{fmt(t.enroll.stepOf, { n, total })}</Badge>
    </div>
  );

  return (
    <div className="auth-stage">
      <div className="enroll-col">
        <div className="page-head">
          <h1>{t.screens.enroll}</h1>
          <Text c="dimmed" fz="sm">{t.enroll.intro}</Text>
        </div>
        <ol className="steps">
          {needPassword && (
            <li className="step glass">
              {stepHead(1, t.enroll.stepPassword)}
              <Text fz="sm" c="dimmed" mt={6}>{t.enroll.stepPasswordHint}</Text>
              <PasswordForm currentLabel={t.password.temporary} disabled={pwd.busy || pwd.waiting}
                onSave={(cur, next) =>
                  pwd.run(async () => {
                    await api('/api/me/password', { body: { currentPassword: cur, newPassword: next } });
                    await reload();
                  })} />
              <Feedback error={pwd.error} note={pwd.note} />
            </li>
          )}
          {needFactor && (
            <li className="step glass" aria-current={current === 'factor' ? 'step' : undefined}>
              {stepHead(needPassword ? 2 : 1, t.enroll.stepFactor)}
              <Text fz="sm" c="dimmed" mt={6}>{t.enroll.stepFactorHint}</Text>
              <PinForm submitLabel={t.pin.set} disabled={factor.busy || factor.waiting}
                onSave={(pin) =>
                  factor.run(async () => {
                    await api('/api/me/pin', { body: { pin } });
                    await refreshed();
                  })} />
              <Group mt="sm">
                <Button variant="default" loading={factor.busy} disabled={factor.waiting}
                  onClick={() =>
                    void factor.run(async () => {
                      await withRestart(() => registerPasskey());
                      await refreshed();
                    })}>
                  {t.passkey.add}
                </Button>
              </Group>
              <Feedback error={factor.error} note={factor.note} />
            </li>
          )}
        </ol>
        <Button variant="subtle" color="gray" size="compact-sm" onClick={() => void signOut()} loading={signingOut} className="enroll-out">
          {t.common.signOut}
        </Button>
      </div>
    </div>
  );
}
