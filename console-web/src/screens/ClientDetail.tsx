import { Anchor, Button, Text, TextInput } from '@mantine/core';
import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { Link, useParams } from 'react-router';
import { consoleTitle, useTitle } from '../brand';
import { ApiError, api } from '../lib/api';
import { parsePositive, parseWhole, splitOrigins, validSubject, type ClientView } from '../lib/clients';
import { formatDate } from '../lib/format';
import { fmt, useStrings } from '../lib/i18n';
import { SessionGuard, type Me } from '../session';
import { Shell } from '../shell';
import { ShownOnce } from '../ShownOnce';
import { useStepUp } from '../stepup';
import { IdChip, InlineLoading, LoadFailedInline, PageHead, StatusBadge } from '../ui';
import { ClientFields, formFrom, positive, whole, type FormErrors, type FormState } from './clientForm';
import { Feedback, useAction } from './factors';

type Secret = { title: string; label: string; value: string };

function ActionRow({ title, body, children }: { title: string; body: string; children: ReactNode }) {
  return (
    <div className="action-row">
      <div className="action-text">
        <h3>{title}</h3>
        <Text fz="sm" c="dimmed">{body}</Text>
      </div>
      <div className="action-ctl">{children}</div>
    </div>
  );
}

function Detail({ me, id }: { me: Me; id: string }) {
  const t = useStrings();
  useTitle(consoleTitle(t.screens.clientDetail));
  const { stepUp, modal } = useStepUp();
  const [client, setClient] = useState<ClientView | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [form, setForm] = useState<FormState | null>(null);
  const [errors, setErrors] = useState<FormErrors>({});
  const [secret, setSecret] = useState<Secret | null>(null);
  // The SPA is not told which client is the Console's own (neither /me nor the client carries it),
  // so it learns from the first 409 own_client_protected and then stays read-only.
  const [own, setOwn] = useState(false);
  const [subject, setSubject] = useState('');
  const settings = useAction();
  const keys = useAction();
  const state = useAction();
  const purge = useAction();

  const load = useCallback(() => {
    setLoadError(null);
    return api<ClientView>(`/api/clients/${encodeURIComponent(id)}`).then(
      (c) => {
        setClient(c);
        setForm(formFrom(c));
      },
      (e: unknown) => setLoadError(e ?? new Error('failed')),
    );
  }, [id]);
  useEffect(() => {
    void load();
  }, [load]);

  /** Marks the Console's own client when a write is refused for it. */
  const guard = async <T,>(fn: () => Promise<T>): Promise<T> => {
    try {
      return await fn();
    } catch (e) {
      if (e instanceof ApiError && e.code === 'own_client_protected') setOwn(true);
      throw e;
    }
  };

  if (loadError !== null) return <Shell me={me}><LoadFailedInline error={loadError} onRetry={() => void load()} /></Shell>;
  if (!client || !form) return <Shell me={me}><InlineLoading /></Shell>;

  const path = `/api/clients/${encodeURIComponent(id)}`;
  const disabled = client.disabledAt !== undefined && client.disabledAt !== '';

  const save = async (ev: FormEvent) => {
    ev.preventDefault();
    const e: FormErrors = {};
    const origins = splitOrigins(form.origins);
    if (form.name.trim() === '') e.name = t.clientForm.nameRequired;
    if (origins.length === 0) e.origins = t.clientForm.originsRequired;
    for (const k of ['pinLength', 'pinMaxAttempts', 'pinLockout', 'tokenTtl', 'rateBurst'] as const) {
      const m = whole(form[k], t, true);
      if (m) e[k] = m;
    }
    const pm = positive(form.ratePerSecond, t);
    if (pm) e.ratePerSecond = pm;
    setErrors(e);
    if (Object.keys(e).length > 0) return;
    // Only what changed goes out, so a name-only edit stays a name-only edit (no step-up).
    const body: Record<string, unknown> = {};
    const orig = formFrom(client);
    if (form.name.trim() !== client.name) body.name = form.name.trim();
    if (origins.join('\n') !== client.origins.join('\n')) body.origins = origins;
    if (form.pinLength !== orig.pinLength || form.pinMaxAttempts !== orig.pinMaxAttempts || form.pinLockout !== orig.pinLockout) {
      body.pinPolicy = { length: parseWhole(form.pinLength), maxAttempts: parseWhole(form.pinMaxAttempts), lockoutSec: parseWhole(form.pinLockout) };
    }
    if (parseWhole(form.tokenTtl) !== client.tokenTtlSec) body.tokenTtlSec = parseWhole(form.tokenTtl);
    if (parseWhole(form.rateBurst) !== client.rateLimit.burst || parsePositive(form.ratePerSecond) !== client.rateLimit.perSecond) {
      body.rateLimit = { burst: parseWhole(form.rateBurst), perSecond: parsePositive(form.ratePerSecond) };
    }
    const names = Object.keys(body);
    if (names.length === 0) return void settings.run(async () => t.clientDetail.nothingToSave);
    await settings.run(async () => {
      if (names.length === 1 && names[0] === 'name') {
        await guard(() => api(path, { method: 'PATCH', body }));
      } else {
        // Anything beyond the name is a step-up, bound to this client's id.
        const done = await guard(() => stepUp('update-client', (token) => api(path, { method: 'PATCH', body, stepUpToken: token }), id));
        if (!done) return false;
      }
      await load();
    }, t.clientDetail.saved);
  };

  return (
    <Shell me={me}>
      <div className="crumb"><Anchor component={Link} to="/clients" fz="sm">‹ {t.clientDetail.back}</Anchor></div>
      <PageHead title={client.name}
        intro={fmt(t.clientDetail.created, { date: formatDate(client.createdAt, t) })}
        meta={<><StatusBadge active={!disabled} on={t.clients.active} off={t.clientDetail.disabledBadge} /><IdChip id={client.clientId} what={t.clients.id} /></>} />
      {own && <div className="panel glass notice" role="status"><Text fz="sm">{t.clientDetail.ownClient}</Text></div>}
      <div className="panel-stack">
        <form className="panel glass" onSubmit={(ev) => void save(ev)} noValidate>
          <h2>{t.clientDetail.settingsTitle}</h2>
          <Text fz="sm" c="dimmed" mt={6} mb="md">{t.clientDetail.stepNote}</Text>
          <ClientFields value={form} onChange={setForm} errors={errors} withRate disabled={own || settings.busy}>
            <TextInput label={t.clientForm.rpId} description={t.clientDetail.rpIdReadOnly} value={client.rpId} readOnly className="readonly-field" />
          </ClientFields>
          <div className="form-foot">
            <Button type="submit" loading={settings.busy} disabled={own || settings.waiting}>{t.clientDetail.save}</Button>
          </div>
          <Feedback error={settings.error} note={settings.note} />
        </form>

        <section className="panel glass">
          <h2>{t.clientDetail.keysTitle}</h2>
          <ActionRow title={t.clientDetail.rotateTitle} body={t.clientDetail.rotateBody}>
            <Button variant="light" loading={keys.loading('rotate')} disabled={own || keys.waiting || (keys.busy && !keys.loading('rotate'))}
              onClick={() => void keys.run(async () => {
                const r = await guard(() => stepUp('rotate-key', (token) =>
                  api<{ accessKey: string }>(`${path}/keys`, { method: 'POST', stepUpToken: token }), id));
                if (!r) return false;
                setSecret({ title: t.clientDetail.rotatedTitle, label: t.clientDetail.keyLabel, value: r.accessKey });
              }, '', 'rotate')}>
              {t.clientDetail.rotate}
            </Button>
          </ActionRow>
          <ActionRow title={t.clientDetail.revokeTitle} body={t.clientDetail.revokeBody}>
            <Button variant="light" color="red" loading={keys.loading('revoke')} disabled={own || keys.waiting || (keys.busy && !keys.loading('revoke'))}
              onClick={() => void keys.run(async () => {
                const r = await guard(() => stepUp('revoke-previous-keys', (token) =>
                  api<{ revoked: number }>(`${path}/keys/previous`, { method: 'DELETE', stepUpToken: token }), id));
                if (!r) return false;
                const n = typeof r.revoked === 'number' ? r.revoked : 0;
                return n > 0 ? fmt(t.clientDetail.revoked, { n }) : t.clientDetail.revokedNone;
              }, '', 'revoke')}>
              {t.clientDetail.revoke}
            </Button>
          </ActionRow>
          <Feedback error={keys.error} note={keys.note} />
        </section>

        <section className="panel glass">
          <h2>{t.clientDetail.stateTitle}</h2>
          {disabled ? (
            <ActionRow title={t.clientDetail.enable} body={t.clientDetail.enableBody}>
              <Button variant="light" loading={state.busy} disabled={own || state.waiting}
                onClick={() => void state.run(async () => {
                  await guard(() => api(`${path}/enable`, { method: 'POST' }));
                  await load();
                }, t.clientDetail.enabledDone)}>
                {t.clientDetail.enable}
              </Button>
            </ActionRow>
          ) : (
            <ActionRow title={t.clientDetail.disable} body={t.clientDetail.disableBody}>
              <Button variant="light" color="red" loading={state.busy} disabled={own || state.waiting}
                onClick={() => void state.run(async () => {
                  const r = await guard(() => stepUp('disable-client', (token) => api(`${path}/disable`, { method: 'POST', stepUpToken: token }), id));
                  if (!r) return false;
                  await load();
                }, t.clientDetail.disabledDone)}>
                {t.clientDetail.disable}
              </Button>
            </ActionRow>
          )}
          <Feedback error={state.error} note={state.note} />
        </section>

        <section className="panel glass">
          <h2>{t.clientDetail.purgeTitle}</h2>
          <ActionRow title={t.clientDetail.purge} body={t.clientDetail.purgeBody}>
            <form className="purge-form" onSubmit={(ev) => ev.preventDefault()}>
              <TextInput label={t.clientDetail.subject} description={t.clientDetail.subjectHint} value={subject}
                onChange={(e) => setSubject(e.currentTarget.value)} disabled={own || purge.busy} spellCheck={false} autoComplete="off" />
              <Button color="red" variant="light" type="submit" loading={purge.busy}
                disabled={own || purge.waiting || subject === ''}
                onClick={() => void purge.run(async () => {
                  const bad = !validSubject(subject);
                  if (bad) throw new ApiError(400, 'invalid_id', {});
                  // The token is bound to <clientId>/<subjectId>, exactly as the route's path names them.
                  const r = await guard(() => stepUp('purge-subject', (token) =>
                    api(`${path}/subjects/${encodeURIComponent(subject)}`, { method: 'DELETE', stepUpToken: token }), `${id}/${subject}`));
                  if (!r) return false;
                  const done = fmt(t.clientDetail.purged, { id: subject });
                  setSubject('');
                  return done;
                }, '', 'purge')}>
                {t.clientDetail.purge}
              </Button>
            </form>
          </ActionRow>
          <Feedback error={purge.error} note={purge.note} />
        </section>
      </div>
      {modal}
      {secret && <ShownOnce title={secret.title} label={secret.label} value={secret.value} onClose={() => setSecret(null)} />}
    </Shell>
  );
}

export function ClientDetail() {
  const { id = '' } = useParams();
  return <SessionGuard>{({ me }) => <Detail key={id} me={me} id={id} />}</SessionGuard>;
}
