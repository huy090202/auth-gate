import { Anchor, Badge, Button, Group, Text, TextInput } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { Link } from 'react-router';
import { consoleTitle, useTitle } from '../brand';
import { ApiError, api } from '../lib/api';
import { labelOf } from '../lib/audit';
import { formatDate } from '../lib/format';
import { fmt, useStrings } from '../lib/i18n';
import { currentSuspension, tenantName, type UserView } from '../lib/operator';
import { SessionGuard, type Me } from '../session';
import { Shell } from '../shell';
import { useStepUp } from '../stepup';
import { PageHead, ConfirmDialog } from '../ui';
import { Feedback, useAction } from './factors';
import { ReasonDialog } from './ReasonDialog';

function UsersBody({ me }: { me: Me }) {
  const t = useStrings();
  useTitle(consoleTitle(t.screens.users));
  const { stepUp, modal } = useStepUp();
  const [email, setEmail] = useState('');
  const [user, setUser] = useState<UserView | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [reasonOpen, setReasonOpen] = useState(false);
  const [confirmReset, setConfirmReset] = useState(false);
  const search = useAction();
  const act = useAction();

  const read = async (address: string) => {
    try {
      setUser(await api<UserView>(`/api/users?email=${encodeURIComponent(address)}`));
      setNotFound(false);
    } catch (e) {
      if (e instanceof ApiError && e.code === 'not_found') {
        setUser(null);
        setNotFound(true);
        return;
      }
      throw e;
    }
  };
  const find = (ev: FormEvent) => {
    ev.preventDefault();
    const address = email.trim();
    if (address === '') return;
    void search.run(() => read(address), '', 'find');
  };

  const base = user ? `/api/users/${encodeURIComponent(user.userId)}` : '';
  /** Runs one user write behind a step-up bound to the user id, then reads the user again. */
  const write = (action: Parameters<typeof stepUp>[0], suffix: string, body: unknown, note: string | ((r: { dueAt?: string }) => string), key: string) =>
    act.run(async () => {
      if (!user) return false;
      const r = await stepUp(action, (token) => api<unknown>(`${base}/${suffix}`, { method: 'POST', body, stepUpToken: token }), user.userId);
      if (!r) return false;
      await read(user.email);
      return typeof note === 'function' ? note(r as { dueAt?: string }) : note;
    }, '', key);

  const suspended = !!user?.suspendedAt;
  const last = user ? currentSuspension(user.lastSuspension, user.suspendedAt) : null;
  const btnProps = (key: string) => ({ loading: act.loading(key), disabled: act.waiting || (act.busy && !act.loading(key)) });
  return (
    <Shell me={me}>
      <PageHead title={t.screens.users} intro={t.users.intro} />
      <section className="panel glass">
        <form className="toolbar" onSubmit={find}>
          <TextInput className="toolbar-grow" type="email" label={t.users.emailLabel} value={email} maxLength={320} autoComplete="off" spellCheck={false}
            onChange={(e) => setEmail(e.currentTarget.value)} />
          <Button type="submit" loading={search.busy} disabled={email.trim() === ''}>{t.users.find}</Button>
        </form>
        <Feedback error={search.error} />
        {notFound && <Text c="dimmed" mt="md" role="status">{t.users.notFound}</Text>}
      </section>
      {user && (
        <section className="panel glass user-card" style={{ marginTop: 20 }}>
          <Group gap="xs">
            <h2>{user.displayName === '' ? user.email : user.displayName}</h2>
            <Badge variant="light" color={suspended ? 'orange' : 'canopy'}>{suspended ? t.users.suspended : t.users.active}</Badge>
          </Group>
          <dl className="detail-grid" style={{ marginTop: 14 }}>
            <dt>{t.users.email}</dt>
            <dd>{user.email}</dd>
            <dt>{t.users.emailStatus}</dt>
            <dd>{user.emailVerifiedAt ? t.users.verified : t.users.unverified}</dd>
            <dt>{t.users.status}</dt>
            <dd>{suspended ? t.users.suspended : t.users.active}</dd>
            <dt>{t.users.factors}</dt>
            <dd>{user.factorCount}</dd>
            <dt>{t.users.lastSignIn}</dt>
            <dd>{user.lastSignInAt ? formatDate(user.lastSignInAt, t) : t.users.never}</dd>
            <dt>{t.users.tenantsTitle}</dt>
            <dd>
              {user.tenants.length === 0 ? <span className="cell-muted">{t.users.noTenants}</span> : (
                <ul className="plain-list">
                  {user.tenants.map((x) => (
                    <li key={x.tenantId}>
                      <Anchor component={Link} to={`/tenants/${encodeURIComponent(x.tenantId)}`}>{tenantName(x, t.tenants.personalName)}</Anchor>
                      {' '}<span className="cell-muted">({labelOf(t.users.roles, x.role)})</span>
                    </li>
                  ))}
                </ul>
              )}
            </dd>
          </dl>
          {last && (
            <Text fz="sm" c="dimmed" mt="md">
              {fmt(t.users.lastSuspension, { admin: last.username, date: formatDate(last.at, t), reason: last.reason })}
            </Text>
          )}
          <Group mt="lg" gap="sm">
            {suspended ? (
              <Button variant="light" {...btnProps('unsuspend')} onClick={() => void write('user-unsuspend', 'unsuspend', undefined, t.users.unsuspendDone, 'unsuspend')}>
                {t.users.unsuspend}
              </Button>
            ) : (
              <Button variant="light" color="red" {...btnProps('suspend')} onClick={() => setReasonOpen(true)}>{t.users.suspend}</Button>
            )}
            {!user.emailVerifiedAt && (
              <Button variant="light" {...btnProps('resend')} onClick={() => void write('user-resend-verification', 'resend-verification', undefined, t.users.resendDone, 'resend')}>
                {t.users.resend}
              </Button>
            )}
            <Button variant="light" color="red" {...btnProps('reset')} onClick={() => setConfirmReset(true)}>{t.users.reset}</Button>
          </Group>
          <Feedback error={act.error} />
        </section>
      )}
      {user && reasonOpen && (
        <ReasonDialog title={fmt(t.users.reasonDialogTitle, { email: user.email })} label={t.users.reasonLabel} confirmLabel={t.reasonDialog.confirm}
          onCancel={() => setReasonOpen(false)}
          onConfirm={(reason) => {
            setReasonOpen(false);
            void write('user-suspend', 'suspend', { reason }, t.users.suspendDone, 'suspend');
          }} />
      )}
      {user && confirmReset && (
        <ConfirmDialog title={t.users.resetTitle} body={fmt(t.users.resetBody, { email: user.email })} confirmLabel={t.users.resetConfirm} danger
          onCancel={() => setConfirmReset(false)}
          onConfirm={() => {
            setConfirmReset(false);
            void write('user-factor-reset', 'factor-reset', undefined, (r: { dueAt?: string }) =>
              r.dueAt ? fmt(t.users.resetDone, { date: formatDate(r.dueAt, t) }) : t.users.resetStarted, 'reset');
          }} />
      )}
      {modal}
    </Shell>
  );
}

export function Users() {
  return <SessionGuard>{({ me }) => <UsersBody me={me} />}</SessionGuard>;
}
