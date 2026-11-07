import { Badge, Button, Group, Text } from '@mantine/core';
import { useState } from 'react';
import { consoleTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { formatDate } from '../lib/format';
import { fmt, useStrings } from '../lib/i18n';
import { monoFont } from '../theme';
import { Shell } from '../shell';
import { SessionGuard, passkeyCount, type Me, type Passkey } from '../session';
import { useStepUp } from '../stepup';
import { Feedback, PasswordForm, PinForm, registerPasskey, useAction, withRestart } from './factors';

function KeyIcon() {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="8" cy="15" r="4" />
      <path d="m11 12 8-8m-3 3 3 3" />
    </svg>
  );
}

type Props = { me: Me; reload: () => Promise<void>; stepUp: ReturnType<typeof useStepUp>['stepUp'] };

function PasswordSection({ reload, stepUp }: Props) {
  const t = useStrings();
  const a = useAction();
  return (
    <section className="panel glass">
      <h2>{t.account.passwordTitle}</h2>
      <Text fz="sm" c="dimmed" mt={6}>{t.account.passwordNote}</Text>
      <PasswordForm currentLabel={t.password.current} disabled={a.busy || a.waiting} loading={a.loading('pwd')}
        onSave={(current, next) =>
          a.run(async () => {
            // A wrong current password keeps the token valid, but the next try asks again.
            const done = await stepUp('change-password', (token) =>
              api('/api/me/password', { body: { currentPassword: current, newPassword: next }, stepUpToken: token }));
            if (!done) return false; // modal closed: not an error, the form keeps its input
            await reload();
          }, t.password.changed, 'pwd')} />
      <Feedback error={a.error} note={a.note} />
    </section>
  );
}

function PinSection({ me, reload, stepUp }: Props) {
  const t = useStrings();
  const a = useAction();
  const [editing, setEditing] = useState(false);
  const hasPin = me.factors.pin;
  const passkeys = passkeyCount(me.factors);
  const save = (pin: string) =>
    a.run(async () => {
      const done = await stepUp('add-factor', (token) => api('/api/me/pin', { body: { pin }, stepUpToken: token }));
      if (!done) return false;
      setEditing(false);
      await reload();
    }, t.pin.saved, 'save');
  const remove = () =>
    a.run(async () => {
      const done = await stepUp('remove-factor', (token) => api('/api/me/pin', { method: 'DELETE', stepUpToken: token }));
      if (!done) return false;
      await reload();
    }, t.pin.removed, 'remove');
  const lastFactor = passkeys === 0;
  const showForm = editing || !hasPin;
  return (
    <section className="panel glass">
      <Group justify="space-between" wrap="nowrap" gap="sm">
        <Group gap="xs">
          <h2>{t.account.pinTitle}</h2>
          <Badge variant={hasPin ? 'light' : 'default'} size="sm">{hasPin ? t.account.set : t.account.notSet}</Badge>
        </Group>
        {!showForm && <Button variant="light" size="sm" onClick={() => setEditing(true)}>{t.pin.change}</Button>}
      </Group>
      <Text fz="sm" c="dimmed" mt={6}>{hasPin ? t.account.pinHas : t.account.pinMissing}</Text>
      {showForm && (
        <PinForm submitLabel={hasPin ? t.pin.save : t.pin.set} disabled={a.busy || a.waiting} loading={a.loading('save')} onSave={save}
          onCancel={hasPin ? () => setEditing(false) : undefined} />
      )}
      {hasPin && !showForm && (
        <Group mt="sm" gap="sm">
          <Button variant="subtle" color="red" size="compact-sm" className="flush" loading={a.loading('remove')} disabled={(a.busy && !a.loading('remove')) || lastFactor}
            aria-describedby={lastFactor ? 'pin-last' : undefined} onClick={() => void remove()}>
            {t.pin.remove}
          </Button>
          {lastFactor && <Text id="pin-last" fz="xs" c="dimmed">{t.account.lastFactor}</Text>}
        </Group>
      )}
      <Feedback error={a.error} note={a.note} />
    </section>
  );
}

