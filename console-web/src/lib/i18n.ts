import { createI18n, fmt, type Lang } from '@authgate/web-kit/i18n';
import { en, vi, type Strings } from '../strings';

export { fmt, type Lang };
// The key is the one the Console always used, so saved language choices survive.
export const { useLang, useStrings } = createI18n<Strings>('console.lang', { en, vi });

/** A sentence for a failure: the backend's code when it has one, else the generic one. */
export function describe(e: unknown, t: Strings): string {
  const code = typeof e === 'object' && e !== null && 'code' in e ? (e as { code: unknown }).code : undefined;
  if (code === 'network_error') return t.networkError;
  if (typeof code === 'string' && Object.hasOwn(t.errors, code)) return t.errors[code as keyof Strings['errors']];
  return t.errors.internal;
}
