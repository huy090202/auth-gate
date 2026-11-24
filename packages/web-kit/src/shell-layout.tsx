import { Anchor, Drawer, Menu } from '@mantine/core';
import { useMediaQuery } from '@mantine/hooks';
import { useEffect, useState, type ElementType, type ReactNode } from 'react';

export type ShellStrings = { mainNav: string; expandSidebar: string; collapseSidebar: string; collapse: string; openMenu: string; closeMenu: string };

function readCollapsed(key: string): boolean {
  try {
    return localStorage.getItem(key) === 'collapsed';
  } catch {
    return false;
  }
}

/** One sidebar link. `component` is the router's NavLink; the kit does not depend on a router. */
export function NavItem({ component, to, label, icon }: { component: ElementType; to: string; label: string; icon: ReactNode }) {
  const Link = Anchor as ElementType;
  return (
    <Link component={component} to={to} className="nav-link" fz="sm" underline="never" title={label}>
      {icon}
      <span className="nav-label">{label}</span>
    </Link>
  );
}

/** Ends the session: the logout call may fail (a session that is already gone is what we wanted), then `after` runs. */
export async function endSession(logout: () => Promise<unknown>, after: () => void): Promise<void> {
  try {
    await logout();
  } catch {
    /* already gone */
  }
  after();
}

/**
 * The frame of every signed-in, full-stage screen: a sticky sidebar (brand, optional switcher,
 * navigation, collapse), a sticky header with the account controls, and a centred content
 * container. The sidebar can collapse to an icon rail on wide screens; below 768 px the sidebar gives way to a
 * header menu button and a navigation drawer. The app fills the slots; `storageKey` keeps the collapse choice per app.
 */
export function ShellLayout({ storageKey, strings, brand, switcher, nav, collapseIcon, menuIcon, routeKey, headerStart, account, collapsed: initial, children }: {
  storageKey: string;
  strings: ShellStrings;
  brand: ReactNode;
  /** Between the brand and the navigation (the Portal's workspace switcher). */
  switcher?: ReactNode;
  nav: ReactNode;
  collapseIcon: ReactNode;
  /** The phone header's menu button. */
  menuIcon: ReactNode;
  /** The current route (the app's pathname): a change closes the drawer. */
  routeKey: string;
  /** Header controls before the account menu. */
  headerStart?: ReactNode;
  account: {
    avatar: ReactNode; name: string; buttonLabel: string;
    /** The dropdown: Menu.Label and Menu.Item entries. */
    menu: ReactNode;
  };
  collapsed?: boolean;
  children: ReactNode;
}) {
  const [collapsed, setCollapsed] = useState(initial ?? readCollapsed(storageKey));
  const toggle = () => {
    const next = !collapsed;
    setCollapsed(next);
    try {
      localStorage.setItem(storageKey, next ? 'collapsed' : 'expanded');
    } catch {
      /* storage blocked: the choice lasts until reload */
    }
  };
  const [drawer, setDrawer] = useState(false);
  const wide = useMediaQuery('(min-width: 769px)');
  useEffect(() => setDrawer(false), [routeKey, wide]);
  const label = collapsed ? strings.expandSidebar : strings.collapseSidebar;
  return (
    <div className="shell">
      <aside className="sidebar glass" data-collapsed={collapsed}>
        <div className="sidebar-brand">{brand}</div>
        {switcher}
        <nav className="sidebar-nav" aria-label={strings.mainNav}>{nav}</nav>
        <div className="sidebar-foot">
          <button type="button" className="sidebar-collapse" onClick={toggle} aria-expanded={!collapsed} aria-label={label} title={label}>
            {collapseIcon}
            <span className="nav-label">{strings.collapse}</span>
          </button>
        </div>
      </aside>
      <div className="shell-main">
        <header className="content-header glass">
          <button type="button" className="header-menu" onClick={() => setDrawer(true)} aria-label={strings.openMenu} aria-expanded={drawer}>
            {menuIcon}
          </button>
          <div className="header-brand">{brand}</div>
          <div className="header-start">{headerStart}</div>
          <Menu position="bottom-end" width={220} withinPortal={false}>
            <Menu.Target>
              <button type="button" className="avatar-btn" aria-label={account.buttonLabel}>
                {account.avatar}
                <span className="avatar-name">{account.name}</span>
              </button>
            </Menu.Target>
            <Menu.Dropdown>{account.menu}</Menu.Dropdown>
          </Menu>
        </header>
        <Drawer opened={drawer} onClose={() => setDrawer(false)} position="left" size="min(320px, 85vw)" title={brand}
          closeButtonProps={{ 'aria-label': strings.closeMenu }} classNames={{ body: 'drawer-body', close: 'drawer-close' }}>
          {switcher}
          <nav className="drawer-nav" aria-label={strings.mainNav} onClick={(e) => { if (e.button === 0 && !(e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) && (e.target as Element).closest('a')) setDrawer(false); }}>{nav}</nav>
          <div className="drawer-tools">{headerStart}</div>
        </Drawer>
        <div className="shell-content">
          <div className="shell-container">{children}</div>
        </div>
      </div>
    </div>
  );
}
