import { Button, Checkbox, Group, Modal, Stack, Text } from '@mantine/core';
import { useEffect, useRef, useState } from 'react';
import { CopyButton } from './ui';
import { useStrings } from './lib/i18n';

/**
 * A value that can never be read again (an access key, a temporary password). The dialog cannot be
 * dismissed by Escape, a click outside or a close icon; Close stays off until the user ticks that
 * they saved the value. The value is plain text and lives only as long as the caller keeps this mounted.
 */
export function ShownOnce({ title, label, value, onClose }: { title: string; label: string; value: string; onClose(): void }) {
  const t = useStrings();
  const [saved, setSaved] = useState(false);
  const guarded = useRef(true);

  // The value lives only in this component's state, so leaving the page loses it. A reload or a tab
  // close asks first; Back is absorbed by a same-URL history entry that is pushed again on every
  // popstate (the path never changes, so the route stays mounted).
  useEffect(() => {
    const isSentinel = () => (history.state as { shownOnce?: boolean } | null)?.shownOnce === true;
    const push = () => history.pushState({ ...(history.state as object | null), shownOnce: true }, '');
    // StrictMode mounts twice: do not stack two entries.
    if (!isSentinel()) push();
    const onUnload = (e: BeforeUnloadEvent) => e.preventDefault();
    const onPop = () => {
      if (guarded.current) push();
    };
    window.addEventListener('beforeunload', onUnload);
    window.addEventListener('popstate', onPop);
    return () => {
      window.removeEventListener('beforeunload', onUnload);
      window.removeEventListener('popstate', onPop);
    };
  }, []);

  /** Closing removes the sentinel entry first, so the next Back does not land on the same page. */
  const close = () => {
    guarded.current = false;
    const state = history.state as { shownOnce?: boolean } | null;
    if (state?.shownOnce !== true) return onClose();
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      window.removeEventListener('popstate', finish);
      onClose();
    };
    window.addEventListener('popstate', finish);
    window.setTimeout(finish, 500);
    history.back();
  };

  return (
    <Modal opened onClose={() => {}} title={title} centered size="lg" withCloseButton={false}
      closeOnEscape={false} closeOnClickOutside={false}>
      <Stack gap="md">
        <Text fz="sm" c="dimmed">{t.shownOnce.notice}</Text>
        <div>
          <Text fz="sm" fw={600} id="shown-once-label" mb={6}>{label}</Text>
          <div className="secret-box">
            <code className="secret-value" aria-labelledby="shown-once-label">{value}</code>
            <CopyButton value={value} autoFocus />
          </div>
        </div>
        <Checkbox label={t.shownOnce.saved} checked={saved} onChange={(e) => setSaved(e.currentTarget.checked)} />
        <Group justify="flex-end">
          <Button disabled={!saved} onClick={close}>{t.shownOnce.close}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}
