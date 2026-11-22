import { Badge, Button, Group, SegmentedControl, Text, TextInput, useMantineColorScheme } from '@mantine/core';
import { monoFont } from '@authgate/web-kit/theme';
import { useRef, useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { ApiError, api } from '../lib/api';
import { checkAvatar, uploadAvatar } from '../lib/avatar';
import { formatDate } from '../lib/format';
import { fmt, useLang, useStrings, type Lang } from '../lib/i18n';
import { Avatar } from '../shell';
import { passkeyCount, useFullMe, useMe, type Me, type Passkey } from '../session';
import { useStepUp } from '../stepup';
import { PageHead, TypeToConfirm } from '../ui';
import { Feedback, PasswordForm, PinForm, useAction } from './forms';
import { registerPasskey } from './factors';
import { RecoveryCodes } from './RecoveryCodes';

type StepUp = ReturnType<typeof useStepUp>['stepUp'];
type Props = { me: Me; reload: () => Promise<void>; stepUp: StepUp };

function KeyIcon() {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="8" cy="15" r="4" />
      <path d="m11 12 8-8m-3 3 3 3" />
    </svg>
  );
}

function ProfileSection({ me, reload }: Pick<Props, 'me' | 'reload'>) {
  const t = useStrings();
  const name = useAction();
  const pic = useAction();
  const [draft, setDraft] = useState(me.displayName);
  const file = useRef<HTMLInputElement>(null);

  const saveName = (ev: FormEvent) => {
    ev.preventDefault();
    const next = draft.trim();
    if (next === '' || next === me.displayName) return;
    void name.run(async () => {
      await api('/api/me/display-name', { body: { displayName: next } });
      await reload();
    }, t.account.displayNameSaved);
  };
  const choose = (f: File | undefined) => {
    if (file.current) file.current.value = ''; // the same file may be chosen again after a refusal
    if (!f) return;
    // Checked here, before a presign is asked for: a refused file never leaves the browser.
    const problem = checkAvatar(f);
    if (problem) return pic.setError(problem === 'size' ? t.account.avatarTooBig : t.account.avatarWrongType);
    void pic.run(async () => {
      try {
        await uploadAvatar(f);
      } catch (e) {
        if (e instanceof ApiError && e.code === 'upload_failed') {
          pic.setError(t.account.avatarUploadFailed);
          return false;
        }
        throw e;
      }
      await reload();
    }, t.account.avatarSaved, 'upload');
  };
  const removePic = () =>
    pic.run(async () => {
      await api('/api/me/avatar', { method: 'DELETE' });
      await reload();
    }, t.account.avatarRemoved, 'remove');

  return (
    <section className="panel glass">
      <h2>{t.account.profileTitle}</h2>
      <div className="avatar-row">
        <Avatar me={me} large />
        <div>
          <Text fz="sm" fw={600}>{t.account.avatarTitle}</Text>
          <Text fz="xs" c="dimmed">{t.account.avatarHint}</Text>
          <div className="avatar-actions">
            <input ref={file} type="file" className="sr-only" tabIndex={-1} accept="image/png,image/jpeg,image/webp" aria-label={t.account.avatarChoose}
              onChange={(e) => choose(e.currentTarget.files?.[0])} />
            <Button size="compact-sm" variant="light" loading={pic.loading('upload')} disabled={pic.busy && !pic.loading('upload')}
              onClick={() => file.current?.click()}>
              {t.account.avatarChoose}
            </Button>
            {me.avatarUrl && (
              <Button size="compact-sm" variant="subtle" color="red" loading={pic.loading('remove')} disabled={pic.busy && !pic.loading('remove')}
                onClick={() => void removePic()}>
                {t.account.avatarRemove}
              </Button>
            )}
          </div>
        </div>
      </div>
      <Feedback error={pic.error} />
      <form className="panel-form" onSubmit={saveName}>
        <Group align="flex-end" gap="sm">
          <TextInput className="toolbar-grow" label={t.account.displayName} value={draft} onChange={(e) => setDraft(e.currentTarget.value)}
            maxLength={64} required />
          <Button type="submit" loading={name.busy} disabled={draft.trim() === '' || draft.trim() === me.displayName}>{t.account.displayNameSave}</Button>
        </Group>
        <Feedback error={name.error} />
      </form>
    </section>
  );
}

