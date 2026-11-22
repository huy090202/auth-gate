import {
  LangSwitch as KitLangSwitch,
  SchemeToggle as KitSchemeToggle,
  SiteHeader as KitSiteHeader,
} from '@authgate/web-kit/controls';
import { Brand } from './brand';
import { useLang, useStrings } from './lib/i18n';

export function SchemeToggle() {
  return <KitSchemeToggle strings={useStrings()} />;
}

export function LangSwitch() {
  const [lang, setLang] = useLang();
  return <KitLangSwitch lang={lang} onChange={setLang} label={useStrings().language} />;
}

/** The plain header of screens without the sidebar: brand, language, theme. */
export function SiteHeader() {
  return (
    <KitSiteHeader brand={<Brand />}>
      <LangSwitch />
      <SchemeToggle />
    </KitSiteHeader>
  );
}
