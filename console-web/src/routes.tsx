import { Navigate, Route, Routes } from 'react-router';
import { consoleTitle, useTitle } from './brand';
import { useStrings } from './lib/i18n';
import type { Strings } from './strings';

// Stand-ins: Tasks 13 and 14 replace each one with its screen.
function Placeholder({ screen }: { screen: keyof Strings['screens'] }) {
  const t = useStrings();
  useTitle(consoleTitle(t.screens[screen]));
  return (
    <div className="app-stage">
      <div className="app-column">
        <div className="page-head">
          <h1>{t.screens[screen]}</h1>
        </div>
      </div>
    </div>
  );
}

export function AppRoutes() {
  return (
    <Routes>
      <Route path="/" element={<Navigate to="/clients" replace />} />
      <Route path="/sign-in" element={<Placeholder screen="signIn" />} />
      <Route path="/sign-in/second-factor" element={<Placeholder screen="secondFactor" />} />
      <Route path="/enroll" element={<Placeholder screen="enroll" />} />
      <Route path="/clients" element={<Placeholder screen="clients" />} />
      <Route path="/clients/new" element={<Placeholder screen="clientNew" />} />
      <Route path="/clients/:id" element={<Placeholder screen="clientDetail" />} />
      <Route path="/audit" element={<Placeholder screen="audit" />} />
      <Route path="/account" element={<Placeholder screen="account" />} />
      <Route path="/admins" element={<Placeholder screen="admins" />} />
      <Route path="*" element={<Navigate to="/clients" replace />} />
    </Routes>
  );
}
