import { Anchor, Button, Group, TextInput } from '@mantine/core';
import { useState, type FormEvent } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { portalTitle, useTitle } from '../brand';
import { api } from '../lib/api';
import { parseWhole, splitOrigins } from '../lib/clients';
import { useStrings } from '../lib/i18n';
import { clientPath, clientsPath } from '../lib/tenants';
import { PageHead, ShownOnce } from '../ui';
import { ClientFields, DEFAULTS, emptyForm, whole, type FormErrors, type FormState } from './clientForm';
import { Feedback, useAction } from './forms';

export function ClientNew() {
  const t = useStrings();
  useTitle(portalTitle(t.screens.clientNew));
  const { tenantId = '' } = useParams();
  const nav = useNavigate();
  const a = useAction();
  const [form, setForm] = useState<FormState>(emptyForm);
  const [rpId, setRpId] = useState('');
  const [errors, setErrors] = useState<FormErrors>({});
  const [created, setCreated] = useState<{ clientId: string; accessKey: string } | null>(null);

  const submit = async (ev: FormEvent) => {
    ev.preventDefault();
    const e: FormErrors = {};
    const origins = splitOrigins(form.origins);
    if (form.name.trim() === '') e.name = t.clientForm.nameRequired;
    if (rpId.trim() === '') e.rpId = t.clientForm.rpIdRequired;
    if (origins.length === 0) e.origins = t.clientForm.originsRequired;
    for (const k of ['pinLength', 'pinMaxAttempts', 'pinLockout', 'tokenTtl'] as const) {
      const m = whole(form[k], t, false, k);
      if (m) e[k] = m;
    }
    setErrors(e);
    if (Object.keys(e).length > 0) return;
    const body: Record<string, unknown> = { name: form.name.trim(), rpId: rpId.trim(), origins };
    // The PIN policy goes as a whole object: a field left blank takes AuthGate's default.
    if (form.pinLength.trim() || form.pinMaxAttempts.trim() || form.pinLockout.trim()) {
      body.pinPolicy = {
        length: parseWhole(form.pinLength) ?? DEFAULTS.pinLength,
        maxAttempts: parseWhole(form.pinMaxAttempts) ?? DEFAULTS.pinMaxAttempts,
        lockoutSec: parseWhole(form.pinLockout) ?? DEFAULTS.pinLockout,
      };
    }
    if (form.tokenTtl.trim()) body.tokenTtlSec = parseWhole(form.tokenTtl);
    await a.run(async () => {
      setCreated(await api<{ clientId: string; accessKey: string }>(`/api/t/${encodeURIComponent(tenantId)}/clients`, { body }));
    });
  };

  return (
    <>
      <PageHead title={t.screens.clientNew} intro={t.clientNew.intro}
        actions={<Anchor component={Link} to={clientsPath(tenantId)} fz="sm">{t.clientDetail.back}</Anchor>} />
      <form className="panel glass" onSubmit={(ev) => void submit(ev)} noValidate>
        <ClientFields value={form} onChange={setForm} errors={errors} disabled={a.busy || created !== null} withDefaults>
          <TextInput label={t.clientForm.rpId} description={t.clientForm.rpIdHint} value={rpId} onChange={(e) => setRpId(e.currentTarget.value)}
            error={errors.rpId} disabled={a.busy || created !== null} required spellCheck={false} />
        </ClientFields>
        <Group mt="lg" justify="flex-end">
          <Button type="submit" loading={a.busy} disabled={a.waiting || created !== null}>{t.clientNew.submit}</Button>
        </Group>
        <Feedback error={a.error} />
      </form>
      {created && (
        <ShownOnce title={t.clientNew.keyTitle} label={t.clientNew.keyLabel} value={created.accessKey}
          onClose={() => nav(clientPath(tenantId, created.clientId), { replace: true })} />
      )}
    </>
  );
}
