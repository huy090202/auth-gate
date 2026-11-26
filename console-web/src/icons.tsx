import type { ReactNode } from 'react';

// Small line icons in the style of brand.tsx: presentation attributes, no library, decorative.
const base = { width: 20, height: 20, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', strokeWidth: 1.8, strokeLinecap: 'round', strokeLinejoin: 'round', 'aria-hidden': true, focusable: false } as const;
const icon = (d: ReactNode) => <svg {...base}>{d}</svg>;

export const ClientsIcon = () => icon(<><rect x="3" y="4" width="18" height="6" rx="2" /><rect x="3" y="14" width="18" height="6" rx="2" /><path d="M7 7h.01M7 17h.01" /></>);
export const TenantsIcon = () => icon(<><path d="M4 21V7l8-4 8 4v14" /><path d="M9 21v-6h6v6M8 10h.01M12 10h.01M16 10h.01" /></>);
export const UsersIcon = () => icon(<><circle cx="12" cy="8" r="3.5" /><path d="M5 20a7 7 0 0 1 14 0" /><path d="M19.5 6.5l1.5 1.5 2.5-2.5" /></>);
export const AuditIcon = () => icon(<><path d="M8 6h12M8 12h12M8 18h12" /><path d="M4 6h.01M4 12h.01M4 18h.01" /></>);
export const AdminsIcon = () => icon(<><circle cx="9" cy="8" r="3.5" /><path d="M2.5 20a6.5 6.5 0 0 1 13 0" /><path d="M16 4.6a3.5 3.5 0 0 1 0 6.8M18 14.2a6.5 6.5 0 0 1 3.5 5.8" /></>);
export const AccountIcon = () => icon(<><circle cx="12" cy="8" r="4" /><path d="M4 21a8 8 0 0 1 16 0" /></>);
export const PanelLeftIcon = () => icon(<><rect x="3" y="4" width="18" height="16" rx="3" /><path d="M9 4v16" /></>);
export const MenuIcon = () => icon(<path d="M4 6h16M4 12h16M4 18h16" />);
export const SignOutIcon = () => icon(<><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" /><path d="m16 17 5-5-5-5M21 12H9" /></>);
