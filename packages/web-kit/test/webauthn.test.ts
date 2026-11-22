import { expect, test } from 'vitest';
import { serializeCredential, toCreationOptions, toRequestOptions } from '../src/lib/webauthn';
import assertion from './fixtures/assertion.json';
import attestation from './fixtures/attestation.json';
import creation from './fixtures/creation-options.json';
import request from './fixtures/request-options.json';

const decode = (s: string): ArrayBuffer => {
  const bytes = Buffer.from(s, 'base64url');
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
};

test('toCreationOptions decodes challenge, user.id and excludeCredentials ids to ArrayBuffers of the right length', () => {
  const o = toCreationOptions(creation.publicKey);
  expect(o.challenge).toBeInstanceOf(ArrayBuffer);
  expect((o.challenge as ArrayBuffer).byteLength).toBe(32);
  expect((o.user.id as ArrayBuffer).byteLength).toBe(64);
  expect((o.excludeCredentials![0]!.id as ArrayBuffer).byteLength).toBe(32);
  expect(o.rp.id).toBe('localhost');
  expect(o.pubKeyCredParams).toEqual(creation.publicKey.pubKeyCredParams);
});

test('toRequestOptions decodes challenge and allowCredentials ids', () => {
  const o = toRequestOptions(request.publicKey);
  expect((o.challenge as ArrayBuffer).byteLength).toBe(32);
  expect(new Uint8Array(o.allowCredentials![0]!.id as ArrayBuffer)).toEqual(
    new Uint8Array(decode(request.publicKey.allowCredentials[0]!.id)),
  );
  expect(o.rpId).toBe('localhost');
});

test('serializeCredential(attestation fixture as a PublicKeyCredential-like object) round-trips to the fixture JSON', () => {
  const cred = {
    id: attestation.id,
    rawId: decode(attestation.rawId),
    type: 'public-key',
    response: {
      clientDataJSON: decode(attestation.response.clientDataJSON),
      attestationObject: decode(attestation.response.attestationObject),
    },
    getClientExtensionResults: () => ({}),
  } as unknown as PublicKeyCredential;
  expect(serializeCredential(cred)).toEqual(attestation);
});

test('serializeCredential on an assertion includes userHandle null when absent', () => {
  const cred = {
    id: assertion.id,
    rawId: decode(assertion.rawId),
    type: 'public-key',
    response: {
      clientDataJSON: decode(assertion.response.clientDataJSON),
      authenticatorData: decode(assertion.response.authenticatorData),
      signature: decode(assertion.response.signature),
      userHandle: null,
    },
    getClientExtensionResults: () => ({}),
  } as unknown as PublicKeyCredential;
  const out = serializeCredential(cred) as { response: { userHandle: unknown } };
  expect(out.response.userHandle).toBeNull();
  expect(out).toEqual(assertion);
});
