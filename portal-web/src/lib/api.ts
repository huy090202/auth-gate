import { createApi } from '@authgate/web-kit/api';

// Every code the Portal backend can send: its own, and the AuthGate ones it passes through.
export const errorCodes = [
  'unauthorized', 'invalid_credentials', 'second_factor_required', 'enrollment_required', 'email_unverified',
  'turnstile_required', 'turnstile_failed', 'turnstile_unavailable', 'invalid_email', 'invalid_name', 'invalid_password',
  'password_breached', 'invalid_lang', 'invalid_token', 'step_up_required', 'invalid_action', 'rate_limited',
  'unsupported_media_type', 'not_found', 'invalid_json', 'wrong_password', 'last_factor', 'factor_not_enrolled',
  'authgate_unavailable', 'internal', 'verification_failed', 'locked', 'challenge_gone', 'wrong_factor', 'pin_rejected',
  'registration_failed', 'invalid_factor', 'enrollment_in_progress', 'invalid_target', 'busy', 'conflict',
  // Account, workspace and client calls.
  'invalid_display_name', 'invalid_avatar', 'email_taken', 'forbidden', 'invalid_id', 'invalid_cursor',
  'tenant_deleting', 'tenant_suspended', 'quota_exceeded', 'client_not_found', 'invalid_client', 'disabled_by_operator',
] as const;
export type ErrorCode = (typeof errorCodes)[number];

const client = createApi(errorCodes);
export const { api, ApiError, onApiFailure } = client;
export type ApiError = InstanceType<typeof ApiError>;