function PasskeySection({ me, reload, stepUp }: Props) {
  const t = useStrings();
  const a = useAction();
  const keys: Passkey[] = Array.isArray(me.factors.webauthn) ? me.factors.webauthn : [];
  const lastFactor = !me.factors.pin && keys.length === 1;
  const day = (s: string) => formatDate(s, t);
  const add = () =>
    a.run(async () => {
      // A restart after challenge_gone asks for a new step-up: the first token was spent at /start.
      const done = await withRestart(() => stepUp('add-factor', (token) => registerPasskey(token)));
      if (!done) return false;
      await reload();
    }, t.passkey.added, 'add');
  const remove = (id: string) =>
    a.run(async () => {
      const done = await stepUp('remove-factor', (token) =>
        api(`/api/me/passkeys/${encodeURIComponent(id)}`, { method: 'DELETE', stepUpToken: token }));
      if (!done) return false;
      await reload();
    }, t.passkey.removed, 'rm:' + id);
  return (
    <section className="panel glass">
      <Group justify="space-between" wrap="nowrap" gap="sm">
        <Group gap="xs">
          <h2>{t.account.passkeysTitle}</h2>
          {keys.length > 0 && <Badge variant="light" size="sm">{keys.length}</Badge>}
        </Group>
        <Button variant="light" size="sm" onClick={() => void add()} loading={a.loading('add')} disabled={a.waiting || (a.busy && !a.loading('add'))}>{t.passkey.add}</Button>
      </Group>
      {keys.length === 0 ? (
        <Text fz="sm" c="dimmed" mt={6}>{t.account.passkeysNone}</Text>
      ) : (
        <ul className="key-list">
          {keys.map((k) => {
            const short = k.credentialId.slice(0, 12);
            return (
              <li key={k.credentialId}>
                <span className="key-icon"><KeyIcon /></span>
                <div className="key-meta">
                  <Text fz="sm" ff={monoFont} truncate title={k.credentialId}>{short}…</Text>
                  <Text fz="xs" c="dimmed">
                    {fmt(t.account.addedOn, { date: day(k.createdAt) })} · {k.lastUsedAt ? fmt(t.account.lastUsed, { date: day(k.lastUsedAt) }) : t.account.neverUsed}
                  </Text>
                  {lastFactor && <Text fz="xs" c="dimmed">{t.account.lastFactor}</Text>}
                </div>
                <Button size="compact-sm" variant="subtle" color="red" loading={a.loading('rm:' + k.credentialId)}
                  disabled={(a.busy && !a.loading('rm:' + k.credentialId)) || lastFactor}
                  aria-label={fmt(t.account.removeLabel, { id: short })} onClick={() => void remove(k.credentialId)}>
                  {t.common.remove}
                </Button>
              </li>
            );
          })}
        </ul>
      )}
      <Feedback error={a.error} note={a.note} />
    </section>
  );
}

function AccountBody({ me, reload }: { me: Me; reload: () => Promise<void> }) {
  const t = useStrings();
  useTitle(consoleTitle(t.screens.account));
  const { stepUp, modal } = useStepUp();
  const props = { me, reload, stepUp };
  return (
    <Shell me={me}>
      <div className="page-head">
        <h1>{t.screens.account}</h1>
        <Text c="dimmed" fz="sm">{fmt(t.account.intro, { username: me.username })}</Text>
      </div>
      <div className="panel-stack">
        <PasswordSection {...props} />
        <PinSection {...props} />
        <PasskeySection {...props} />
      </div>
      {modal}
    </Shell>
  );
}

export function Account() {
  return <SessionGuard>{({ me, reload }) => <AccountBody me={me} reload={reload} />}</SessionGuard>;
}
