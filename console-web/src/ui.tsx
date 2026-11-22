import { ActionIcon, Badge, Button, Group, Loader, Modal, Stack, Text, TextInput } from '@mantine/core';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import { explain } from './lib/explain';
import { fmt, useStrings } from './lib/i18n';
import { useToast } from './toast';

const stroke = { width: 16, height: 16, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 1.8, strokeLinecap: 'round', strokeLinejoin: 'round', 'aria-hidden': true, focusable: false } as const;
const CopyIcon = () => <svg {...stroke}><rect x="9" y="9" width="11" height="11" rx="2" /><path d="M5 15V6a2 2 0 0 1 2-2h9" /></svg>;
const CheckIcon = () => <svg {...stroke}><path d="m5 12.5 4.5 4.5L19 7.5" /></svg>;
export const ChevronIcon = () => <svg {...stroke}><path d="m9 6 6 6-6 6" /></svg>;

/** Copies `value` and says so for two seconds. A blocked clipboard is reported, never silent. */
export function CopyButton({ value, label, autoFocus }: { value: string; label?: string; autoFocus?: boolean }) {
  const t = useStrings();
  const [state, setState] = useState<'idle' | 'done' | 'failed'>('idle');
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setState('done');
    } catch {
      setState('failed');
    }
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setState('idle'), 2000);
  };
  const text = state === 'done' ? t.shownOnce.copied : t.shownOnce.copy;
  return (
    <>
      <Button size="compact-sm" variant="light" leftSection={state === 'done' ? <CheckIcon /> : <CopyIcon />} onClick={() => void copy()}
        aria-label={label} data-autofocus={autoFocus ? true : undefined}>
        {text}
      </Button>
      {state === 'failed' && <Text fz="xs" c="red.7" role="alert">{t.shownOnce.copyFailed}</Text>}
    </>
  );
}

/**
 * An identifier in monospace: cut off with an ellipsis when the column is narrow (the whole value is
 * the tooltip and what the copy button copies).
 */
export function IdChip({ id, what }: { id: string; what: string }) {
  const t = useStrings();
  const toast = useToast();
  const [done, setDone] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const copy = () => {
    void navigator.clipboard.writeText(id).then(
      () => {
        setDone(true);
        toast(t.ui.copied); // the icon alone is easy to miss
        clearTimeout(timer.current);
        timer.current = setTimeout(() => setDone(false), 2000);
      },
      () => {},
    );
  };
  return (
    <span className="id-chip">
      <code className="id-text" title={id}>{id}</code>
      <ActionIcon variant="subtle" color="gray" size="sm" onClick={copy} aria-label={fmt(done ? t.ui.copied : t.ui.copyId, { what })}>
        {done ? <CheckIcon /> : <CopyIcon />}
      </ActionIcon>
    </span>
  );
}

export function StatusBadge({ active, on, off }: { active: boolean; on: string; off: string }) {
  return <Badge variant="light" size="md" color={active ? 'canopy' : 'gray'}>{active ? on : off}</Badge>;
}

/** A page heading with its intro, and optional actions on the right. */
export function PageHead({ title, intro, meta, actions }: { title: string; intro?: string; meta?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="page-head page-head-row">
      <div>
        <h1>{title}</h1>
        {intro && <Text c="dimmed" fz="sm" mt={4}>{intro}</Text>}
        {meta && <div className="page-meta">{meta}</div>}
      </div>
      {actions && <div className="page-actions">{actions}</div>}
    </div>
  );
}

/** A yes/no step before a destructive action; the consequence is spelled out in `body`. */
export function ConfirmDialog({ title, body, confirmLabel, danger, onConfirm, onCancel }: {
  title: string; body: string; confirmLabel: string; danger?: boolean; onConfirm(): void; onCancel(): void;
}) {
  const t = useStrings();
  return (
    <Modal opened onClose={onCancel} title={title} centered>
      <Stack gap="lg">
        <Text fz="sm">{body}</Text>
        <Group justify="flex-end" gap="sm">
          {/* The safe answer takes the focus: Enter cancels, a destructive confirm needs a deliberate move. */}
          <Button variant="default" onClick={onCancel} data-autofocus>{t.ui.cancel}</Button>
          <Button color={danger ? 'red' : undefined} onClick={onConfirm}>{confirmLabel}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

/** Like ConfirmDialog, but the confirm button stays off until `expected` is typed exactly (case and spaces count). */
export function TypeToConfirm({ title, body, expected, confirmLabel, onConfirm, onCancel }: {
  title: string; body: string; expected: string; confirmLabel: string; onConfirm(): void; onCancel(): void;
}) {
  const t = useStrings();
  const [typed, setTyped] = useState('');
  return (
    <Modal opened onClose={onCancel} title={title} centered>
      <Stack gap="lg">
        <Text fz="sm">{body}</Text>
        <TextInput label={fmt(t.ui.typeToConfirm, { expected })} value={typed} onChange={(e) => setTyped(e.currentTarget.value)}
          spellCheck={false} autoComplete="off" data-autofocus />
        <Group justify="flex-end" gap="sm">
          <Button variant="default" onClick={onCancel}>{t.ui.cancel}</Button>
          <Button color="red" disabled={typed !== expected} onClick={onConfirm}>{confirmLabel}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}

export function InlineLoading() {
  const t = useStrings();
  return <div className="inline-loading"><Loader size="sm" aria-label={t.loading} /></div>;
}

/** A failed load inside the frame: the sidebar stays. */
export function LoadFailedInline({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const t = useStrings();
  return (
    <section className="panel glass">
      <Text c="red.7" fz="sm" role="alert">{explain(error, t)}</Text>
      <Button mt="md" size="sm" variant="light" onClick={onRetry}>{t.common.retry}</Button>
    </section>
  );
}
