import { Anchor, Button, Text, TextInput } from '@mantine/core';
import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { ApiError, api } from '../lib/api';
import {
  clientApi, isDisabled, lockedByOperator, parseWhole, splitOrigins, validSubject, type ClientView,
} from '../lib/clients';
import { formatDate } from '../lib/format';
import { fmt, useStrings } from '../lib/i18n';
import { can } from '../lib/orgs';
import { clientsPath, useSuspended, useWorkspaces } from '../lib/tenants';
import { useStepUp } from '../stepup';
import { useToast } from '../toast';
import { IdChip, InlineLoading, LoadFailedInline, PageHead, ShownOnce, StatusBadge, TypeToConfirm } from '../ui';
import { ClientFields, formFrom, whole, type FormErrors, type FormState } from './clientForm';
import { Feedback, useAction } from './forms';

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

function Detail({ tenantId, id }: { tenantId: string; id: string }) {
  const t = useStrings();
  useTitle(portalTitle(t.screens.clientDetail));
  const { stepUp, modal } = useStepUp();
  const toast = useToast();
  const navigate = useNavigate();
  const { tenants } = useWorkspaces();
  const role = tenants.find((x) => x.tenantId === tenantId)?.role;
  // Disable, enable, purge and delete are for owners and admins; an unknown role shows none of them.
  const suspended = useSuspended(tenantId);
  const manage = role !== undefined && can(role, 'manage-client');
  const [client, setClient] = useState<ClientView | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [form, setForm] = useState<FormState | null>(null);
  const [errors, setErrors] = useState<FormErrors>({});
  const [secret, setSecret] = useState<Secret | null>(null);
  const [subject, setSubject] = useState('');
  const [confirmDelete, setConfirmDelete] = useState(false);
  const settings = useAction();
  const keys = useAction();
  const state = useAction();
  const purge = useAction();
  const remove = useAction();
  const path = clientApi(tenantId, id);

  /** `resetForm`: after a save, replace what is typed; a plain reload (enable, disable) keeps unsaved edits. */
  const load = useCallback((resetForm = false) => {
    setLoadError(null);
    return api<ClientView>(clientApi(tenantId, id)).then(
      (c) => {
        setClient(c);
        setForm((prev) => (resetForm || prev === null ? formFrom(c) : prev));
      },
      (e: unknown) => setLoadError(e ?? new Error('failed')),
    );
  }, [tenantId, id]);
  useEffect(() => {
    void load();
  }, [load]);

  const dialogs = (
    <>
      {modal}
      {confirmDelete && client && (
        <TypeToConfirm title={t.clientDetail.deleteConfirmTitle} body={fmt(t.clientDetail.deleteConfirmBody, { name: client.name })}
          expected={client.name} confirmLabel={t.clientDetail.delete}
          onCancel={() => setConfirmDelete(false)}
          onConfirm={() => {
            setConfirmDelete(false);
            void remove.run(async () => {
              const r = await stepUp('delete-client', (token) => api(path, { method: 'DELETE', stepUpToken: token }), id);
              if (!r) return false;
              void navigate(clientsPath(tenantId));
              return t.clientDetail.deleted;
            });
          }} />
      )}
      {secret && <ShownOnce title={secret.title} label={secret.label} value={secret.value} onClose={() => setSecret(null)} />}
    </>
  );
  if (loadError !== null) return <><LoadFailedInline error={loadError} onRetry={() => void load()} />{dialogs}</>;
  if (!client || !form) return <InlineLoading />;

  const disabled = isDisabled(client);
  const locked = lockedByOperator(client);

  const save = async (ev: FormEvent) => {
    ev.preventDefault();
    const e: FormErrors = {};
    const origins = splitOrigins(form.origins);
    if (form.name.trim() === '') e.name = t.clientForm.nameRequired;
    if (origins.length === 0) e.origins = t.clientForm.originsRequired;
    for (const k of ['pinLength', 'pinMaxAttempts', 'pinLockout', 'tokenTtl'] as const) {
      const m = whole(form[k], t, true, k);
      if (m) e[k] = m;
    }
    setErrors(e);
    if (Object.keys(e).length > 0) return;
    // Only what changed goes out. The backend asks for no step-up to edit these.
    const body: Record<string, unknown> = {};
    if (form.name.trim() !== client.name) body.name = form.name.trim();
    if (origins.join('\n') !== client.origins.join('\n')) body.origins = origins;
    if (parseWhole(form.pinLength) !== client.pinPolicy.length || parseWhole(form.pinMaxAttempts) !== client.pinPolicy.maxAttempts
      || parseWhole(form.pinLockout) !== client.pinPolicy.lockoutSec) {
      body.pinPolicy = { length: parseWhole(form.pinLength), maxAttempts: parseWhole(form.pinMaxAttempts), lockoutSec: parseWhole(form.pinLockout) };
    }
    if (parseWhole(form.tokenTtl) !== client.tokenTtlSec) body.tokenTtlSec = parseWhole(form.tokenTtl);
    if (Object.keys(body).length === 0) return toast(t.clientDetail.nothingToSave, 'info');
    await settings.run(async () => {
      await api(path, { method: 'PATCH', body });
      await load(true);
    }, t.clientDetail.saved);
  };

  return (
    <>
      <div className="crumb"><Anchor component={Link} to={clientsPath(tenantId)} fz="sm">‹ {t.clientDetail.back}</Anchor></div>
      <PageHead title={client.name}
        intro={fmt(t.clientDetail.created, { date: formatDate(client.createdAt, t) })}
        meta={<>
          <StatusBadge active={!disabled} on={t.clients.active} off={locked ? t.clients.lockedBadge : t.clients.disabled} />
          <IdChip id={client.clientId} what={t.clients.id} />
        </>} />
      {locked && (
        <div className="panel glass notice" role="status">
          <Text fz="sm" fw={600}>{t.clientDetail.locked}</Text>
          <Text fz="sm">{t.clientDetail.lockedBody}</Text>
        </div>
      )}
      <div className="panel-stack">
        <form className="panel glass" onSubmit={(ev) => void save(ev)} noValidate>
          <h2>{t.clientDetail.settingsTitle}</h2>
          <ClientFields value={form} onChange={setForm} errors={errors} disabled={settings.busy}>
            <TextInput label={t.clientForm.rpId} description={t.clientDetail.rpIdReadOnly} value={client.rpId} readOnly className="readonly-field" />
          </ClientFields>
          <div className="form-foot">
            <Button type="submit" loading={settings.busy} disabled={settings.waiting || suspended}>{t.clientDetail.save}</Button>
          </div>
          <Feedback error={settings.error} />
        </form>

        <section className="panel glass">
          <h2>{t.clientDetail.keysTitle}</h2>
          <ActionRow title={t.clientDetail.rotateTitle} body={t.clientDetail.rotateBody}>
            <Button variant="light" loading={keys.loading('rotate')} disabled={suspended || keys.waiting || (keys.busy && !keys.loading('rotate'))}
              onClick={() => void keys.run(async () => {
                const r = await stepUp('rotate-key', (token) =>
                  api<{ accessKey: string }>(`${path}/keys`, { method: 'POST', stepUpToken: token }), id);
                if (!r) return false;
                setSecret({ title: t.clientDetail.rotatedTitle, label: t.clientDetail.keyLabel, value: r.accessKey });
              }, '', 'rotate')}>
              {t.clientDetail.rotate}
            </Button>
          </ActionRow>
          <ActionRow title={t.clientDetail.revokeTitle} body={t.clientDetail.revokeBody}>
            <Button variant="light" color="red" loading={keys.loading('revoke')} disabled={suspended || keys.waiting || (keys.busy && !keys.loading('revoke'))}
              onClick={() => void keys.run(async () => {
                const r = await stepUp('revoke-previous-keys', (token) =>
                  api<{ revoked: number }>(`${path}/keys/previous`, { method: 'DELETE', stepUpToken: token }), id);
                if (!r) return false;
                const n = typeof r.revoked === 'number' ? r.revoked : 0;
                return n > 0 ? fmt(t.clientDetail.revoked, { n }) : t.clientDetail.revokedNone;
              }, '', 'revoke')}>
              {t.clientDetail.revoke}
            </Button>
          </ActionRow>
          <Feedback error={keys.error} />
        </section>

        {manage && <>
          <section className="panel glass">
            <h2>{t.clientDetail.stateTitle}</h2>
            {disabled ? (
              // An administrator's lock is not the tenant's to lift: no Enable at all, only the notice above.
              !locked && (
                <ActionRow title={t.clientDetail.enable} body={t.clientDetail.enableBody}>
                  <Button variant="light" loading={state.busy} disabled={state.waiting || suspended}
                    onClick={() => void state.run(async () => {
                      await api(`${path}/enable`, { method: 'POST' });
                      await load();
                    }, t.clientDetail.enabledDone)}>
                    {t.clientDetail.enable}
                  </Button>
                </ActionRow>
              )
            ) : (
              <ActionRow title={t.clientDetail.disable} body={t.clientDetail.disableBody}>
                <Button variant="light" color="red" loading={state.busy} disabled={state.waiting || suspended}
                  onClick={() => void state.run(async () => {
                    const r = await stepUp('disable-client', (token) => api(`${path}/disable`, { method: 'POST', stepUpToken: token }), id);
                    if (!r) return false;
                    await load();
                  }, t.clientDetail.disabledDone)}>
                  {t.clientDetail.disable}
                </Button>
              </ActionRow>
            )}
            <Feedback error={state.error} />
          </section>

          <section className="panel glass">
            <h2>{t.clientDetail.purgeTitle}</h2>
            <ActionRow title={t.clientDetail.purge} body={t.clientDetail.purgeBody}>
              <form className="purge-form" onSubmit={(ev) => ev.preventDefault()}>
                <TextInput label={t.clientDetail.subject} description={t.clientDetail.subjectHint} value={subject}
                  onChange={(e) => setSubject(e.currentTarget.value)} disabled={purge.busy} spellCheck={false} autoComplete="off" />
                <Button color="red" variant="light" type="submit" loading={purge.busy} disabled={purge.waiting || suspended || subject === ''}
                  onClick={() => void purge.run(async () => {
                    // Caught here, before a step-up is spent on an id the backend would refuse.
                    if (!validSubject(subject)) throw new ApiError(400, 'invalid_id', {});
                    // The token is bound to <clientId>/<subjectId>, exactly as the route's path names them.
                    const r = await stepUp('purge-subject', (token) =>
                      api(`${path}/subjects/${encodeURIComponent(subject)}`, { method: 'DELETE', stepUpToken: token }), `${id}/${subject}`);
                    if (!r) return false;
                    const done = fmt(t.clientDetail.purged, { id: subject });
                    setSubject('');
                    return done;
                  }, '', 'purge')}>
                  {t.clientDetail.purge}
                </Button>
              </form>
            </ActionRow>
            <Feedback error={purge.error} />
          </section>

          <section className="panel glass">
            <h2>{t.clientDetail.deleteTitle}</h2>
            <ActionRow title={t.clientDetail.delete} body={t.clientDetail.deleteBody}>
              <Button color="red" loading={remove.busy} disabled={remove.waiting || suspended} onClick={() => setConfirmDelete(true)}>
                {t.clientDetail.delete}
              </Button>
            </ActionRow>
            <Feedback error={remove.error} />
          </section>
        </>}
      </div>
      {dialogs}
    </>
  );
}

export function ClientDetail() {
  const { tenantId = '', id = '' } = useParams();
  return <Detail key={`${tenantId}/${id}`} tenantId={tenantId} id={id} />;
}
