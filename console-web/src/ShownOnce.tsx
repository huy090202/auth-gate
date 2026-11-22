import { ShownOnce as KitShownOnce } from '@authgate/web-kit/shown-once';
import { useStrings } from './lib/i18n';

export function ShownOnce(props: { title: string; label: string; value: string; onClose(): void }) {
  return <KitShownOnce {...props} strings={useStrings().shownOnce} />;
}
