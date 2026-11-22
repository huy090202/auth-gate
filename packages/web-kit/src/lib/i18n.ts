import { useCallback, useSyncExternalStore } from 'react';

export type Lang = 'en' | 'vi';

/**
 * One language store per app. `storageKey` is the localStorage key of the saved choice, so an app
 * keeps its users' saved language when it adopts the kit. `strings` holds both languages.
 */
export function createI18n<S>(storageKey: string, strings: { en: S; vi: S }) {
  const listeners = new Set<() => void>();

  function read(): Lang {
    try {
      return localStorage.getItem(storageKey) === 'vi' ? 'vi' : 'en';
    } catch {
      return 'en';
    }
  }

  function useLang(): [Lang, (l: Lang) => void] {
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
        localStorage.setItem(storageKey, l);
      } catch {
        /* storage blocked: the choice lasts until reload */
      }
      listeners.forEach((fn) => fn());
    }, []);
    return [lang, set];
  }

  function useStrings(): S {
    return useLang()[0] === 'vi' ? strings.vi : strings.en;
  }

  return { useLang, useStrings };
}

/** Fills `{name}` placeholders. Strings stay plain text so a typed strings module can hold them. */
export function fmt(template: string, values: Record<string, string | number>): string {
  return template.replace(/\{(\w+)\}/g, (m, k: string) => (Object.hasOwn(values, k) ? String(values[k]) : m));
}
