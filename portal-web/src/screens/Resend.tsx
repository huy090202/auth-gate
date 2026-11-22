import { Text, TextInput } from '@mantine/core';
import { useState } from 'react';
import { api } from '../lib/api';
import { useStrings } from '../lib/i18n';
import { TurnstileForm } from './parts';

/** "Send the link again" for an address that has not confirmed its email. Always answers the same. */
export function Resend({ initialEmail }: { initialEmail: string }) {
  const t = useStrings();
  const [email, setEmail] = useState(initialEmail);
  const [sent, setSent] = useState(false);
  if (sent) return <Text fz="sm" role="status">{t.checkEmail.sent}</Text>;
  return (
    <TurnstileForm submitLabel={t.checkEmail.resend}
      onSubmit={async (turnstileToken) => {
        await api('/api/auth/resend-verification', { body: { email, turnstileToken } });
        setSent(true);
      }}>
      <TextInput label={t.common.email} type="email" value={email} onChange={(e) => setEmail(e.currentTarget.value)} required
        autoComplete="email" autoCapitalize="none" spellCheck={false} />
    </TurnstileForm>
  );
}
