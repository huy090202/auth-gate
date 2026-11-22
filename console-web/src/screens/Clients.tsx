import { Anchor, Button, Table, Text, TextInput } from '@mantine/core';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router';
import { consoleTitle, useTitle } from '../brand';
import { fetchAllClients, type ClientSummary } from '../lib/clients';
import { formatDate } from '../lib/format';
import { fmt, useStrings } from '../lib/i18n';
import { SessionGuard, type Me } from '../session';
import { Shell } from '../shell';
import { IdChip, InlineLoading, LoadFailedInline, PageHead, StatusBadge } from '../ui';

function ClientsBody({ me }: { me: Me }) {
  const t = useStrings();
  useTitle(consoleTitle(t.screens.clients));
  const [rows, setRows] = useState<ClientSummary[] | null>(null);
  const [capped, setCapped] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [filter, setFilter] = useState('');

  const load = useCallback(() => {
    setError(null);
    setRows(null);
    fetchAllClients().then(
      (r) => {
        setRows(r.clients);
        setCapped(r.capped);
      },
      (e: unknown) => setError(e ?? new Error('failed')),
    );
  }, []);
  useEffect(load, [load]);

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return (rows ?? []).filter((c) => q === '' || c.name.toLowerCase().includes(q));
  }, [rows, filter]);

  return (
    <Shell me={me}>
      <PageHead title={t.screens.clients} intro={t.clients.intro}
        actions={<Button component={Link} to="/clients/new">{t.clients.newClient}</Button>} />
      {error !== null ? (
        <LoadFailedInline error={error} onRetry={load} />
      ) : rows === null ? (
        <InlineLoading />
      ) : (
        <section className="panel glass">
          <div className="toolbar">
            <TextInput className="toolbar-grow" aria-label={t.clients.filter} placeholder={t.clients.filter} value={filter}
              onChange={(e) => setFilter(e.currentTarget.value)} />
            <Text fz="sm" c="dimmed" role="status">{fmt(t.clients.count, { shown: shown.length, total: rows.length })}</Text>
          </div>
          {capped && <Text fz="sm" c="orange.8" mt="sm" role="status">{fmt(t.clients.capped, { n: rows.length })}</Text>}
          {rows.length === 0 ? (
            <Text c="dimmed" mt="md">{t.clients.empty}</Text>
          ) : shown.length === 0 ? (
            <Text c="dimmed" mt="md">{t.clients.noMatch}</Text>
          ) : (
            <div className="table-wrap">
              <Table className="data-table" highlightOnHover>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t.clients.colName}</Table.Th>
                    <Table.Th>{t.clients.colRpId}</Table.Th>
                    <Table.Th>{t.clients.colOwner}</Table.Th>
                    <Table.Th>{t.clients.colStatus}</Table.Th>
                    <Table.Th>{t.clients.colCreated}</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {shown.map((c) => (
                    <Table.Tr key={c.clientId}>
                      <Table.Td>
                        <Anchor component={Link} to={`/clients/${encodeURIComponent(c.clientId)}`} fw={600} className="cell-name">{c.name}</Anchor>
                        <IdChip id={c.clientId} what={t.clients.id} />
                      </Table.Td>
                      <Table.Td><code className="cell-mono cell-clip">{c.rpId}</code></Table.Td>
                      <Table.Td>
                        {c.owner ? <code className="cell-mono" title={c.owner}>{c.owner.slice(-8)}</code> : <Text fz="sm" c="dimmed">{t.clients.ownerOperator}</Text>}
                      </Table.Td>
                      <Table.Td>
                        <StatusBadge active={!c.disabledAt} on={t.clients.active} off={t.clients.disabled} />
                      </Table.Td>
                      <Table.Td className="cell-muted">{formatDate(c.createdAt, t)}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </div>
          )}
        </section>
      )}
    </Shell>
  );
}

export function Clients() {
  return <SessionGuard>{({ me }) => <ClientsBody me={me} />}</SessionGuard>;
}
