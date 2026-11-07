import { Button, Checkbox, Group, Modal, Stack, Text } from '@mantine/core';
import { useState } from 'react';
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
  return (
    <Modal opened onClose={() => {}} title={title} centered size="lg" withCloseButton={false}
      closeOnEscape={false} closeOnClickOutside={false}>
      <Stack gap="md">
        <Text fz="sm" c="dimmed">{t.shownOnce.notice}</Text>
        <div>
          <Text fz="sm" fw={600} id="shown-once-label" mb={6}>{label}</Text>
          <div className="secret-box">
            <code className="secret-value" aria-labelledby="shown-once-label">{value}</code>
            <CopyButton value={value} />
          </div>
        </div>
        <Checkbox label={t.shownOnce.saved} checked={saved} onChange={(e) => setSaved(e.currentTarget.checked)} />
        <Group justify="flex-end">
          <Button disabled={!saved} onClick={onClose}>{t.shownOnce.close}</Button>
        </Group>
      </Stack>
    </Modal>
  );
}
