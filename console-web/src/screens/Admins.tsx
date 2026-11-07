import { Badge, Button, NativeSelect, Table, Text, TextInput } from '@mantine/core';
import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { consoleTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { formatDate } from '../lib/format';
import { fmt, useStrings } from '../lib/i18n';
import { SessionGuard, type Me } from '../session';
import { Shell } from '../shell';
import { ShownOnce } from '../ShownOnce';
import { useStepUp } from '../stepup';
import { ConfirmDialog, InlineLoading, LoadFailedInline, PageHead } from '../ui';
import { Feedback, useAction } from './factors';

type Role = 'master' | 'admin';
type AdminRow = {
  username: string; role: Role; mustChangePassword: boolean; lockedUntil: string | null; disabledAt: string | null; createdAt: string;
};
type Pending =
  | { kind: 'reset' | 'clear' | 'disable' | 'enable'; username: string }
  | { kind: 'role'; username: string; role: Role };

const USERNAME = /^[a-z0-9_.-]{3,32}$/;
const enc = encodeURIComponent;

function AdminsBody({ me }: { me: Me }) {
  const t = useStrings();
  useTitle(consoleTitle(t.screens.admins));
  const { stepUp, modal } = useStepUp();
  const [rows, setRows] = useState<AdminRow[] | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const [secret, setSecret] = useState<{ title: string; value: string } | null>(null);
  const [username, setUsername] = useState('');
  const [role, setRole] = useState<Role>('admin');
  const [nameError, setNameError] = useState('');
  const rowAction = useAction();
  const create = useAction();

  const load = useCallback(() => {
    setLoadError(null);
    return api<{ admins?: AdminRow[] }>('/api/admins').then(
      (r) => setRows(r.admins ?? []),
      (e: unknown) => setLoadError(e ?? new Error('failed')),
    );
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  const submitCreate = async (ev: FormEvent) => {
    ev.preventDefault();
    // Normalised the way the backend does, because the step-up token is bound to this exact string.
    const name = username.trim().toLowerCase();
    if (!USERNAME.test(name)) return setNameError(t.errors.invalid_username);
    setNameError('');
    await create.run(async () => {
      const r = await stepUp('admin-create', (token) =>
        api<{ username: string; temporaryPassword: string }>('/api/admins', { body: { username: name, role }, stepUpToken: token }), `${name}/${role}`);
      if (!r) return false;
      setUsername('');
      setSecret({ title: fmt(t.admins.tempTitle, { username: r.username }), value: r.temporaryPassword });
      await load();
      return fmt(t.admins.createdNote, { username: r.username });
    });
  };

  const perform = (p: Pending) => {
    const u = p.username;
    const base = `/api/admins/${enc(u)}`;
    // Each call is bound to the username (the role action also to the new role): the same string the backend rebuilds.
    const key = `${p.kind}:${u}`;
    return rowAction.run(async () => {
      switch (p.kind) {
        case 'reset': {
          const r = await stepUp('admin-reset-password', (token) =>
            api<{ temporaryPassword: string }>(`${base}/reset-password`, { method: 'POST', stepUpToken: token }), u);
          if (!r) return false;
          setSecret({ title: fmt(t.admins.tempTitle, { username: u }), value: r.temporaryPassword });
          await load();
          return '';
        }
        case 'clear': {
          if (!(await stepUp('admin-clear-factors', (token) => api(`${base}/clear-factors`, { method: 'POST', stepUpToken: token }), u))) return false;
          await load();
          return fmt(t.admins.clearedNote, { username: u });
        }
        case 'disable':
        case 'enable': {
          const action = p.kind === 'disable' ? 'admin-disable' : 'admin-enable';
          if (!(await stepUp(action, (token) => api(`${base}/${p.kind}`, { method: 'POST', stepUpToken: token }), u))) return false;
          await load();
          return fmt(p.kind === 'disable' ? t.admins.disabledNote : t.admins.enabledNote, { username: u });
        }
        case 'role': {
          const body = { role: p.role };
          if (!(await stepUp('admin-role', (token) => api(`${base}/role`, { method: 'POST', body, stepUpToken: token }), `${u}/${p.role}`))) return false;
          await load();
          return fmt(t.admins.roleNote, { username: u, role: t.common.role[p.role] });
        }
      }
    }, '', key);
  };

  const copy = (p: Pending): { title: string; body: string; confirm: string; danger: boolean } => {
    const username = p.username;
    switch (p.kind) {
      case 'reset': return { title: fmt(t.admins.resetTitle, { username }), body: t.admins.resetBody, confirm: t.admins.reset, danger: true };
      case 'clear': return { title: fmt(t.admins.clearTitle, { username }), body: t.admins.clearBody, confirm: t.admins.clearFactors, danger: true };
      case 'disable': return { title: fmt(t.admins.disableTitle, { username }), body: t.admins.disableBody, confirm: t.admins.disable, danger: true };
      case 'enable': return { title: fmt(t.admins.enableTitle, { username }), body: t.admins.enableBody, confirm: t.admins.enable, danger: false };
      case 'role': return {
        title: fmt(t.admins.roleTitle, { username, role: t.common.role[p.role] }), body: t.admins.roleBody,
        confirm: p.role === 'master' ? t.admins.makeMaster : t.admins.makeAdmin, danger: p.role === 'admin',
      };
    }
  };

  const ask = (p: Pending) => setPending(p);
  const dialog = pending && copy(pending);

  return (
    <Shell me={me}>
      <PageHead title={t.screens.admins} intro={t.admins.intro} />
      <div className="panel-stack">
        <form className="panel glass" onSubmit={(ev) => void submitCreate(ev)} noValidate>
          <h2>{t.admins.createTitle}</h2>
          <Text fz="sm" c="dimmed" mt={6}>{t.admins.createNote}</Text>
          <div className="toolbar toolbar-form">
            <TextInput className="toolbar-grow" label={t.admins.username} description={t.admins.usernameHint} value={username}
              onChange={(e) => setUsername(e.currentTarget.value)} error={nameError || undefined} disabled={create.busy}
              autoComplete="off" spellCheck={false} required />
            <NativeSelect label={t.admins.role} value={role} onChange={(e) => setRole(e.currentTarget.value as Role)} disabled={create.busy}
              data={[{ value: 'admin', label: t.common.role.admin }, { value: 'master', label: t.common.role.master }]} />
            <Button type="submit" loading={create.busy} disabled={create.waiting}>{t.admins.create}</Button>
          </div>
          <Feedback error={create.error} note={create.note} />
        </form>

        {loadError !== null ? (
          <LoadFailedInline error={loadError} onRetry={() => void load()} />
        ) : rows === null ? (
          <InlineLoading />
        ) : (
          <section className="panel glass">
            {rows.length === 0 ? (
              <Text c="dimmed">{t.admins.empty}</Text>
            ) : (
              <div className="table-wrap">
                <Table className="data-table" highlightOnHover>
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th>{t.admins.colUser}</Table.Th>
                      <Table.Th>{t.admins.colRole}</Table.Th>
                      <Table.Th>{t.admins.colStatus}</Table.Th>
                      <Table.Th>{t.admins.colCreated}</Table.Th>
                      <Table.Th>{t.admins.colActions}</Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {rows.map((r) => {
                      const self = r.username === me.username;
                      const btn = (kind: Pending['kind'], label: string, p: Pending, color?: string) => (
                        <Button size="compact-sm" variant="light" color={color} disabled={self || ((rowAction.busy || rowAction.waiting) && !rowAction.loading(`${kind}:${r.username}`))}
                          loading={rowAction.loading(`${kind}:${r.username}`)}
                          aria-label={fmt(t.admins.rowFor, { action: label, username: r.username })} onClick={() => ask(p)}>
                          {label}
                        </Button>
                      );
                      return (
                        <Table.Tr key={r.username}>
                          <Table.Td>
                            <span className="actor">
                              <span className="actor-name cell-strong">{r.username}</span>
                              {self && <Badge size="xs" variant="outline" color="gray">{t.admins.you}</Badge>}
                            </span>
                          </Table.Td>
                          <Table.Td>
                            <Badge variant="light" color={r.role === 'master' ? 'canopy' : 'gray'}>{t.common.role[r.role]}</Badge>
                          </Table.Td>
                          <Table.Td>
                            <span className="chips">
                              {r.disabledAt ? <Badge variant="light" color="gray">{t.admins.disabled}</Badge> : <Badge variant="light" color="canopy">{t.admins.active}</Badge>}
                              {r.lockedUntil && <Badge variant="light" color="orange">{fmt(t.admins.locked, { time: formatDate(r.lockedUntil, t) })}</Badge>}
                              {r.mustChangePassword && <Badge variant="outline" color="gray">{t.admins.mustChange}</Badge>}
                            </span>
                          </Table.Td>
                          <Table.Td className="cell-muted cell-nowrap">{formatDate(r.createdAt, t)}</Table.Td>
                          <Table.Td>
                            <div className="row-actions">
                              {btn('reset', t.admins.reset, { kind: 'reset', username: r.username })}
                              {btn('clear', t.admins.clearFactors, { kind: 'clear', username: r.username })}
                              {r.disabledAt
                                ? btn('enable', t.admins.enable, { kind: 'enable', username: r.username })
                                : btn('disable', t.admins.disable, { kind: 'disable', username: r.username }, 'red')}
                              {btn('role', r.role === 'master' ? t.admins.makeAdmin : t.admins.makeMaster,
                                { kind: 'role', username: r.username, role: r.role === 'master' ? 'admin' : 'master' })}
                            </div>
                            {self && <Text fz="xs" c="dimmed" mt={6}>{t.admins.selfNote}</Text>}
                          </Table.Td>
                        </Table.Tr>
                      );
                    })}
                  </Table.Tbody>
                </Table>
              </div>
            )}
            <Feedback error={rowAction.error} note={rowAction.note} />
          </section>
        )}
      </div>
      {pending && dialog && (
        <ConfirmDialog title={dialog.title} body={dialog.body} confirmLabel={dialog.confirm} danger={dialog.danger}
          onCancel={() => setPending(null)}
          onConfirm={() => {
            setPending(null);
            void perform(pending);
          }} />
      )}
      {modal}
      {secret && <ShownOnce title={secret.title} label={t.admins.tempLabel} value={secret.value} onClose={() => setSecret(null)} />}
    </Shell>
  );
}

export function Admins() {
  return <SessionGuard role="master">{({ me }) => <AdminsBody me={me} />}</SessionGuard>;
}
