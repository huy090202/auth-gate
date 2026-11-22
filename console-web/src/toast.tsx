import { ToastProvider as KitToastProvider } from '@authgate/web-kit/toast';
import type { ReactNode } from 'react';
import { useStrings } from './lib/i18n';

export { useToast, type ToastKind } from '@authgate/web-kit/toast';

export function ToastProvider({ children }: { children: ReactNode }) {
  return <KitToastProvider strings={useStrings().toast}>{children}</KitToastProvider>;
}
