/**
 * The one spelling of a recovery code, the same as normaliseCode in internal/portal/recovery.go:
 * upper case, spaces and dashes dropped, then the dash put back in the middle. Anything that is not
 * ten characters long keeps no dash, so it matches nothing.
 */
export function normaliseCode(s: string): string {
  const c = s.toUpperCase().replaceAll(' ', '').replaceAll('-', '');
  return c.length === 10 ? `${c.slice(0, 5)}-${c.slice(5)}` : c;
}

/** The codes as a text file body, one per line. */
export const codesText = (codes: string[]): string => codes.join('\n') + '\n';
