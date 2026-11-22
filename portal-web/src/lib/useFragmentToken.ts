import { useState } from 'react';
import { useLocation } from 'react-router';
import { takeFragmentToken } from './fragment';

let taken: { key: string; token: string } | null = null;

/**
 * The token of an emailed link, read once per history entry. StrictMode runs initialisers twice,
 * and the first read removes the fragment, so the second must get the same answer from memory.
 */
export function useFragmentToken(): string {
  const { key } = useLocation();
  const [token] = useState(() => {
    if (taken?.key === key) return taken.token;
    taken = { key, token: takeFragmentToken() };
    return taken.token;
  });
  return token;
}
