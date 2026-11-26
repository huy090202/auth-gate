import { Anchor, Badge, Button, Table, Text, TextInput } from '@mantine/core';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router';
import { consoleTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { explain } from '../lib/explain';
import { fmt, useStrings } from '../lib/i18n';
import { tenantName, tenantStatus, type TenantRow } from '../lib/operator';
import { SessionGuard, type Me } from '../session';
import { Shell } from '../shell';
import type { Strings } from '../strings';
import { IdChip, InlineLoading, LoadFailedInline, PageHead } from '../ui';

export function TenantStatusBadge({ row, t }: { row: Pick<TenantRow, 'suspendedAt' | 'deletingAt'>; t: Strings }) {
  const s = tenantStatus(row);
  if (s === 'deleting') return <Badge variant="light" color="gray">{t.tenants.deleting}</Badge>;
  if (s === 'suspended') return <Badge variant="light" color="orange">{t.tenants.suspended}</Badge>;
  return <Badge variant="light" color="canopy">{t.tenants.active}</Badge>;
}

function TenantsBody({ me }: { me: Me }) {
  const t = useStrings();
  useTitle(consoleTitle(t.screens.tenants));
  const [rows, setRows] = useState<TenantRow[] | null>(null);
  const [next, setNext] = useState('');
  const [error, setError] = useState<unknown>(null);
  const [moreError, setMoreError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [filter, setFilter] = useState('');
  const seq = useRef(0);

  const fetchPage = useCallback(async (cursor: string) => {
    const mine = ++seq.current;
    setLoading(true);
    setMoreError(null);
    try {
      const r = await api<{ tenants?: TenantRow[]; nextCursor?: string }>(`/api/tenants${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`);
      if (mine !== seq.current) return;
      setRows((prev) => (cursor ? [...(prev ?? []), ...(r.tenants ?? [])] : (r.tenants ?? [])));
      setNext(r.nextCursor ?? '');
      setError(null);
    } catch (e) {
      if (mine !== seq.current) return;
      if (cursor) setMoreError(e ?? new Error('failed'));
      else setError(e ?? new Error('failed'));
    } finally {
      if (mine === seq.current) setLoading(false);
    }
  }, []);
  useEffect(() => {
    void fetchPage('');
  }, [fetchPage]);

  const label = useCallback((r: TenantRow) => tenantName(r, t.tenants.personalName), [t]);
  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return (rows ?? []).filter((r) => q === '' || label(r).toLowerCase().includes(q) || r.creatorEmail.toLowerCase().includes(q));
  }, [rows, filter, label]);

  return (
    <Shell me={me}>
      <PageHead title={t.screens.tenants} intro={t.tenants.intro} />
      {error !== null ? (
        <LoadFailedInline error={error} onRetry={() => void fetchPage('')} />
      ) : rows === null ? (
        <InlineLoading />
      ) : (
        <section className="panel glass">
          <div className="toolbar">
            <TextInput className="toolbar-grow" aria-label={t.tenants.filter} placeholder={t.tenants.filter} value={filter}
              onChange={(e) => setFilter(e.currentTarget.value)} />
            <Text fz="sm" c="dimmed" role="status">{fmt(t.tenants.count, { shown: shown.length, total: rows.length })}</Text>
          </div>
          {rows.length === 0 ? (
            <Text c="dimmed" mt="md">{t.tenants.empty}</Text>
          ) : shown.length === 0 ? (
            <Text c="dimmed" mt="md">{t.tenants.noMatch}</Text>
          ) : (
            <div className="table-wrap">
              <Table className="data-table" highlightOnHover>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t.tenants.colName}</Table.Th>
                    <Table.Th>{t.tenants.colKind}</Table.Th>
                    <Table.Th>{t.tenants.colCreator}</Table.Th>
                    <Table.Th>{t.tenants.colMembers}</Table.Th>
                    <Table.Th>{t.tenants.colClients}</Table.Th>
                    <Table.Th>{t.tenants.colStatus}</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {shown.map((r) => (
                    <Table.Tr key={r.tenantId}>
                      <Table.Td className="cell-title" data-label={t.tenants.colName}>
                        <Anchor component={Link} to={`/tenants/${encodeURIComponent(r.tenantId)}`} fw={600} className="cell-name">{label(r)}</Anchor>
                        <IdChip id={r.tenantId} what={t.tenants.id} />
                      </Table.Td>
                      <Table.Td data-label={t.tenants.colKind}>{r.kind === 'org' ? t.tenants.kindOrg : t.tenants.kindPersonal}</Table.Td>
                      <Table.Td className="cell-muted" data-label={t.tenants.colCreator}>{r.creatorEmail === '' ? '—' : r.creatorEmail}</Table.Td>
                      <Table.Td data-label={t.tenants.colMembers}>{r.memberCount}</Table.Td>
                      <Table.Td data-label={t.tenants.colClients}>
                        {r.clientCount === undefined || r.clientQuota === undefined
                          ? <span className="cell-muted">{t.tenants.quotaUnknown}</span>
                          : fmt(t.tenants.clientsOfQuota, { count: r.clientCount, quota: r.clientQuota })}
                      </Table.Td>
                      <Table.Td data-label={t.tenants.colStatus}><TenantStatusBadge row={r} t={t} /></Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </div>
          )}
          {moreError !== null && <Text c="red.7" fz="sm" mt="md" role="alert">{explain(moreError, t)}</Text>}
          {next !== '' && (
            <div className="more-row">
              <Button variant="light" loading={loading} onClick={() => void fetchPage(next)}>{t.tenants.loadMore}</Button>
            </div>
          )}
        </section>
      )}
    </Shell>
  );
}

export function Tenants() {
  return <SessionGuard>{({ me }) => <TenantsBody me={me} />}</SessionGuard>;
}
