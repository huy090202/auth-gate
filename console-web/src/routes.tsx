import { Navigate, Route, Routes } from 'react-router';
import { Account } from './screens/Account';
import { Admins } from './screens/Admins';
import { Audit } from './screens/Audit';
import { ClientDetail } from './screens/ClientDetail';
import { ClientNew } from './screens/ClientNew';
import { Clients } from './screens/Clients';
import { Enroll } from './screens/Enroll';
import { SecondFactor } from './screens/SecondFactor';
import { SignIn } from './screens/SignIn';
import { TenantDetail } from './screens/TenantDetail';
import { Tenants } from './screens/Tenants';
import { Users } from './screens/Users';
import { SessionProvider } from './session';

export function AppRoutes() {
  return (
    <SessionProvider>
      <Routes>
        <Route path="/" element={<Navigate to="/clients" replace />} />
        <Route path="/sign-in" element={<SignIn />} />
        <Route path="/sign-in/second-factor" element={<SecondFactor />} />
        <Route path="/enroll" element={<Enroll />} />
        <Route path="/clients" element={<Clients />} />
        <Route path="/clients/new" element={<ClientNew />} />
        <Route path="/clients/:id" element={<ClientDetail />} />
        <Route path="/tenants" element={<Tenants />} />
        <Route path="/tenants/:id" element={<TenantDetail />} />
        <Route path="/users" element={<Users />} />
        <Route path="/audit" element={<Audit />} />
        <Route path="/account" element={<Account />} />
        <Route path="/admins" element={<Admins />} />
        <Route path="*" element={<Navigate to="/clients" replace />} />
      </Routes>
    </SessionProvider>
  );
}
