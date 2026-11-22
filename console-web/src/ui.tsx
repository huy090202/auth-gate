import {
  CopyButton as KitCopyButton,
  IdChip as KitIdChip,
  ConfirmDialog as KitConfirmDialog,
  InlineLoading as KitInlineLoading,
  LoadFailedInline as KitLoadFailedInline,
  TypeToConfirm as KitTypeToConfirm,
} from '@authgate/web-kit/ui';
import { explain } from './lib/explain';
import { useStrings } from './lib/i18n';

// The kit's parts take their words as props; these wrappers give them the Console's strings, so
// screens call them as they always did.
export { ChevronIcon, PageHead, StatusBadge } from '@authgate/web-kit/ui';

type Props<C extends (p: never) => unknown, K extends string> = Omit<Parameters<C>[0], K>;

export function CopyButton(props: Props<typeof KitCopyButton, 'strings'>) {
  const { shownOnce } = useStrings();
  return <KitCopyButton {...props} strings={shownOnce} />;
}

export function IdChip(props: Props<typeof KitIdChip, 'strings'>) {
  const { ui } = useStrings();
  return <KitIdChip {...props} strings={ui} />;
}

export function ConfirmDialog(props: Props<typeof KitConfirmDialog, 'cancelLabel'>) {
  const t = useStrings();
  return <KitConfirmDialog {...props} cancelLabel={t.ui.cancel} />;
}

export function TypeToConfirm(props: Props<typeof KitTypeToConfirm, 'cancelLabel' | 'typeLabel'>) {
  const t = useStrings();
  return <KitTypeToConfirm {...props} cancelLabel={t.ui.cancel} typeLabel={t.ui.typeToConfirm} />;
}

export function InlineLoading() {
  return <KitInlineLoading label={useStrings().loading} />;
}

/** A failed load inside the frame: the sidebar stays. */
export function LoadFailedInline({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const t = useStrings();
  return <KitLoadFailedInline message={explain(error, t)} retryLabel={t.common.retry} onRetry={onRetry} />;
}
