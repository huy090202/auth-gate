/**
 * Reads `token` from the URL fragment once and takes the fragment out of the address bar, so the
 * token is not kept in the session history or in a copied URL. Returns '' when there is none.
 */
export function takeFragmentToken(): string {
  const token = new URLSearchParams(window.location.hash.replace(/^#/, '')).get('token') ?? '';
  if (window.location.hash) history.replaceState(history.state, '', window.location.pathname + window.location.search);
  return token;
}
