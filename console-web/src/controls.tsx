import { ActionIcon, SegmentedControl, useComputedColorScheme, useMantineColorScheme } from '@mantine/core';
import { Brand } from './brand';
import { useLang, useStrings, type Lang } from './lib/i18n';

const icon = { width: 18, height: 18, fill: 'none', stroke: 'currentColor', strokeWidth: 1.8, strokeLinecap: 'round', strokeLinejoin: 'round' } as const;

export function SchemeToggle() {
  const t = useStrings();
  // keepTransitions: Mantine would otherwise append a <style> element on every toggle, which the CSP blocks.
  const { setColorScheme } = useMantineColorScheme({ keepTransitions: true });
  const scheme = useComputedColorScheme('light');
  const next = scheme === 'dark' ? 'light' : 'dark';
  return (
    <ActionIcon variant="default" size="lg" radius="md" onClick={() => setColorScheme(next)}
      aria-label={next === 'dark' ? t.switchToDark : t.switchToLight}>
      {next === 'dark' ? (
        <svg viewBox="0 0 24 24" {...icon} aria-hidden="true"><path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5Z" /></svg>
      ) : (
        <svg viewBox="0 0 24 24" {...icon} aria-hidden="true">
          <circle cx="12" cy="12" r="4" />
          <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
        </svg>
      )}
    </ActionIcon>
  );
}

export function LangSwitch() {
  const [lang, setLang] = useLang();
  const t = useStrings();
  return (
    <SegmentedControl size="xs" radius="md" value={lang} onChange={(v) => setLang(v as Lang)} aria-label={t.language}
      data={[{ label: 'EN', value: 'en' }, { label: 'VI', value: 'vi' }]} />
  );
}

/** The plain header of screens without the sidebar: brand, language, theme. */
export function SiteHeader() {
  return (
    <header className="site-header glass">
      <Brand />
      <div className="header-tools">
        <LangSwitch />
        <SchemeToggle />
      </div>
    </header>
  );
}
