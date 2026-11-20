import type { Strings } from '../strings';

/** A timestamp in the reader's language and time zone. An unreadable value is shown as it came. */
export function formatDate(iso: string, t: Strings): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return new Intl.DateTimeFormat(t.locale, { dateStyle: 'medium', timeStyle: 'short' }).format(d);
}
