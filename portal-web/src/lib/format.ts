import { formatDate as formatDateAt } from '@authgate/web-kit/format';
import type { Strings } from '../strings';

/** A timestamp in the reader's language and time zone. An unreadable value is shown as it came. */
export const formatDate = (iso: string, t: Strings): string => formatDateAt(iso, t.locale);
