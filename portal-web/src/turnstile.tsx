import { Text } from '@mantine/core';
import { useEffect, useRef, useState } from 'react';
import { api } from './lib/api';
import { useLang, useStrings } from './lib/i18n';

const scriptUrl = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';

type Api = {
  render(el: HTMLElement, o: Record<string, unknown>): string;
  reset(id: string): void;
  remove(id: string): void;
};
declare global {
  interface Window {
    turnstile?: Api;
  }
}

let siteKey: Promise<string> | null = null;
/** The public site key, fetched once. A failure is forgotten so the next screen tries again. */
function loadSiteKey(): Promise<string> {
  siteKey ??= api<{ turnstileSiteKey: string }>('/api/config').then(
    (c) => c.turnstileSiteKey,
    (e: unknown) => {
      siteKey = null;
      throw e;
    },
  );
  return siteKey;
}

let script: Promise<Api> | null = null;
/** The Cloudflare script, added to the page the first time a screen needs it and never before. */
function loadScript(): Promise<Api> {
  script ??= new Promise<Api>((resolve, reject) => {
    const s = document.createElement('script');
    s.src = scriptUrl;
    s.async = true;
    s.onload = () => (window.turnstile ? resolve(window.turnstile) : reject(new Error('turnstile missing')));
    s.onerror = () => {
      script = null;
      s.remove();
      reject(new Error('turnstile blocked'));
    };
    document.head.append(s);
  });
  return script;
}

/**
 * The state a form keeps for its widget: the token (empty until solved), and `reset`, which clears
 * it and makes the widget issue a new challenge. A token works once, so a form calls `reset` after
 * every submit that the backend answered.
 */
export function useTurnstile() {
  const [token, setToken] = useState('');
  const [nonce, setNonce] = useState(0);
  return {
    token,
    reset: () => {
      setToken('');
      setNonce((n) => n + 1);
    },
    props: { onToken: setToken, nonce },
  };
}

/** The widget. Renders explicitly into its own box; the token goes up through `onToken`. */
export function Turnstile({ onToken, nonce }: { onToken: (t: string) => void; nonce: number }) {
  const t = useStrings();
  const [lang] = useLang();
  const box = useRef<HTMLDivElement>(null);
  const id = useRef<string | null>(null);
  const first = useRef(nonce);
  const [failed, setFailed] = useState(false);
  const cb = useRef(onToken);
  cb.current = onToken;

  useEffect(() => {
    let live = true;
    let made: { ts: Api; id: string } | null = null;
    void Promise.all([loadSiteKey(), loadScript()]).then(
      ([key, ts]) => {
        if (!live || !box.current) return;
        const wid = ts.render(box.current, {
          sitekey: key,
          language: lang,
          theme: 'auto',
          callback: (tok: string) => cb.current(tok),
          'expired-callback': () => cb.current(''),
          'error-callback': () => cb.current(''),
        });
        made = { ts, id: wid };
        id.current = wid;
      },
      () => live && setFailed(true),
    );
    return () => {
      live = false;
      id.current = null;
      made?.ts.remove(made.id);
    };
  }, [lang]);

  useEffect(() => {
    if (nonce === first.current) return;
    if (id.current && window.turnstile) window.turnstile.reset(id.current);
  }, [nonce]);

  return (
    <div className="turnstile" role="group" aria-label={t.turnstile.label}>
      <div ref={box} />
      {failed && <Text c="red.7" fz="sm" role="alert">{t.turnstile.failed}</Text>}
    </div>
  );
}
