import { useEffect } from 'react';
import { useLocation } from 'react-router';
import { Backdrop } from './brand';
import { SchemeToggle, SiteHeader } from './controls';
import { useLang } from './lib/i18n';
import { AppRoutes } from './routes';
import { ToastProvider } from './toast';

export { SchemeToggle };

/** Signed-in screens bring their own frame (the sidebar shell); every other screen keeps the top header. */
const framed = ['/account', '/t'];

export function App() {
  const [lang] = useLang();
  const { pathname } = useLocation();
  useEffect(() => {
    document.documentElement.lang = lang;
  }, [lang]);
  const header = !framed.some((p) => pathname === p || pathname.startsWith(p + '/'));
  return (
    <ToastProvider>
      <Backdrop />
      {header && <SiteHeader />}
      <main>
        <AppRoutes />
      </main>
    </ToastProvider>
  );
}
