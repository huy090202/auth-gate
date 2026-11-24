import { Button, Group, Modal, Select, Stack, Table, Text, TextInput } from '@mantine/core';
import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { Navigate, useParams } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { formatDate } from '../lib/format';
import { fmt, useStrings } from '../lib/i18n';
import { MAX_INVITES, can, orgApi, roleChoices, type Member, type PendingInvite } from '../lib/orgs';
import { clientsPath, useWorkspaces, type Role } from '../lib/tenants';
import { useStepUp } from '../stepup';
import { InlineLoading, LoadFailedInline, PageHead } from '../ui';
import { Feedback, useAction } from './forms';

function InviteDialog({ tenantId, actor, onClose, onSent }: { tenantId: string; actor: Role; onClose: () => void; onSent: () => Promise<void> }) {
  const t = useStrings();
  const [email, setEmail] = useState('');
  const choices = roleChoices(actor, 'member');
  const [role, setRole] = useState<Role>('member');
  const send = useAction();
  const submit = (ev: FormEvent) => {
    ev.preventDefault();
    void send.run(async () => {
      await api(orgApi.invites(tenantId), { method: 'POST', body: { email: email.trim(), role } });
      await onSent();
      onClose();
      return fmt(t.members.invited, { email: email.trim() });
    });
  };
  return (
    <Modal opened onClose={onClose} title={t.members.inviteTitle} centered>
      <form onSubmit={submit} noValidate>
        <Stack gap="md">
          <TextInput type="email" label={t.members.email} value={email} autoComplete="off" disabled={send.busy}
            onChange={(e) => setEmail(e.currentTarget.value)} data-autofocus />
          <Select label={t.members.role} aria-label={t.members.role} value={role} allowDeselect={false} disabled={send.busy}
            data={choices.map((r) => ({ value: r, label: t.roles[r] }))} onChange={(v) => v && setRole(v as Role)} />
          <Feedback error={send.error} />
          <Group justify="flex-end" gap="sm">
            <Button variant="default" onClick={onClose}>{t.ui.cancel}</Button>
            <Button type="submit" loading={send.busy} disabled={send.waiting || email.trim() === ''}>{t.members.send}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}

function Roster({ tenantId, actor, userId }: { tenantId: string; actor: Role; userId: string }) {
  const t = useStrings();
  const { stepUp, modal } = useStepUp();
  const [members, setMembers] = useState<Member[] | null>(null);
  const [invites, setInvites] = useState<PendingInvite[]>([]);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [inviting, setInviting] = useState(false);
  const change = useAction();
  const invite = useAction();
  const canInvite = can(actor, 'invite');

  const loadInvites = useCallback(async () => {
    const r = await api<{ invites?: PendingInvite[] }>(orgApi.invites(tenantId));
    setInvites(r.invites ?? []);
  }, [tenantId]);
  const load = useCallback(async () => {
    setLoadError(null);
    try {
      const r = await api<{ members?: Member[] }>(orgApi.members(tenantId));
      setMembers(r.members ?? []);
      if (canInvite) await loadInvites();
    } catch (e) {
      setLoadError(e ?? new Error('failed'));
    }
  }, [tenantId, canInvite, loadInvites]);
  useEffect(() => {
    void load();
  }, [load]);

  if (loadError !== null) return <LoadFailedInline error={loadError} onRetry={() => void load()} />;
  if (!members) return <InlineLoading />;

  const setRole = (m: Member, role: string | null) => {
    if (!role || role === m.role) return;
    void change.run(async () => {
      const r = await stepUp('change-role', (token) =>
        api(orgApi.member(tenantId, m.userId), { method: 'PATCH', body: { role }, stepUpToken: token }), `${tenantId}/${m.userId}`);
      if (!r) return false;
      await load();
    }, t.members.roleChanged);
  };
  const remove = (m: Member) => void change.run(async () => {
    const r = await stepUp('remove-member', (token) =>
      api(orgApi.member(tenantId, m.userId), { method: 'DELETE', stepUpToken: token }), `${tenantId}/${m.userId}`);
    if (!r) return false;
    await load();
    return fmt(t.members.removed, { name: m.displayName });
  });

  return (
    <div className="panel-stack">
      <section className="panel glass">
        {canInvite && (
          <div className="toolbar">
            <Button onClick={() => setInviting(true)}>{t.members.invite}</Button>
          </div>
        )}
        <div className="table-wrap">
          <Table className="data-table">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>{t.members.colName}</Table.Th>
                <Table.Th>{t.members.colEmail}</Table.Th>
                <Table.Th>{t.members.colRole}</Table.Th>
                <Table.Th>{t.members.colJoined}</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {members.map((m) => {
                const choices = can(actor, 'change-role', m.role) ? roleChoices(actor, m.role) : [];
                return (
                  <Table.Tr key={m.userId}>
                    <Table.Td><span className="cell-name">{m.displayName}</span>{m.userId === userId && <> <span className="cell-muted">{t.members.you}</span></>}</Table.Td>
                    <Table.Td className="cell-muted">{m.email}</Table.Td>
                    <Table.Td>
                      {choices.length > 0 ? (
                        <Select aria-label={fmt(t.members.roleOf, { name: m.displayName })} value={m.role} allowDeselect={false}
                          disabled={change.busy || change.waiting} data={choices.map((r) => ({ value: r, label: t.roles[r] }))}
                          onChange={(v) => setRole(m, v)} />
                      ) : t.roles[m.role]}
                    </Table.Td>
                    <Table.Td className="cell-muted">{formatDate(m.joinedAt, t)}</Table.Td>
                    <Table.Td>
                      {can(actor, 'remove', m.role) && m.userId !== userId && (
                        <Button size="xs" color="red" variant="light" disabled={change.busy || change.waiting} onClick={() => remove(m)}>
                          {t.members.remove}
                        </Button>
                      )}
                    </Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        </div>
        <Feedback error={change.error} />
      </section>

      {canInvite && (
        <section className="panel glass">
          <h2>{fmt(t.members.pending, { n: invites.length, max: MAX_INVITES })}</h2>
          {invites.length === 0 ? (
            <Text c="dimmed">{t.members.pendingEmpty}</Text>
          ) : (
            <div className="table-wrap">
              <Table className="data-table">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t.members.colEmail}</Table.Th>
                    <Table.Th>{t.members.colRole}</Table.Th>
                    <Table.Th>{t.members.colInvitedBy}</Table.Th>
                    <Table.Th>{t.members.colExpires}</Table.Th>
                    <Table.Th />
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {invites.map((i) => (
                    <Table.Tr key={i.inviteId}>
                      <Table.Td>{i.email}</Table.Td>
                      <Table.Td>{t.roles[i.role]}</Table.Td>
                      <Table.Td className="cell-muted">{i.invitedBy.displayName}</Table.Td>
                      <Table.Td className="cell-muted">{formatDate(i.expiresAt, t)}</Table.Td>
                      <Table.Td>
                        <Group gap="xs" wrap="nowrap">
                          <Button size="xs" variant="light" disabled={invite.busy || invite.waiting}
                            onClick={() => void invite.run(async () => {
                              await api(orgApi.resend(tenantId, i.inviteId), { method: 'POST' });
                              await loadInvites();
                              return fmt(t.members.resent, { email: i.email });
                            })}>{t.members.resend}</Button>
                          <Button size="xs" color="red" variant="light" disabled={invite.busy || invite.waiting}
                            onClick={() => void invite.run(async () => {
                              await api(orgApi.invite(tenantId, i.inviteId), { method: 'DELETE' });
                              await loadInvites();
                            }, t.members.revoked)}>{t.members.revoke}</Button>
                        </Group>
                      </Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </div>
          )}
          <Feedback error={invite.error} />
        </section>
      )}
      {inviting && <InviteDialog tenantId={tenantId} actor={actor} onClose={() => setInviting(false)} onSent={loadInvites} />}
      {modal}
    </div>
  );
}

export function Members() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.members));
  const { tenantId = '' } = useParams();
  const { tenants, userId } = useWorkspaces();
  const ws = tenants.find((x) => x.tenantId === tenantId);
  // A personal workspace has no members; the nav hides the link, and a typed URL goes back to the clients.
  if (ws?.kind === 'personal') return <Navigate to={clientsPath(tenantId)} replace />;
  return (
    <>
      <PageHead title={t.screens.members} intro={t.members.intro} />
      <Roster key={tenantId} tenantId={tenantId} actor={ws?.role ?? 'member'} userId={userId} />
    </>
  );
}
