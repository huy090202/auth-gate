import { useEffect } from 'react';
import { useLocation } from 'react-router';
import { Backdrop, Brand } from './brand';
import { LangSwitch, SchemeToggle } from './controls';
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
      {header && (
        <header className="site-header glass">
          <Brand />
          <div className="header-tools">
            <LangSwitch />
            <SchemeToggle />
          </div>
        </header>
      )}
      <main>
        <AppRoutes />
      </main>
    </>
  );
}
