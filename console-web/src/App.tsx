import { useEffect } from 'react';
import { useLocation } from 'react-router';
import { Backdrop } from './brand';
import { SchemeToggle, SiteHeader } from './controls';
import { useLang } from './lib/i18n';
import { AppRoutes } from './routes';

export { SchemeToggle };

/** Screens without the sidebar (signed out, second factor, enrolling) keep the top header. */
const headerPaths = ['/sign-in', '/enroll'];

export function App() {
  const [lang] = useLang();
  const { pathname } = useLocation();
  useEffect(() => {
    document.documentElement.lang = lang;
  }, [lang]);
  const header = headerPaths.some((p) => pathname === p || pathname.startsWith(p + '/'));
  return (
    <>
      <Backdrop />
      {header && <SiteHeader />}
      <main>
        <AppRoutes />
      </main>
    </>
  );
}
