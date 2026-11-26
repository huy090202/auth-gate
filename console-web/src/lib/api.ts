import { createApi } from '@authgate/web-kit/api';

// Every code the Console backend can send: its own, and the AuthGate ones it passes through.
export const errorCodes = [
  'unauthorized', 'invalid_credentials', 'second_factor_required', 'enrollment_required', 'already_verified',
  'forbidden', 'step_up_required', 'invalid_action', 'rate_limited', 'unsupported_media_type', 'not_found',
  'invalid_json', 'invalid_username', 'invalid_password', 'wrong_password', 'username_taken', 'last_factor',
  'factor_not_enrolled', 'cannot_modify_self', 'last_master', 'own_client_protected', 'invalid_month',
  'authgate_unavailable', 'internal', 'verification_failed', 'locked', 'challenge_gone', 'wrong_factor',
  'pin_rejected', 'registration_failed', 'invalid_client', 'invalid_cursor', 'invalid_id', 'client_not_found',
  'rotate_conflict', 'invalid_token', 'invalid_factor', 'enrollment_in_progress', 'invalid_role', 'invalid_target', 'busy',
  // The operator pages: tenants, users and the avatar.
  'invalid_reason', 'invalid_owner', 'not_mailable', 'tenant_deleting', 'tenant_suspended', 'invalid_quota', 'invalid_email',
  'invalid_avatar', 'portal_unavailable',
] as const;
export type ErrorCode = (typeof errorCodes)[number];

const client = createApi(errorCodes);
export const { api, ApiError, onApiFailure } = client;
export type ApiError = InstanceType<typeof ApiError>;
