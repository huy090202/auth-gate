import { Navigate, Route, Routes } from 'react-router';
import { Account } from './screens/Account';
import { CheckEmail } from './screens/CheckEmail';
import { Enroll } from './screens/Enroll';
import { FactorReset } from './screens/FactorReset';
import { Forgot } from './screens/Forgot';
import { Recovery } from './screens/Recovery';
import { ResetPassword } from './screens/ResetPassword';
import { SecondFactor } from './screens/SecondFactor';
import { SignIn } from './screens/SignIn';
import { SignUp } from './screens/SignUp';
import { VerifyEmail } from './screens/VerifyEmail';
import { SessionProvider } from './session';

export function AppRoutes() {
  return (
    <SessionProvider>
      <Routes>
        <Route path="/" element={<Navigate to="/account" replace />} />
        <Route path="/sign-up" element={<SignUp />} />
        <Route path="/sign-up/check-email" element={<CheckEmail />} />
        <Route path="/verify-email" element={<VerifyEmail />} />
        <Route path="/sign-in" element={<SignIn />} />
        <Route path="/sign-in/second-factor" element={<SecondFactor />} />
        <Route path="/sign-in/recovery" element={<Recovery />} />
        <Route path="/enroll" element={<Enroll />} />
        <Route path="/forgot" element={<Forgot />} />
        <Route path="/reset-password" element={<ResetPassword />} />
        <Route path="/factor-reset" element={<FactorReset />} />
        <Route path="/account" element={<Account />} />
        <Route path="*" element={<Navigate to="/account" replace />} />
      </Routes>
    </SessionProvider>
  );
}
