import { Navigate, Outlet, Route, Routes } from 'react-router';
import { Account } from './screens/Account';
import { Audit } from './screens/Audit';
import { CheckEmail } from './screens/CheckEmail';
import { ClientDetail } from './screens/ClientDetail';
import { ClientNew } from './screens/ClientNew';
import { Clients } from './screens/Clients';
import { ConfirmEmail } from './screens/ConfirmEmail';
import { Enroll } from './screens/Enroll';
import { FactorReset } from './screens/FactorReset';
import { Forgot } from './screens/Forgot';
import { Invite } from './screens/Invite';
import { Members } from './screens/Members';
import { OrgSettings } from './screens/OrgSettings';
import { Recovery } from './screens/Recovery';
import { ResetPassword } from './screens/ResetPassword';
import { SecondFactor } from './screens/SecondFactor';
import { SignIn } from './screens/SignIn';
import { SignUp } from './screens/SignUp';
import { VerifyEmail } from './screens/VerifyEmail';
import { SessionGuard, SessionProvider } from './session';
import { Shell } from './shell';
import { pendingInvite } from './lib/invite';
import { clientsPath } from './lib/tenants';

/** The signed-in frame: a full session only, with the sidebar and the workspace switcher around the screen. */
function Frame() {
  return <SessionGuard>{({ me }) => <Shell me={me}><Outlet /></Shell>}</SessionGuard>;
}

/** `/` opens the personal workspace's clients, or the invite the user left to sign in. */
function Home() {
  return (
    <SessionGuard>
      {({ me }) => {
        const p = pendingInvite();
        return <Navigate to={p ? `/invite/${encodeURIComponent(p.tenantId)}` : clientsPath(me.personalTenantId)} replace />;
      }}
    </SessionGuard>
  );
}

export function AppRoutes() {
  return (
    <SessionProvider>
      <Routes>
        <Route path="/" element={<Home />} />
        <Route path="/sign-up" element={<SignUp />} />
        <Route path="/sign-up/check-email" element={<CheckEmail />} />
        <Route path="/verify-email" element={<VerifyEmail />} />
        <Route path="/confirm-email" element={<ConfirmEmail />} />
        <Route path="/sign-in" element={<SignIn />} />
        <Route path="/sign-in/second-factor" element={<SecondFactor />} />
        <Route path="/sign-in/recovery" element={<Recovery />} />
        <Route path="/enroll" element={<Enroll />} />
        <Route path="/forgot" element={<Forgot />} />
        <Route path="/reset-password" element={<ResetPassword />} />
        <Route path="/factor-reset" element={<FactorReset />} />
        <Route path="/invite/:tenantId" element={<Invite />} />
        <Route element={<Frame />}>
          <Route path="/account" element={<Account />} />
          <Route path="/t/:tenantId/clients" element={<Clients />} />
          <Route path="/t/:tenantId/clients/new" element={<ClientNew />} />
          <Route path="/t/:tenantId/clients/:id" element={<ClientDetail />} />
          <Route path="/t/:tenantId/audit" element={<Audit />} />
          <Route path="/t/:tenantId/members" element={<Members />} />
          <Route path="/t/:tenantId/settings" element={<OrgSettings />} />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </SessionProvider>
  );
}