function PreferencesSection() {
  const t = useStrings();
  const [lang, setLang] = useLang();
  // keepTransitions: Mantine would otherwise append a <style> element on every change, which the CSP blocks.
  const { colorScheme, setColorScheme } = useMantineColorScheme({ keepTransitions: true });
  return (
    <section className="panel glass">
      <h2>{t.account.preferencesTitle}</h2>
      <div className="pref-row">
        <Text fz="sm" id="pref-lang">{t.account.language}</Text>
        <SegmentedControl size="sm" radius="md" value={lang} onChange={(v) => setLang(v as Lang)} aria-labelledby="pref-lang"
          data={[{ label: 'English', value: 'en' }, { label: 'Tiếng Việt', value: 'vi' }]} />
      </div>
      <div className="pref-row">
        <Text fz="sm" id="pref-scheme">{t.account.appearance}</Text>
        <SegmentedControl size="sm" radius="md" value={colorScheme} onChange={(v) => setColorScheme(v as 'light' | 'dark' | 'auto')}
          aria-labelledby="pref-scheme"
          data={[{ label: t.account.light, value: 'light' }, { label: t.account.dark, value: 'dark' }, { label: t.account.system, value: 'auto' }]} />
      </div>
    </section>
  );
}

function EmailSection({ me, stepUp }: Pick<Props, 'me' | 'stepUp'>) {
  const t = useStrings();
  const a = useAction();
  const [next, setNext] = useState('');
  const submit = (ev: FormEvent) => {
    ev.preventDefault();
    void a.run(async () => {
      const done = await stepUp('change-email', (token) => api('/api/me/email', { body: { newEmail: next.trim() }, stepUpToken: token }));
      if (!done) return false;
      setNext('');
    }, t.account.emailSent);
  };
  return (
    <section className="panel glass">
      <h2>{t.account.emailTitle}</h2>
      <Text fz="sm" c="dimmed" mt={6}>{fmt(t.account.emailNote, { email: me.email })}</Text>
      <form className="panel-form" onSubmit={submit}>
        <Group align="flex-end" gap="sm">
          <TextInput className="toolbar-grow" type="email" label={t.account.newEmail} value={next} onChange={(e) => setNext(e.currentTarget.value)}
            autoComplete="email" required />
          <Button type="submit" loading={a.busy} disabled={next.trim() === '' || a.waiting}>{t.account.emailSend}</Button>
        </Group>
        <Feedback error={a.error} />
      </form>
    </section>
  );
}

function PasswordSection({ reload, stepUp }: Pick<Props, 'reload' | 'stepUp'>) {
  const t = useStrings();
  const a = useAction();
  return (
    <section className="panel glass">
      <h2>{t.account.passwordTitle}</h2>
      <Text fz="sm" c="dimmed" mt={6}>{t.account.passwordNote}</Text>
      <PasswordForm disabled={a.busy || a.waiting} loading={a.loading('pwd')}
        onSave={(current, next) =>
          a.run(async () => {
            const done = await stepUp('change-password', (token) =>
              api('/api/me/password', { body: { currentPassword: current, newPassword: next }, stepUpToken: token }));
            if (!done) return false; // modal closed: not an error, the form keeps its input
            await reload();
          }, t.password.changed, 'pwd')} />
      <Feedback error={a.error} />
    </section>
  );
}

function PinSection({ me, reload, stepUp }: Props) {
  const t = useStrings();
  const a = useAction();
  const [editing, setEditing] = useState(false);
  const hasPin = me.factors.pin;
  const lastFactor = passkeyCount(me.factors) === 0;
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
          <Button variant="subtle" color="red" size="compact-sm" className="flush" loading={a.loading('remove')}
            disabled={(a.busy && !a.loading('remove')) || lastFactor} aria-describedby={lastFactor ? 'pin-last' : undefined}
            onClick={() => void remove()}>
            {t.pin.remove}
          </Button>
          {lastFactor && <Text id="pin-last" fz="xs" c="dimmed">{t.account.lastFactor}</Text>}
        </Group>
      )}
      <Feedback error={a.error} />
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
      const done = await stepUp('add-factor', (token) => registerPasskey(token));
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
        <Button variant="light" size="sm" onClick={() => void add()} loading={a.loading('add')} disabled={a.waiting || (a.busy && !a.loading('add'))}>
          {t.passkey.add}
        </Button>
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
      <Feedback error={a.error} />
    </section>
  );
}

