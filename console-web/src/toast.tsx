import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useStrings } from './lib/i18n';

export type ToastKind = 'success' | 'info';
type Item = { id: number; message: string; kind: ToastKind; leaving: boolean };
type ToastFn = (message: string, kind?: ToastKind) => void;

// Without a provider (a screen rendered alone in a test) a toast is dropped.
const Ctx = createContext<ToastFn>(() => {});

/** `toast(message, kind)` shows a short note at the top right. Success and info only: errors stay inline. */
export const useToast = (): ToastFn => useContext(Ctx);

const LIFETIME = 4000;
const EXIT = 200; // a little over the 180 ms exit animation in styles.css

const stroke = { width: 18, height: 18, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 1.8, strokeLinecap: 'round', strokeLinejoin: 'round', 'aria-hidden': true, focusable: false } as const;
const SuccessIcon = () => <svg {...stroke}><circle cx="12" cy="12" r="9" /><path d="m8 12.5 3 3 5-6" /></svg>;
const InfoIcon = () => <svg {...stroke}><circle cx="12" cy="12" r="9" /><path d="M12 11v5M12 8h.01" /></svg>;
const CloseIcon = () => <svg {...stroke} width={16} height={16}><path d="m6 6 12 12M18 6 6 18" /></svg>;

function ToastItem({ item, onLeave, onGone }: { item: Item; onLeave: (id: number) => void; onGone: (id: number) => void }) {
  const t = useStrings();
  const [paused, setPaused] = useState(false);
  const { id, leaving } = item;

  // The timer restarts from the full 4 s when the pointer or focus leaves.
  useEffect(() => {
    if (leaving || paused) return;
    const h = setTimeout(() => onLeave(id), LIFETIME);
    return () => clearTimeout(h);
  }, [leaving, paused, id, onLeave]);
  useEffect(() => {
    if (!leaving) return;
    const h = setTimeout(() => onGone(id), EXIT);
    return () => clearTimeout(h);
  }, [leaving, id, onGone]);

  return (
    <div className="toast glass" data-kind={item.kind} data-leaving={leaving ? 'true' : undefined}
      onMouseEnter={() => setPaused(true)} onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)} onBlur={() => setPaused(false)}>
      <span className="toast-icon">{item.kind === 'success' ? <SuccessIcon /> : <InfoIcon />}</span>
      <span className="toast-text">{item.message}</span>
      <button type="button" className="toast-close" aria-label={t.toast.dismiss} onClick={() => onLeave(id)}><CloseIcon /></button>
    </div>
  );
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Item[]>([]);
  const seq = useRef(0);
  const toast = useCallback<ToastFn>((message, kind = 'success') => {
    if (message === '') return;
    setItems((prev) => [...prev, { id: ++seq.current, message, kind, leaving: false }]);
  }, []);
  const leave = useCallback((id: number) => setItems((prev) => prev.map((i) => (i.id === id ? { ...i, leaving: true } : i))), []);
  const gone = useCallback((id: number) => setItems((prev) => prev.filter((i) => i.id !== id)), []);
  const value = useMemo(() => toast, [toast]);
  return (
    <Ctx.Provider value={value}>
      {children}
      <div className="toast-stack" role="status" aria-live="polite">
        {items.map((i) => <ToastItem key={i.id} item={i} onLeave={leave} onGone={gone} />)}
      </div>
    </Ctx.Provider>
  );
}
