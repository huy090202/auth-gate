import { Button, Group, Modal, Stack, TextInput } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router';
import { api } from '../lib/api';
import { useStrings } from '../lib/i18n';
import { membersPath, useWorkspaces } from '../lib/tenants';
import { Feedback, useAction } from './forms';

/** The "New organisation" dialog. On success the shell's list is reloaded and the new Members page opens. */
export function NewOrg({ onClose }: { onClose: () => void }) {
  const t = useStrings();
  const navigate = useNavigate();
  const { reload } = useWorkspaces();
  const [name, setName] = useState('');
  const create = useAction();

  const submit = (ev: FormEvent) => {
    ev.preventDefault();
    void create.run(async () => {
      const r = await api<{ tenantId: string }>('/api/tenants', { method: 'POST', body: { name: name.trim() } });
      // The list first: the new page reads the user's role in it.
      await reload();
      onClose();
      void navigate(membersPath(r.tenantId));
    }, t.newOrg.created);
  };

  return (
    <Modal opened onClose={onClose} title={t.newOrg.title} centered>
      <form onSubmit={submit} noValidate>
        <Stack gap="md">
          <TextInput label={t.newOrg.name} description={t.newOrg.hint} value={name} maxLength={64} autoComplete="off"
            onChange={(e) => setName(e.currentTarget.value)} disabled={create.busy} data-autofocus />
          <Feedback error={create.error} />
          <Group justify="flex-end" gap="sm">
            <Button variant="default" onClick={onClose}>{t.ui.cancel}</Button>
            <Button type="submit" loading={create.busy} disabled={create.waiting || name.trim() === ''}>{t.newOrg.create}</Button>
          </Group>
        </Stack>
      </form>
    </Modal>
  );
}
