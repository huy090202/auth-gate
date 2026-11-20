// base64url <-> ArrayBuffer for WebAuthn options and credentials. Nothing else.
// The JSON shapes are the ones go-webauthn emits and parses.

export function fromB64url(s: string): ArrayBuffer {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4);
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

export function toB64url(buf: ArrayBuffer | ArrayBufferView): string {
  // ArrayBuffer.isView, not instanceof ArrayBuffer: buffers from another realm (iframes,
  // test runners) fail instanceof.
  const bytes = ArrayBuffer.isView(buf)
    ? new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength)
    : new Uint8Array(buf, 0, buf.byteLength);
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

type Desc = { id: string; type: string; transports?: string[] };
const descs = (list?: Desc[]) =>
  list?.map((d) => ({ ...d, id: fromB64url(d.id) }) as PublicKeyCredentialDescriptor);

export function toCreationOptions(publicKey: unknown): PublicKeyCredentialCreationOptions {
  const pk = publicKey as Record<string, unknown> & { challenge: string; user: { id: string }; excludeCredentials?: Desc[] };
  return {
    ...pk,
    challenge: fromB64url(pk.challenge),
    user: { ...pk.user, id: fromB64url(pk.user.id) },
    excludeCredentials: descs(pk.excludeCredentials),
  } as PublicKeyCredentialCreationOptions;
}

export function toRequestOptions(publicKey: unknown): PublicKeyCredentialRequestOptions {
  const pk = publicKey as Record<string, unknown> & { challenge: string; allowCredentials?: Desc[] };
  return {
    ...pk,
    challenge: fromB64url(pk.challenge),
    allowCredentials: descs(pk.allowCredentials),
  } as PublicKeyCredentialRequestOptions;
}

export function serializeCredential(cred: PublicKeyCredential): object {
  const r = cred.response as AuthenticatorResponse & Partial<AuthenticatorAttestationResponse & AuthenticatorAssertionResponse>;
  const response: Record<string, string | string[] | null> = { clientDataJSON: toB64url(r.clientDataJSON) };
  if (r.attestationObject) {
    response.attestationObject = toB64url(r.attestationObject);
    if (typeof r.getTransports === 'function') response.transports = r.getTransports();
  } else {
    response.authenticatorData = toB64url(r.authenticatorData!);
    response.signature = toB64url(r.signature!);
    response.userHandle = r.userHandle ? toB64url(r.userHandle) : null;
  }
  const out: Record<string, unknown> = {
    id: cred.id,
    rawId: toB64url(cred.rawId),
    type: cred.type,
    response,
    clientExtensionResults: cred.getClientExtensionResults(),
  };
  if (cred.authenticatorAttachment) out.authenticatorAttachment = cred.authenticatorAttachment;
  return out;
}
