import { Badge, Button, Select, Table, Text } from '@mantine/core';
import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { consoleTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import {
  detailKeys, failureLabel, failureOf, labelOf, monthLabel, parsePairs, recentMonths, type AuditEntry,
} from '../lib/audit';
import { explain } from '../lib/explain';
import { formatDate } from '../lib/format';
import { fmt, useStrings } from '../lib/i18n';
import { SessionGuard, type Me } from '../session';
import { Shell } from '../shell';
import type { Strings } from '../strings';
import { ChevronIcon, IdChip, InlineLoading, LoadFailedInline, PageHead } from '../ui';

const ALL = '__all__';
const LONG_LIST = 8; // a list longer than this can be searched
const NONE = '__none__'; // an entry with no actor name (a failed sign-in of an unknown account)

function Value({ k, v, t }: { k: string; v: string; t: Strings }) {
  const list = (items: string[], mono = false) => (
    <span className="chips">{items.map((s, i) => <span key={i} className={mono ? 'chip chip-mono' : 'chip'}>{s}</span>)}</span>
  );
  switch (k) {
    case 'reason': return <>{labelOf(t.audit.reasons, v)}</>;
    case 'action': return <>{labelOf(t.audit.actions, v)}</>;
    case 'factor': return <>{labelOf(t.audit.factors, v)}</>;
    case 'role': return <>{labelOf(t.common.role, v)}</>;
    case 'name': return <>{v}</>;
    case 'fields': return list(v.split(',').map((f) => labelOf(t.audit.detailKeys, f)));
    case 'origins': return list(v.split(',').filter(Boolean), true);
    case 'pinPolicy':
    case 'rateLimit': {
      const pairs = parsePairs(v);
      return pairs ? list(pairs.map(([a, b]) => `${labelOf(t.audit.parts, a)}: ${b}`)) : <code className="cell-mono">{v}</code>;
    }
    default: return <code className="cell-mono">{v}</code>;
  }
}

function Details({ e, t }: { e: AuditEntry; t: Strings }) {
  const keys = detailKeys(e.detail);
  return (
    <dl className="detail-grid">
      {keys.map((k) => (
        <Fragment key={k}>
          <dt>{labelOf(t.audit.detailKeys, k)}</dt>
          <dd><Value k={k} v={e.detail![k] ?? ''} t={t} /></dd>
        </Fragment>
      ))}
      <dt>{t.audit.ip}</dt>
      <dd>{e.ip ? <code className="cell-mono">{e.ip}</code> : <span className="cell-muted">—</span>}</dd>
      {keys.length === 0 && <dd className="detail-empty cell-muted">{t.audit.noDetail}</dd>}
    </dl>
  );
}

function ResultBadge({ result, t }: { result: string; t: Strings }) {
  const code = failureOf(result);
  if (code === null) return <Badge variant="light" color="canopy">{t.audit.success}</Badge>;
  return <Badge variant="light" color="red" title={result}>{code === '' ? (result || '?') : failureLabel(code, t)}</Badge>;
}

function AuditBody({ me }: { me: Me }) {
  const t = useStrings();
  useTitle(consoleTitle(t.screens.audit));
  const months = useMemo(() => recentMonths(new Date(), 12), []);
  const [month, setMonth] = useState(months[0]!);
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [next, setNext] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const [open, setOpen] = useState<Set<number>>(new Set());
  const [seen, setSeen] = useState<Set<number>>(new Set()); // rows opened at least once keep their content for the close animation
  const [fAction, setFAction] = useState(ALL);
  const [fResult, setFResult] = useState(ALL);
  const [fActor, setFActor] = useState(ALL);
  const seq = useRef(0);

  const fetchPage = useCallback(async (m: string, cursor: string) => {
    const mine = ++seq.current; // a slower answer for a month the user has left must not land
    setLoading(true);
    setError(null);
    try {
      const r = await api<{ entries?: AuditEntry[]; nextCursor?: string }>(
        `/api/audit?month=${encodeURIComponent(m)}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`,
      );
      if (mine !== seq.current) return;
      setEntries((prev) => (cursor ? [...prev, ...(r.entries ?? [])] : (r.entries ?? [])));
      setNext(r.nextCursor ?? '');
    } catch (e) {
      if (mine === seq.current) setError(e ?? new Error('failed'));
    } finally {
      if (mine === seq.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    setEntries([]);
    setNext('');
    setOpen(new Set());
    setSeen(new Set());
    setFAction(ALL);
    setFResult(ALL);
    setFActor(ALL);
    void fetchPage(month, '');
  }, [month, fetchPage]);

  const actionOptions = useMemo(() => {
    const seen = [...new Set(entries.map((e) => e.action))];
    return seen.map((a) => ({ value: a, label: labelOf(t.audit.actions, a) })).sort((a, b) => a.label.localeCompare(b.label, t.locale));
  }, [entries, t]);
  const resultOptions = useMemo(() => {
    const codes = [...new Set(entries.map((e) => failureOf(e.result)).filter((c): c is string => c !== null && c !== ''))];
    return [
      { value: ALL, label: t.audit.all }, { value: 'success', label: t.audit.success }, { value: 'failed', label: t.audit.failedAny },
      ...codes.map((c) => ({ value: `f:${c}`, label: `${t.audit.failedPrefix}: ${failureLabel(c, t)}` })),
    ];
  }, [entries, t]);
  const actorOptions = useMemo(() => {
    const names = [...new Set(entries.map((e) => e.username))];
    return [
      { value: ALL, label: t.audit.all },
      ...names.map((n) => ({ value: n === '' ? NONE : n, label: n === '' ? t.audit.unknownActor : n })),
    ];
  }, [entries, t]);

  const shown = entries.flatMap((e, i) => (matches(e) ? [{ e, i }] : []));
  function matches(e: AuditEntry): boolean {
    if (fAction !== ALL && e.action !== fAction) return false;
    if (fActor !== ALL && (e.username === '' ? NONE : e.username) !== fActor) return false;
    const code = failureOf(e.result);
    if (fResult === 'success') return code === null;
    if (fResult === 'failed') return code !== null;
    if (fResult.startsWith('f:')) return code === fResult.slice(2);
    return true;
  }
  const toggle = (i: number) => {
    setSeen((s) => (s.has(i) ? s : new Set(s).add(i)));
    setOpen((s) => {
      const n = new Set(s);
      if (!n.delete(i)) n.add(i);
      return n;
    });
  };

  return (
    <Shell me={me}>
      <PageHead title={t.screens.audit} intro={t.audit.intro} />
      <section className="panel glass">
        <div className="toolbar">
          <Select label={t.audit.month} value={month} onChange={(v) => v !== null && setMonth(v)}
            data={months.map((m) => ({ value: m, label: monthLabel(m, t) }))} />
          <Select label={t.audit.filterAction} value={fAction} onChange={(v) => v !== null && setFAction(v)}
            data={[{ value: ALL, label: t.audit.all }, ...actionOptions]} searchable={actionOptions.length > LONG_LIST} />
          <Select label={t.audit.filterResult} value={fResult} onChange={(v) => v !== null && setFResult(v)} data={resultOptions}
            searchable={resultOptions.length > LONG_LIST} />
          <Select label={t.audit.filterActor} value={fActor} onChange={(v) => v !== null && setFActor(v)} data={actorOptions}
            searchable={actorOptions.length > LONG_LIST} />
        </div>
        <Text fz="xs" c="dimmed" mt="sm">{t.audit.filtersNote}</Text>
        {error !== null && entries.length === 0 ? (
          <div className="inset-error"><LoadFailedInline error={error} onRetry={() => void fetchPage(month, '')} /></div>
        ) : loading && entries.length === 0 ? (
          <InlineLoading />
        ) : entries.length === 0 ? (
          <Text c="dimmed" mt="md">{t.audit.empty}</Text>
        ) : (
          <>
            <Text fz="sm" c="dimmed" mt="md" role="status">{fmt(t.audit.count, { shown: shown.length, total: entries.length })}</Text>
            {shown.length === 0 ? (
              <Text c="dimmed" mt="sm">{t.audit.noMatch}</Text>
            ) : (
              <div className="table-wrap">
                <Table className="data-table audit-table">
                  <Table.Thead>
                    <Table.Tr>
                      <Table.Th className="col-toggle"><span className="sr-only">{t.audit.show}</span></Table.Th>
                      <Table.Th className="col-time">{t.audit.colTime}</Table.Th>
                      <Table.Th className="col-actor">{t.audit.colActor}</Table.Th>
                      <Table.Th>{t.audit.colAction}</Table.Th>
                      <Table.Th className="col-target">{t.audit.colTarget}</Table.Th>
                      <Table.Th className="col-result">{t.audit.colResult}</Table.Th>
                    </Table.Tr>
                  </Table.Thead>
                  <Table.Tbody>
                    {shown.map(({ e, i }) => {
                      const isOpen = open.has(i);
                      const stepTarget = e.action.startsWith('stepup-') && Object.hasOwn(t.audit.actions, e.target);
                      return (
                        <Fragment key={i}>
                          <Table.Tr className={isOpen ? 'row-open' : undefined}>
                            <Table.Td className="col-toggle">
                              <button type="button" className="row-toggle" aria-expanded={isOpen} aria-controls={`audit-d-${i}`}
                                aria-label={isOpen ? t.audit.hide : t.audit.show} onClick={() => toggle(i)}>
                                <ChevronIcon />
                              </button>
                            </Table.Td>
                            <Table.Td className="cell-muted cell-nowrap" title={e.at}>{formatDate(e.at, t)}</Table.Td>
                            <Table.Td>
                              <span className="actor">
                                {e.username === '' ? <span className="cell-muted">{t.audit.unknownActor}</span> : <span className="actor-name">{e.username}</span>}
                                {e.source !== '' && (
                                  <Badge size="xs" variant="outline" color={e.source === 'cli' ? 'orange' : 'gray'}>{labelOf(t.audit.source, e.source)}</Badge>
                                )}
                              </span>
                            </Table.Td>
                            <Table.Td className="cell-strong">{labelOf(t.audit.actions, e.action)}</Table.Td>
                            <Table.Td>
                              {e.target === '' ? <span className="cell-muted">—</span>
                                : stepTarget ? labelOf(t.audit.actions, e.target)
                                : <IdChip id={e.target} what={t.audit.colTarget} />}
                            </Table.Td>
                            <Table.Td><ResultBadge result={e.result} t={t} /></Table.Td>
                          </Table.Tr>
                          {/* Always in the table so the height can transition both ways; the content is built on
                              the first open, and `inert` keeps a closed one out of reach of keyboard and readers. */}
                          <Table.Tr className="row-detail" id={`audit-d-${i}`} data-open={isOpen ? 'true' : 'false'}>
                            <Table.Td colSpan={6}>
                              <div className="detail-reveal">
                                <div className="detail-clip" inert={!isOpen}>
                                  <div className="detail-body">{seen.has(i) && <Details e={e} t={t} />}</div>
                                </div>
                              </div>
                            </Table.Td>
                          </Table.Tr>
                        </Fragment>
                      );
                    })}
                  </Table.Tbody>
                </Table>
              </div>
            )}
            {error !== null && <Text c="red.7" fz="sm" mt="md" role="alert">{explain(error, t)}</Text>}
            {next !== '' && (
              <div className="more-row">
                <Button variant="light" loading={loading} onClick={() => void fetchPage(month, next)}>{t.audit.loadMore}</Button>
              </div>
            )}
          </>
        )}
      </section>
    </Shell>
  );
}

export function Audit() {
  return <SessionGuard>{({ me }) => <AuditBody me={me} />}</SessionGuard>;
}
