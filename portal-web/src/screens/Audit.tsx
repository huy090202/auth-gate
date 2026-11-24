import { Badge, Button, Table, Text } from '@mantine/core';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useParams } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { auditUrl, detailKeys, failureLabel, failureOf, labelOf, type AuditEntry } from '../lib/audit';
import { explain } from '../lib/explain';
import { formatDate } from '../lib/format';
import { useStrings } from '../lib/i18n';
import type { Strings } from '../strings';
import { IdChip, InlineLoading, LoadFailedInline, PageHead } from '../ui';

function DetailChips({ e, t }: { e: AuditEntry; t: Strings }) {
  const keys = detailKeys(e.detail);
  if (keys.length === 0) return null;
  const text = (k: string, v: string) =>
    k === 'fields' ? v.split(',').map((f) => labelOf(t.audit.fields, f)).join(', ') : v;
  return (
    <span className="chips">
      {keys.map((k) => (
        <span key={k} className="chip">{labelOf(t.audit.detailKeys, k)}: {text(k, e.detail![k] ?? '')}</span>
      ))}
    </span>
  );
}

function ResultBadge({ result, t }: { result: string; t: Strings }) {
  const code = failureOf(result);
  if (code === null) return <Badge variant="light" color="canopy">{t.audit.success}</Badge>;
  return <Badge variant="light" color="red" title={result}>{code === '' ? (result || '?') : failureLabel(code, t)}</Badge>;
}

export function Audit() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.audit));
  const { tenantId = '' } = useParams();
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [next, setNext] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const seq = useRef(0);

  const fetchPage = useCallback(async (cursor: string) => {
    const mine = ++seq.current; // a slower answer for a workspace the user has left must not land
    setLoading(true);
    setError(null);
    try {
      const r = await api<{ entries?: AuditEntry[]; nextCursor?: string }>(auditUrl(tenantId, cursor));
      if (mine !== seq.current) return;
      setEntries((prev) => (cursor ? [...prev, ...(r.entries ?? [])] : (r.entries ?? [])));
      setNext(r.nextCursor ?? '');
    } catch (e) {
      if (mine === seq.current) setError(e ?? new Error('failed'));
    } finally {
      if (mine === seq.current) setLoading(false);
    }
  }, [tenantId]);

  useEffect(() => {
    setEntries([]);
    setNext('');
    void fetchPage('');
  }, [fetchPage]);

  return (
    <>
      <PageHead title={t.screens.audit} intro={t.audit.intro} />
      <section className="panel glass">
        {error !== null && entries.length === 0 ? (
          <LoadFailedInline error={error} onRetry={() => void fetchPage('')} />
        ) : loading && entries.length === 0 ? (
          <InlineLoading />
        ) : entries.length === 0 ? (
          <Text c="dimmed">{t.audit.empty}</Text>
        ) : (
          <>
            <div className="table-wrap">
              <Table className="data-table">
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>{t.audit.colTime}</Table.Th>
                    <Table.Th>{t.audit.colActor}</Table.Th>
                    <Table.Th>{t.audit.colAction}</Table.Th>
                    <Table.Th>{t.audit.colTarget}</Table.Th>
                    <Table.Th>{t.audit.colResult}</Table.Th>
                    <Table.Th>{t.audit.colIp}</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {entries.map((e, i) => (
                    <Table.Tr key={i}>
                      <Table.Td className="cell-muted cell-title" data-label={t.audit.colTime} title={e.at}>{formatDate(e.at, t)}</Table.Td>
                      <Table.Td data-label={t.audit.colActor}>{e.actorName ? e.actorName : <span className="cell-muted">{t.audit.unknownActor}</span>}</Table.Td>
                      <Table.Td data-label={t.audit.colAction}>
                        <div className="cell-strong">{labelOf(t.audit.actions, e.action)}</div>
                        <DetailChips e={e} t={t} />
                      </Table.Td>
                      <Table.Td data-label={t.audit.colTarget}>{e.target === '' ? <span className="cell-muted">—</span> : <IdChip id={e.target} what={t.audit.colTarget} />}</Table.Td>
                      <Table.Td data-label={t.audit.colResult}><ResultBadge result={e.result} t={t} /></Table.Td>
                      <Table.Td data-label={t.audit.colIp}>{e.ip ? <code className="cell-mono">{e.ip}</code> : <span className="cell-muted">—</span>}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </div>
            {error !== null && <Text c="red.7" fz="sm" mt="md" role="alert">{explain(error, t)}</Text>}
            {next !== '' && (
              <div className="more-row">
                <Button variant="light" loading={loading} onClick={() => void fetchPage(next)}>{t.audit.loadMore}</Button>
              </div>
            )}
          </>
        )}
      </section>
    </>
  );
}
