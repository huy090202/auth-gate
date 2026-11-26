import { Button, Text, TextInput } from '@mantine/core';
import { useState, type FormEvent, type ReactNode } from 'react';
import { Navigate, useNavigate, useParams } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { ApiError, api } from '../lib/api';
import { fmt, useStrings } from '../lib/i18n';
import { can, orgApi } from '../lib/orgs';
import { clientsPath, useSuspended, useWorkspaces } from '../lib/tenants';
import { useStepUp } from '../stepup';
import { ConfirmDialog, InlineLoading, LoadFailedInline, PageHead, TypeToConfirm } from '../ui';
import { Feedback, useAction } from './forms';

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

export function OrgSettings() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.orgSettings));
  const { tenantId = '' } = useParams();
  const navigate = useNavigate();
  const { tenants, reload, loading } = useWorkspaces();
  const suspended = useSuspended(tenantId);
  const { stepUp, modal } = useStepUp();
  const ws = tenants.find((x) => x.tenantId === tenantId);
  const [name, setName] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<'leave' | 'delete' | null>(null);
  const rename = useAction();
  const leave = useAction();
  const remove = useAction();

  if (ws?.kind === 'personal') return <Navigate to={clientsPath(tenantId)} replace />;
  if (!ws && loading) return <InlineLoading />;
  if (!ws) return <LoadFailedInline error={new ApiError(404, 'not_found', {})} onRetry={() => void reload()} />;
  const personal = tenants.find((x) => x.kind === 'personal')?.tenantId ?? '';
  const owner = can(ws.role, 'rename');
  const typed = name ?? ws.name;

  const save = (ev: FormEvent) => {
    ev.preventDefault();
    void rename.run(async () => {
      try {
        await api(orgApi.tenant(tenantId), { method: 'PATCH', body: { name: typed.trim() } });
      } catch (e) {
        // The generic invalid_name text is about display names.
        if (!(e instanceof ApiError && e.code === 'invalid_name')) throw e;
        rename.setError(t.newOrg.invalidName);
        return false;
      }
      await reload();
      setName(null);
    }, t.orgSettings.renamed);
  };
  // After either, the organisation is no longer the user's: the list is reloaded, then the personal workspace opens.
  const done = async () => {
    await reload();
    void navigate(clientsPath(personal));
  };

  return (
    <>
      <PageHead title={t.screens.orgSettings} intro={ws.name} />
      <div className="panel-stack">
        {owner && (
          <form className="panel glass" onSubmit={save} noValidate>
            <h2>{t.orgSettings.nameTitle}</h2>
            <TextInput label={t.orgSettings.nameLabel} value={typed} maxLength={64} autoComplete="off" disabled={rename.busy}
              onChange={(e) => setName(e.currentTarget.value)} />
            <div className="form-foot">
              <Button type="submit" loading={rename.busy} disabled={rename.waiting || suspended || typed.trim() === '' || typed.trim() === ws.name}>
                {t.orgSettings.save}
              </Button>
            </div>
            <Feedback error={rename.error} />
          </form>
        )}
        <section className="panel glass">
          <h2>{t.orgSettings.leaveTitle}</h2>
          <ActionRow title={t.orgSettings.leave} body={t.orgSettings.leaveBody}>
            <Button variant="light" color="red" loading={leave.busy} disabled={leave.waiting} onClick={() => setConfirm('leave')}>
              {t.orgSettings.leave}
            </Button>
          </ActionRow>
          <Feedback error={leave.error} />
        </section>
        {can(ws.role, 'delete') && (
          <section className="panel glass">
            <h2>{t.orgSettings.dangerTitle}</h2>
            <ActionRow title={t.orgSettings.delete} body={t.orgSettings.deleteBody}>
              <Button color="red" loading={remove.busy} disabled={remove.waiting || suspended} onClick={() => setConfirm('delete')}>
                {t.orgSettings.delete}
              </Button>
            </ActionRow>
            <Feedback error={remove.error} />
          </section>
        )}
      </div>
      {modal}
      {confirm === 'leave' && (
        <ConfirmDialog danger title={fmt(t.orgSettings.leaveConfirmTitle, { name: ws.name })} body={t.orgSettings.leaveConfirmBody}
          confirmLabel={t.orgSettings.leave} onCancel={() => setConfirm(null)}
          onConfirm={() => {
            setConfirm(null);
            void leave.run(async () => {
              await api(orgApi.leave(tenantId), { method: 'POST' });
              await done();
            }, fmt(t.orgSettings.left, { name: ws.name }));
          }} />
      )}
      {confirm === 'delete' && (
        <TypeToConfirm title={t.orgSettings.deleteConfirmTitle} body={fmt(t.orgSettings.deleteConfirmBody, { name: ws.name })}
          expected={ws.name} confirmLabel={t.orgSettings.delete} onCancel={() => setConfirm(null)}
          onConfirm={() => {
            setConfirm(null);
            void remove.run(async () => {
              const r = await stepUp('delete-org', (token) =>
                api(orgApi.tenant(tenantId), { method: 'DELETE', body: { name: ws.name }, stepUpToken: token }), tenantId);
              if (!r) return false;
              await done();
            }, t.orgSettings.deleted);
          }} />
      )}
    </>
  );
}
