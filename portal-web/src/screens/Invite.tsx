import { Button, Group, Stack, Text } from '@mantine/core';
import { useEffect, useState } from 'react';
import { Navigate, useNavigate, useParams } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { ApiError, api } from '../lib/api';
import { explain } from '../lib/explain';
import { fmt, useStrings } from '../lib/i18n';
import { forgetInvite, isUlid, pendingInvite, rememberInvite } from '../lib/invite';
import { clientsPath } from '../lib/tenants';
import { useFragmentToken } from '../lib/useFragmentToken';
import { Loading, LoadFailed, homeFor, useMe } from '../session';
import { Card, useSubmit } from './parts';

type Lookup = { orgName: string; inviterName: string; role: 'owner' | 'admin' | 'member'; email: string; emailMatches?: boolean };

/**
 * The link in an invitation email: /invite/<tenantId>#token=... It works signed out (the invite is
 * shown, and the visitor is sent to sign in or sign up, then brought back here) and signed in
 * (accept or decline). The token is kept in this tab's storage only while the visitor is away.
 */
export function Invite() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.invite));
  const nav = useNavigate();
  const { tenantId = '' } = useParams();
  const { me, status, error: sessionError, retry, clear } = useMe();
  const fragmentToken = useFragmentToken();
  const [token] = useState(() => {
    if (fragmentToken) return fragmentToken;
    const p = pendingInvite();
    return p && p.tenantId === tenantId ? p.token : '';
  });
  const [invite, setInvite] = useState<Lookup | null>(null);
  const [lookupError, setLookupError] = useState('');
  const { busy, error, run } = useSubmit();
  const [attempt, setAttempt] = useState(0);
  const valid = isUlid(tenantId) && token !== '';
  const leave = () => {
    forgetInvite();
    nav('/', { replace: true });
  };

  useEffect(() => {
    if (!valid) return;
    let live = true;
    setLookupError('');
    api<Lookup>(`/api/invites/${tenantId}/lookup`, { body: { token } }).then(
      (r) => live && setInvite(r),
      (e: unknown) => {
        if (!live) return;
        if (e instanceof ApiError && e.code === 'invite_not_found') {
          forgetInvite();
          setLookupError(t.invite.gone);
        } else {
          setLookupError(explain(e, t));
        }
      },
    );
    return () => {
      live = false;
    };
    // The strings only matter for the sentence of a failure.
  }, [tenantId, token, valid, attempt]);

  const halfOrEnroll = status === 'half' || status === 'enroll';
  const wrongAddress = invite?.emailMatches === false;
  useEffect(() => {
    // A session that is not full finishes its sign-in first; the invite is remembered to come back to.
    if (invite && halfOrEnroll) rememberInvite({ tenantId, token, email: invite.email });
    // The wrong account cannot use this invite; leaving it stored would bring them straight back.
    if (wrongAddress) forgetInvite();
  }, [invite, halfOrEnroll, wrongAddress, tenantId, token]);

  // No token: a reload stripped the fragment and nothing is stored, so only the mail's link can help.
  if (!valid) return <Card title={t.screens.invite}><Text fz="sm" role="alert">{token === '' ? t.invite.reopen : t.invite.gone}</Text></Card>;
  if (lookupError) {
    return (
      <Card title={t.screens.invite}>
        <Text fz="sm" role="alert">{lookupError}</Text>
        <Group>
          {lookupError !== t.invite.gone && <Button onClick={() => { setInvite(null); setAttempt((n) => n + 1); }}>{t.common.retry}</Button>}
          <Button variant="light" onClick={leave}>{t.invite.back}</Button>
        </Group>
      </Card>
    );
  }
  if (status === 'loading' || !invite) return <Loading />;
  if (status === 'error') return <LoadFailed error={sessionError} onRetry={retry} />;

  if (status === 'half' || status === 'enroll') return <Navigate to={homeFor(status)} replace />;

  const who = invite.inviterName === '' ? fmt(t.invite.someone, { org: invite.orgName }) : invite.inviterName;
  const summary = (
    <Stack gap={4}>
      <Text fz="sm">{fmt(t.invite.summary, { who, org: invite.orgName, role: t.roles[invite.role] })}</Text>
      <Text fz="sm" c="dimmed">{fmt(t.invite.sentTo, { email: invite.email })}</Text>
    </Stack>
  );

  if (status === 'anon' || !me) {
    const go = (to: string) => {
      rememberInvite({ tenantId, token, email: invite.email });
      nav(to);
    };
    return (
      <Card title={t.screens.invite}>
        {summary}
        <Text c="dimmed" fz="sm">{t.invite.signedOut}</Text>
        <Group>
          <Button onClick={() => go('/sign-in')}>{t.invite.signIn}</Button>
          <Button variant="light" onClick={() => go('/sign-up')}>{t.invite.signUp}</Button>
        </Group>
      </Card>
    );
  }

  const signOut = async () => {
    // They will need the invite again once they sign in with the right account.
    rememberInvite({ tenantId, token, email: invite.email });
    try {
      await api('/api/auth/logout', { method: 'POST' });
    } catch {
      /* a session that is already gone is what we wanted */
    }
    clear();
    nav('/sign-in', { replace: true });
  };

  if (invite.emailMatches === false) {
    return (
      <Card title={t.screens.invite}>
        {summary}
        <Text fz="sm" role="alert">{fmt(t.invite.wrongAccount, { email: invite.email, you: me.email })}</Text>
        <Group>
          <Button onClick={() => void signOut()}>{t.common.signOut}</Button>
          <Button variant="light" onClick={leave}>{t.invite.back}</Button>
        </Group>
      </Card>
    );
  }

  return (
    <Card title={t.screens.invite}>
      {summary}
      {error && <Text c="red.7" fz="sm" role="alert">{error}</Text>}
      <Group>
        <Button loading={busy} onClick={() => void run(async () => {
          await api(`/api/invites/${tenantId}/accept`, { body: { token } });
          forgetInvite();
          // The shell loads the workspace list when it mounts, so the new organisation is in it.
          nav(clientsPath(tenantId), { replace: true });
        })}>{t.invite.accept}</Button>
        <Button variant="default" disabled={busy} onClick={() => void run(async () => {
          await api(`/api/invites/${tenantId}/decline`, { body: { token } });
          forgetInvite();
          nav('/', { replace: true });
        })}>{t.invite.decline}</Button>
        {error && <Button variant="subtle" disabled={busy} onClick={leave}>{t.invite.back}</Button>}
      </Group>
    </Card>
  );
}
