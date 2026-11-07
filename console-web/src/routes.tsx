import { Navigate, Route, Routes } from 'react-router';
import { consoleTitle, useTitle } from './brand';
import { useStrings } from './lib/i18n';
import { Account } from './screens/Account';
import { Enroll } from './screens/Enroll';
import { SecondFactor } from './screens/SecondFactor';
import { SignIn } from './screens/SignIn';
import { SessionGuard, SessionProvider } from './session';
import { Shell } from './shell';
import type { Strings } from './strings';

// Stand-ins: Task 14 replaces each one with its screen. They already sit behind the guard.
function Placeholder({ screen, role }: { screen: keyof Strings['screens']; role?: 'master' }) {
  const t = useStrings();
  useTitle(consoleTitle(t.screens[screen]));
  return (
    <SessionGuard role={role}>
      {({ me }) => (
        <Shell me={me}>
          <div className="page-head">
            <h1>{t.screens[screen]}</h1>
          </div>
        </Shell>
      )}
    </SessionGuard>
  );
}

export function AppRoutes() {
  return (
    <SessionProvider>
      <Routes>
        <Route path="/" element={<Navigate to="/clients" replace />} />
        <Route path="/sign-in" element={<SignIn />} />
        <Route path="/sign-in/second-factor" element={<SecondFactor />} />
        <Route path="/enroll" element={<Enroll />} />
        <Route path="/clients" element={<Placeholder screen="clients" />} />
        <Route path="/clients/new" element={<Placeholder screen="clientNew" />} />
        <Route path="/clients/:id" element={<Placeholder screen="clientDetail" />} />
        <Route path="/audit" element={<Placeholder screen="audit" />} />
        <Route path="/account" element={<Account />} />
        <Route path="/admins" element={<Placeholder screen="admins" role="master" />} />
        <Route path="*" element={<Navigate to="/clients" replace />} />
      </Routes>
    </SessionProvider>
  );
}
