import { Anchor, Badge, Button, Group, Table, Text, TextInput } from '@mantine/core';
import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { Link, useParams } from 'react-router';
import { consoleTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { detailKeys, failureLabel, failureOf, labelOf } from '../lib/audit';
import { parseWhole } from '../lib/clients';
import { explain } from '../lib/explain';
import { formatDate } from '../lib/format';
import { fmt, useStrings } from '../lib/i18n';
import {
  currentSuspension, missingStep, tenantName, tenantStatus, type TenantAuditEntry, type TenantView,
} from '../lib/operator';
import { SessionGuard, type Me } from '../session';
import { Shell } from '../shell';
import { useStepUp } from '../stepup';
import type { Strings } from '../strings';
import { IdChip, InlineLoading, LoadFailedInline, PageHead, StatusBadge } from '../ui';
import { Feedback, useAction } from './factors';
import { ReasonDialog } from './ReasonDialog';
import { TenantStatusBadge } from './Tenants';

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

function AuditPanel({ tid, version, t }: { tid: string; version: number; t: Strings }) {
  const [entries, setEntries] = useState<TenantAuditEntry[] | null>(null);
  const [next, setNext] = useState('');
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const seq = useRef(0);

  const fetchPage = useCallback(async (cursor: string) => {
    const mine = ++seq.current;
    setLoading(true);
    try {
      const r = await api<{ entries?: TenantAuditEntry[]; nextCursor?: string }>(
        `/api/tenants/${encodeURIComponent(tid)}/audit${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`,
      );
      if (mine !== seq.current) return;
      setEntries((prev) => (cursor ? [...(prev ?? []), ...(r.entries ?? [])] : (r.entries ?? [])));
      setNext(r.nextCursor ?? '');
      setError(null);
    } catch (e) {
      if (mine === seq.current) setError(e ?? new Error('failed'));
    } finally {
      if (mine === seq.current) setLoading(false);
    }
  }, [tid]);
  useEffect(() => {
    void fetchPage('');
  }, [fetchPage, version]);

  return (
    <section className="panel glass">
      <h2>{t.tenantPage.auditTitle}</h2>
      {error !== null && entries === null ? (
        <div className="inset-error"><LoadFailedInline error={error} onRetry={() => void fetchPage('')} /></div>
      ) : entries === null ? (
        <InlineLoading />
      ) : entries.length === 0 ? (
        <Text c="dimmed" mt="sm">{t.tenantPage.auditEmpty}</Text>
      ) : (
        <div className="table-wrap">
          <Table className="data-table">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t.audit.colTime}</Table.Th>
                <Table.Th>{t.audit.colActor}</Table.Th>
                <Table.Th>{t.audit.colAction}</Table.Th>
                <Table.Th>{t.tenantPage.auditColDetail}</Table.Th>
                <Table.Th>{t.audit.colResult}</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {entries.map((e, i) => {
                const code = failureOf(e.result);
                const keys = detailKeys(e.detail);
                return (
                  <Table.Tr key={i}>
                    <Table.Td className="cell-muted cell-nowrap cell-title" data-label={t.audit.colTime} title={e.at}>{formatDate(e.at, t)}</Table.Td>
                    <Table.Td data-label={t.audit.colActor}>
                      {e.actorId === 'operator' ? t.tenantPage.operatorActor
                        : e.actorName ? e.actorName : <span className="cell-muted">{t.audit.unknownActor}</span>}
                    </Table.Td>
                    <Table.Td className="cell-strong" data-label={t.audit.colAction}>{labelOf(t.tenantPage.auditActions, e.action)}</Table.Td>
                    <Table.Td data-label={t.tenantPage.auditColDetail}>
                      {keys.length === 0 ? <span className="cell-muted">—</span>
                        : <span className="chips">{keys.map((k) => <span key={k} className="chip">{labelOf(t.audit.detailKeys, k)}: {e.detail![k]}</span>)}</span>}
                    </Table.Td>
                    <Table.Td data-label={t.audit.colResult}>
                      {code === null ? <Badge variant="light" color="canopy">{t.audit.success}</Badge>
                        : <Badge variant="light" color="red" title={e.result}>{code === '' ? (e.result || '?') : failureLabel(code, t)}</Badge>}
                    </Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        </div>
      )}
      {error !== null && entries !== null && <Text c="red.7" fz="sm" mt="md" role="alert">{explain(error, t)}</Text>}
      {next !== '' && (
        <div className="more-row">
          <Button variant="light" loading={loading} onClick={() => void fetchPage(next)}>{t.audit.loadMore}</Button>
        </div>
      )}
    </section>
  );
}

function Detail({ me, id }: { me: Me; id: string }) {
  const t = useStrings();
  useTitle(consoleTitle(t.screens.tenantDetail));
  const { stepUp, modal } = useStepUp();
  const [view, setView] = useState<TenantView | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [version, setVersion] = useState(0);
  const [reasonOpen, setReasonOpen] = useState(false);
  const [editing, setEditing] = useState(false);
  const [quota, setQuota] = useState('');
  const [quotaError, setQuotaError] = useState('');
  const state = useAction();
  const quotaAct = useAction();
  const path = `/api/tenants/${encodeURIComponent(id)}`;

  const load = useCallback(() => {
    setLoadError(null);
    return api<TenantView>(`/api/tenants/${encodeURIComponent(id)}`).then(
      (v) => {
        setView(v);
        setVersion((n) => n + 1);
      },
      (e: unknown) => setLoadError(e ?? new Error('failed')),
    );
  }, [id]);
  useEffect(() => {
    void load();
  }, [load]);

  if (loadError !== null && view === null) return <Shell me={me}><LoadFailedInline error={loadError} onRetry={() => void load()} /></Shell>;
  if (!view) return <Shell me={me}><InlineLoading /></Shell>;

  const { tenant, owner } = view;
  const name = tenantName(tenant, t.tenants.personalName);
  const status = tenantStatus(tenant);
  const deleting = status === 'deleting';
  const agSuspended = !!owner.suspendedAt;
  const portalSuspended = !!tenant.suspendedAt;
  const step = deleting ? null : missingStep(agSuspended, portalSuspended);
  const suspended = agSuspended || portalSuspended;
  const last = currentSuspension(view.lastSuspension, owner.suspendedAt ?? tenant.suspendedAt);
  const word = (b: boolean) => (b ? t.tenantPage.stateSuspended : t.tenantPage.stateActive);

  /** A half-done action still changed AuthGate, so the page is read again whether or not the call succeeded. */
  const refreshing = async <T,>(fn: () => Promise<T>): Promise<T> => {
    try {
      return await fn();
    } catch (e) {
      await load();
      throw e;
    }
  };
  const suspend = (reason: string) => {
    setReasonOpen(false);
    void state.run(async () => {
      const r = await refreshing(() => stepUp('tenant-suspend', (token) =>
        api(`${path}/suspend`, { body: { reason }, stepUpToken: token }), id));
      if (!r) return false;
      await load();
    }, t.tenantPage.suspendDone, 'suspend');
  };
  const unsuspend = () =>
    state.run(async () => {
      const r = await refreshing(() => stepUp('tenant-unsuspend', (token) => api(`${path}/unsuspend`, { method: 'POST', stepUpToken: token }), id));
      if (!r) return false;
      await load();
    }, t.tenantPage.unsuspendDone, 'unsuspend');
  const saveQuota = (ev: FormEvent) => {
    ev.preventDefault();
    const n = parseWhole(quota);
    if (n === null) return setQuotaError(t.tenantPage.quotaInvalid);
    setQuotaError('');
    void quotaAct.run(async () => {
      const r = await refreshing(() => stepUp('tenant-quota', (token) =>
        api(`${path}/quota`, { method: 'PATCH', body: { clientQuota: n }, stepUpToken: token }), id));
      if (!r) return false;
      setEditing(false);
      await load();
    }, t.tenantPage.quotaSaved);
  };

  const clients = view.clients.clients ?? [];
  return (
    <Shell me={me}>
      <div className="crumb"><Anchor component={Link} to="/tenants" fz="sm">‹ {t.tenantPage.back}</Anchor></div>
      <PageHead title={name} intro={fmt(t.tenantPage.created, { date: formatDate(tenant.createdAt, t) })}
        meta={<>
          {deleting
            ? <TenantStatusBadge row={tenant} t={t} />
            : <>
              <Badge variant="light" size="md" color={agSuspended ? 'orange' : 'canopy'}>{fmt(t.tenantPage.badgeAuthGate, { state: word(agSuspended) })}</Badge>
              <Badge variant="light" size="md" color={portalSuspended ? 'orange' : 'canopy'}>{fmt(t.tenantPage.badgePortal, { state: word(portalSuspended) })}</Badge>
            </>}
          <IdChip id={tenant.tenantId} what={t.tenants.id} />
        </>} />
      {deleting && <div className="panel glass notice" role="status"><Text fz="sm">{t.tenantPage.deletingNote}</Text></div>}
      {step && (
        <div className="panel glass notice" role="status">
          <Text fw={650} fz="sm">{t.tenantPage.syncTitle}</Text>
          <Text fz="sm" mt={4}>{fmt(t.tenantPage.syncBody, { authgate: word(agSuspended), portal: word(portalSuspended) })}</Text>
          <Button mt="sm" variant="light" color="orange" loading={state.busy} disabled={state.waiting}
            onClick={() => (step === 'suspend' ? setReasonOpen(true) : void unsuspend())}>
            {step === 'suspend' ? t.tenantPage.syncSuspend : t.tenantPage.syncUnsuspend}
          </Button>
        </div>
      )}
      <div className="panel-stack">
        <section className="panel glass">
          <Group justify="space-between" wrap="nowrap" gap="sm">
            <h2>{t.tenantPage.quotaTitle}</h2>
            {!deleting && !editing && (
              <Button variant="light" size="sm" onClick={() => { setQuota(String(owner.clientQuota)); setEditing(true); }}>{t.tenantPage.quotaChange}</Button>
            )}
          </Group>
          <Text mt={6}>{fmt(t.tenantPage.quotaOf, { count: owner.clientCount, quota: owner.clientQuota })}</Text>
          {editing && (
            <form className="panel-form" onSubmit={saveQuota} noValidate>
              <TextInput label={t.tenantPage.quotaLabel} description={t.tenantPage.quotaHint} value={quota} inputMode="numeric"
                onChange={(e) => setQuota(e.currentTarget.value)} error={quotaError || undefined} />
              <Group mt="sm" gap="sm">
                <Button type="submit" loading={quotaAct.busy} disabled={quotaAct.waiting}>{t.tenantPage.quotaSave}</Button>
                <Button variant="default" onClick={() => setEditing(false)} disabled={quotaAct.busy}>{t.ui.cancel}</Button>
              </Group>
            </form>
          )}
          <Feedback error={quotaAct.error} />
        </section>

        <section className="panel glass">
          <Group gap="xs">
            <h2>{t.tenantPage.membersTitle}</h2>
            {view.pendingInvites > 0 && <Badge variant="light" size="sm">{fmt(t.tenantPage.pendingInvites, { n: view.pendingInvites })}</Badge>}
          </Group>
          {view.members.length === 0 ? (
            <Text c="dimmed" mt="sm">{t.tenantPage.noMembers}</Text>
          ) : (
            <div className="table-wrap">
              <Table className="data-table">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t.tenantPage.colMember}</Table.Th>
                    <Table.Th>{t.tenantPage.colEmail}</Table.Th>
                    <Table.Th>{t.tenantPage.colRole}</Table.Th>
                    <Table.Th>{t.tenantPage.colJoined}</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {view.members.map((m) => (
                    <Table.Tr key={m.userId}>
                      <Table.Td className="cell-title" data-label={t.tenantPage.colMember}>{m.displayName === '' ? m.email : m.displayName}</Table.Td>
                      <Table.Td className="cell-muted" data-label={t.tenantPage.colEmail}>{m.email}</Table.Td>
                      <Table.Td data-label={t.tenantPage.colRole}>{labelOf(t.tenantPage.roles, m.role)}</Table.Td>
                      <Table.Td className="cell-muted" data-label={t.tenantPage.colJoined}>{formatDate(m.joinedAt, t)}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </div>
          )}
        </section>

        <section className="panel glass">
          <Group justify="space-between" wrap="nowrap" gap="sm">
            <h2>{t.tenantPage.clientsTitle}</h2>
            <Anchor component={Link} to={`/clients?owner=${encodeURIComponent(tenant.tenantId)}`} fz="sm">{t.tenantPage.clientsAll}</Anchor>
          </Group>
          {clients.length === 0 ? (
            <Text c="dimmed" mt="sm">{t.tenantPage.noClients}</Text>
          ) : (
            <div className="table-wrap">
              <Table className="data-table">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t.clients.colName}</Table.Th>
                    <Table.Th>{t.clients.colRpId}</Table.Th>
                    <Table.Th>{t.clients.colStatus}</Table.Th>
                    <Table.Th>{t.clients.colCreated}</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {clients.map((c) => (
                    <Table.Tr key={c.clientId}>
                      <Table.Td className="cell-title" data-label={t.clients.colName}>
                        <Anchor component={Link} to={`/clients/${encodeURIComponent(c.clientId)}`} fw={600} className="cell-name">{c.name}</Anchor>
                      </Table.Td>
                      <Table.Td data-label={t.clients.colRpId}><code className="cell-mono cell-clip">{c.rpId}</code></Table.Td>
                      <Table.Td data-label={t.clients.colStatus}><StatusBadge active={!c.disabledAt} on={t.clients.active} off={t.clients.disabled} /></Table.Td>
                      <Table.Td className="cell-muted" data-label={t.clients.colCreated}>{formatDate(c.createdAt, t)}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </div>
          )}
          {view.clients.nextCursor && <Text fz="sm" c="dimmed" mt="sm">{t.tenantPage.clientsMore}</Text>}
        </section>

        <AuditPanel tid={id} version={version} t={t} />

        {!deleting && (
          <section className="panel glass">
            <h2>{t.tenantPage.dangerTitle}</h2>
            {suspended ? (
              <ActionRow title={t.tenantPage.unsuspend} body={t.tenantPage.unsuspendBody}>
                <Button variant="light" loading={state.loading('unsuspend')} disabled={state.waiting || (state.busy && !state.loading('unsuspend'))}
                  onClick={() => void unsuspend()}>{t.tenantPage.unsuspend}</Button>
              </ActionRow>
            ) : (
              <ActionRow title={t.tenantPage.suspend} body={t.tenantPage.suspendBody}>
                <Button color="red" variant="light" loading={state.loading('suspend')} disabled={state.waiting || (state.busy && !state.loading('suspend'))}
                  onClick={() => setReasonOpen(true)}>{t.tenantPage.suspend}</Button>
              </ActionRow>
            )}
            {last && (
              <Text fz="sm" c="dimmed" mt="sm">
                {fmt(t.tenantPage.lastSuspension, { admin: last.username, date: formatDate(last.at, t), reason: last.reason })}
              </Text>
            )}
            <Feedback error={state.error} />
          </section>
        )}
      </div>
      {reasonOpen && (
        <ReasonDialog title={fmt(t.tenantPage.reasonDialogTitle, { name })} label={t.tenantPage.reasonLabel} confirmLabel={t.reasonDialog.confirm}
          onCancel={() => setReasonOpen(false)} onConfirm={suspend} />
      )}
      {modal}
    </Shell>
  );
}

export function TenantDetail() {
  const { id = '' } = useParams();
  return <SessionGuard>{({ me }) => <Detail key={id} me={me} id={id} />}</SessionGuard>;
}
