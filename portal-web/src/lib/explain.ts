import { formatDate } from './format';
import { describe, fmt } from './i18n';
import type { Strings } from '../strings';

/**
 * describe(), plus what the upstream error offers a user to act on: attempts left, when a lock
 * ends, how long to wait. Values come from the server and are rendered as text only.
 */
export function explain(e: unknown, t: Strings): string {
  const parts = [describe(e, t)];
  const extras = typeof e === 'object' && e !== null && 'extras' in e ? (e as { extras: Record<string, unknown> }).extras : {};
  if (typeof extras.attemptsRemaining === 'number') parts.push(fmt(t.common.attemptsLeft, { n: extras.attemptsRemaining }));
  if (typeof extras.lockedUntil === 'string') parts.push(fmt(t.common.lockedUntil, { time: formatDate(extras.lockedUntil, t) }));
  if (typeof extras.retryAfter === 'number') parts.push(fmt(t.common.retryIn, { n: extras.retryAfter }));
  return parts.join(' ');
}

/** Seconds the server asked us to wait, or 0. */
export function waitSeconds(e: unknown): number {
  const v = typeof e === 'object' && e !== null && 'extras' in e ? (e as { extras: Record<string, unknown> }).extras.retryAfter : undefined;
  return typeof v === 'number' && v > 0 ? Math.min(Math.ceil(v), 120) : 0;
}
