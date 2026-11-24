import { Anchor, Button, Table, Text, TextInput } from '@mantine/core';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useParams } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { fetchAllClients, type ClientSummary } from '../lib/clients';
import { formatDate } from '../lib/format';
import { fmt, useStrings } from '../lib/i18n';
import { clientPath, clientsPath } from '../lib/tenants';
import { IdChip, InlineLoading, LoadFailedInline, PageHead, StatusBadge } from '../ui';

/** The table of one workspace's clients, or the empty state. Exported for the list's tests. */
export function ClientList({ rows, capped, tenantId }: { rows: ClientSummary[]; capped: boolean; tenantId: string }) {
  const t = useStrings();
  const [filter, setFilter] = useState('');
  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return rows.filter((c) => q === '' || c.name.toLowerCase().includes(q));
  }, [rows, filter]);
  return (
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
                <Table.Th>{t.clients.colStatus}</Table.Th>
                <Table.Th>{t.clients.colCreated}</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {shown.map((c) => (
                <Table.Tr key={c.clientId}>
                  <Table.Td className="cell-title" data-label={t.clients.colName}>
                    <Anchor component={Link} to={clientPath(tenantId, c.clientId)} fw={600} className="cell-name">{c.name}</Anchor>
                    <IdChip id={c.clientId} what={t.clients.id} />
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
    </section>
  );
}

export function Clients() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.clients));
  const { tenantId = '' } = useParams();
  const [rows, setRows] = useState<ClientSummary[] | null>(null);
  const [capped, setCapped] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const load = useCallback(() => {
    setError(null);
    setRows(null);
    fetchAllClients(tenantId).then(
      (r) => {
        setRows(r.clients);
        setCapped(r.capped);
      },
      (e: unknown) => setError(e ?? new Error('failed')),
    );
  }, [tenantId]);
  useEffect(load, [load]);

  return (
    <>
      <PageHead title={t.screens.clients} intro={t.clients.intro}
        actions={<Button component={Link} to={`${clientsPath(tenantId)}/new`}>{t.clients.newClient}</Button>} />
      {error !== null ? <LoadFailedInline error={error} onRetry={load} />
        : rows === null ? <InlineLoading />
        : <ClientList rows={rows} capped={capped} tenantId={tenantId} />}
    </>
  );
}
