import { Button, Checkbox, Group, Stack, Text } from '@mantine/core';
import { useEffect, useState } from 'react';
import { useStrings } from '../lib/i18n';
import { codesText } from '../lib/recovery';
import { Card } from './parts';

/**
 * The ten codes, shown once. They live only in the props of this screen (never in storage), a reload
 * asks first, and Continue stays off until the person ticks that they saved them.
 */
export function RecoveryCodes({ codes, onContinue }: { codes: string[]; onContinue: () => void }) {
  const t = useStrings();
  const [saved, setSaved] = useState(false);
  const [note, setNote] = useState('');

  useEffect(() => {
    const onUnload = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener('beforeunload', onUnload);
    return () => window.removeEventListener('beforeunload', onUnload);
  }, []);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(codesText(codes));
      setNote(t.recoveryCodes.copied);
    } catch {
      setNote(t.recoveryCodes.copyFailed);
    }
  };
  const download = () => {
    const url = URL.createObjectURL(new Blob([codesText(codes)], { type: 'text/plain' }));
    const a = document.createElement('a');
    a.href = url;
    a.download = t.recoveryCodes.fileName;
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  return (
    <Card title={t.screens.recoveryCodes}>
      <Text fz="sm">{t.recoveryCodes.intro}</Text>
      <Text fz="sm" c="dimmed">{t.recoveryCodes.notice}</Text>
      <ol className="code-grid" aria-label={t.recoveryCodes.list}>
        {codes.map((c) => <li key={c}><code>{c}</code></li>)}
      </ol>
      <Group gap="xs">
        <Button variant="default" onClick={() => void copy()}>{t.recoveryCodes.copyAll}</Button>
        <Button variant="default" onClick={download}>{t.recoveryCodes.download}</Button>
        <Text fz="sm" role="status" aria-live="polite">{note}</Text>
      </Group>
      <Stack gap="md">
        <Checkbox label={t.recoveryCodes.saved} checked={saved} onChange={(e) => setSaved(e.currentTarget.checked)} />
        <Button disabled={!saved} onClick={onContinue}>{t.recoveryCodes.continue}</Button>
      </Stack>
    </Card>
  );
}
