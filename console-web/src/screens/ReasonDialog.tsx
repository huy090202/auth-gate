import { Button, Group, Modal, Stack, Textarea } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { fmt, useStrings } from '../lib/i18n';

const MAX = 200;

/**
 * Asks for the internal reason of a suspension: 1 to 200 characters after trimming. The send button
 * stays off while the trimmed text is empty; `onConfirm` gets the trimmed text. The step-up comes after.
 */
export function ReasonDialog({ title, label, confirmLabel, onConfirm, onCancel }: {
  title: string; label: string; confirmLabel: string; onConfirm(reason: string): void; onCancel(): void;
}) {
  const t = useStrings();
  const [text, setText] = useState('');
  const reason = text.trim();
  const submit = (ev: FormEvent) => {
    ev.preventDefault();
    if (reason !== '') onConfirm(reason);
  };
  return (
    <Modal opened onClose={onCancel} title={title} centered>
      <form onSubmit={submit}>
        <Stack gap="md">
          <Textarea label={label} description={t.reasonDialog.hint} value={text} maxLength={MAX} autosize minRows={3} maxRows={6}
            data-autofocus onChange={(e) => setText(e.currentTarget.value)} />
          <Group justify="space-between" gap="sm">
            <span className="cell-muted" aria-live="polite">{fmt(t.reasonDialog.count, { n: [...text].length })}</span>
            <Group gap="sm">
              <Button variant="default" onClick={onCancel}>{t.ui.cancel}</Button>
              <Button type="submit" color="red" disabled={reason === ''}>{confirmLabel}</Button>
            </Group>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
