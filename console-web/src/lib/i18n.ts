import { useCallback, useSyncExternalStore } from 'react';
import { en, vi, type Strings } from '../strings';

export type Lang = 'en' | 'vi';
const KEY = 'console.lang';
const listeners = new Set<() => void>();

function read(): Lang {
  try {
    return localStorage.getItem(KEY) === 'vi' ? 'vi' : 'en';
  } catch {
    return 'en';
  }
}

export function useLang(): [Lang, (l: Lang) => void] {
  const lang = useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => {
        listeners.delete(fn);
      };
    },
    read,
    () => 'en' as Lang,
  );
  const set = useCallback((l: Lang) => {
    try {
      localStorage.setItem(KEY, l);
    } catch {
      /* storage blocked: the choice lasts until reload */
    }
    listeners.forEach((fn) => fn());
  }, []);
  return [lang, set];
}

export function useStrings(): Strings {
  return useLang()[0] === 'vi' ? vi : en;
}

/** A sentence for a failure: the backend's code when it has one, else the generic one. */
export function describe(e: unknown, t: Strings): string {
  const code = typeof e === 'object' && e !== null && 'code' in e ? (e as { code: unknown }).code : undefined;
  if (code === 'network_error') return t.networkError;
  if (typeof code === 'string' && Object.hasOwn(t.errors, code)) return t.errors[code as keyof Strings['errors']];
  return t.errors.internal;
}

/** Fills `{name}` placeholders. Strings stay plain text so the typed strings module can hold them. */
export function fmt(template: string, values: Record<string, string | number>): string {
  return template.replace(/\{(\w+)\}/g, (m, k: string) => (Object.hasOwn(values, k) ? String(values[k]) : m));
}