/** How many codes are left, and the action that replaces them; with none left it is a warning. */
function RecoverySection({ me, stepUp, onCodes }: Pick<Props, 'me' | 'stepUp'> & { onCodes: (codes: string[]) => void }) {
  const t = useStrings();
  const a = useAction();
  const none = me.recoveryCodesLeft === 0;
  return (
    <section className={none ? 'panel glass notice' : 'panel glass'}>
      <h2>{t.account.recoveryTitle}</h2>
      <Text fz="sm" mt={6} role={none ? 'alert' : undefined}>
        {none ? t.account.recoveryNone : fmt(t.account.recoveryLeft, { n: me.recoveryCodesLeft })}
      </Text>
      <Text fz="sm" c="dimmed" mt={6}>{t.account.recoveryBody}</Text>
      <Group mt="md">
        <Button variant={none ? 'filled' : 'light'} loading={a.busy}
          onClick={() => void a.run(async () => {
            const r = await stepUp('regenerate-recovery-codes', (token) =>
              api<{ recoveryCodes: string[] }>('/api/me/recovery-codes', { method: 'POST', stepUpToken: token }));
            if (!r) return false;
            onCodes(r.recoveryCodes);
            return '';
          })}>
          {t.account.recoveryRegenerate}
        </Button>
      </Group>
      <Feedback error={a.error} />
    </section>
  );
}

function DangerSection({ me, stepUp }: Pick<Props, 'me' | 'stepUp'>) {
  const t = useStrings();
  const a = useAction();
  const nav = useNavigate();
  const { clear } = useMe();
  const [confirm, setConfirm] = useState(false);
  return (
    <section className="panel glass">
      <h2>{t.account.dangerTitle}</h2>
      <Text fz="sm" c="dimmed" mt={6}>{t.account.dangerBody}</Text>
      <Group mt="md">
        <Button color="red" loading={a.busy} disabled={a.waiting} onClick={() => setConfirm(true)}>{t.account.deleteButton}</Button>
      </Group>
      <Feedback error={a.error} />
      {confirm && (
        <TypeToConfirm title={t.account.deleteTitle} body={fmt(t.account.deleteConfirm, { email: me.email })} expected={me.email}
          confirmLabel={t.account.deleteButton} onCancel={() => setConfirm(false)}
          onConfirm={() => {
            setConfirm(false);
            void a.run(async () => {
              const done = await stepUp('delete-account', (token) => api('/api/me', { method: 'DELETE', stepUpToken: token }));
              if (!done) return false;
              // The session is gone with the account. clear() drops the toasts, so the note comes after it.
              clear();
              nav('/sign-in', { replace: true });
              return t.account.deleted;
            });
          }} />
      )}
    </section>
  );
}

export function Account() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.account));
  const { me, reload } = useFullMe();
  const { stepUp, modal } = useStepUp();
  const [codes, setCodes] = useState<string[] | null>(null);
  const props = { me, reload, stepUp };

  // The new codes take the whole screen until they are saved, like the first set after enrolment.
  if (codes) return <RecoveryCodes codes={codes} onContinue={() => void reload().catch(() => {}).finally(() => setCodes(null))} />;
  return (
    <>
      <PageHead title={t.screens.account} intro={fmt(t.account.intro, { email: me.email })} />
      <div className="panel-stack">
        <ProfileSection me={me} reload={reload} />
        <PreferencesSection />
        <EmailSection me={me} stepUp={stepUp} />
        <PasswordSection {...props} />
        <PinSection {...props} />
        <PasskeySection {...props} />
        <RecoverySection me={me} stepUp={stepUp} onCodes={setCodes} />
        <DangerSection me={me} stepUp={stepUp} />
      </div>
      {modal}
    </>
  );
}
