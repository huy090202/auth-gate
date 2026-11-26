import { useStepUp as useKitStepUp } from '@authgate/web-kit/step-up';
import type { ReactNode } from 'react';
import { api } from './lib/api';
import { explain } from './lib/explain';
import { useStrings } from './lib/i18n';
import { FactorForm } from './screens/SecondFactor';
import { passkeyCount, type Me } from './session';

export type StepUpAction =
  | 'rotate-key' | 'revoke-previous-keys' | 'disable-client' | 'delete-client' | 'purge-subject' | 'update-client'
  | 'change-password' | 'add-factor' | 'remove-factor'
  | 'admin-create' | 'admin-reset-password' | 'admin-clear-factors' | 'admin-disable' | 'admin-enable' | 'admin-role'
  | 'tenant-suspend' | 'tenant-unsuspend' | 'tenant-quota'
  | 'user-suspend' | 'user-unsuspend' | 'user-resend-verification' | 'user-factor-reset';

/**
 * Runs `run(token)` behind a fresh step-up (the modal, the single retry on step_up_required and
 * the settling are the kit's; see `useStepUp` in @authgate/web-kit/step-up). `target` is what the
 * token is bound to; omit it for the actions on the user's own account.
 */
export function useStepUp(): {
  stepUp: <T>(action: StepUpAction, run: (token: string) => Promise<T>, target?: string) => Promise<T | undefined>;
  modal: ReactNode;
} {
  const t = useStrings();
  return useKitStepUp<StepUpAction>({
    strings: { ...t.stepUp, loading: t.loading },
    explain: (e) => explain(e, t),
    api,
    loadFactors: async () => {
      const m = await api<Me>('/api/me');
      return { pin: m.factors.pin, webauthn: passkeyCount(m.factors) > 0 };
    },
    startPath: '/api/stepup/start',
    verifyPath: '/api/stepup/verify',
    renderFactorForm: (p) => <FactorForm<{ stepUpToken: string }> {...p} />,
  });
}
