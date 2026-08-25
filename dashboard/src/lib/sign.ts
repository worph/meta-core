// Client-side proof of possession for account operations.
//
// meta-core stores every private key in plaintext, so it can sign as anybody —
// which means a signature it produced proves nothing about who asked. The only
// signature that carries authority is one made where the key actually is, and
// for a browser-driven admin console that is here. meta-core enforces the other
// half by refusing to sign anything in the challenge domain
// (identity.ChallengeDomain), so this path cannot be short-circuited.
//
// The secret key entered on this page is used for exactly one signature and is
// never sent anywhere: `/api/identity/challenge` receives only the uid, and the
// request that follows carries only the challenge and its signature.
//
// Same scheme, same curve and same encodings as meta-watch's sign-in
// (packages/meta-watch/ui/js/auth.js) — deliberately, so one implementation on
// the server verifies both.

import * as secp from '@noble/secp256k1';

export type IdentityAction = 'reveal' | 'delete';

function hexToBytes(hex: string): Uint8Array | null {
  let h = hex.trim();
  if (h.startsWith('0x') || h.startsWith('0X')) h = h.slice(2);
  if (h.length !== 64 || /[^0-9a-fA-F]/.test(h)) return null;
  const out = new Uint8Array(32);
  for (let i = 0; i < 32; i++) out[i] = parseInt(h.slice(i * 2, i * 2 + 2), 16);
  return out;
}

function bytesToBase64(bytes: Uint8Array): string {
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s);
}

const B58 = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz';

/// base58btc, the multibase "z" alphabet. Spelled out rather than pulled in:
/// the alternative is a second dependency for thirty lines of arithmetic.
function base58Encode(bytes: Uint8Array): string {
  const digits: number[] = [0];
  for (const byte of bytes) {
    let carry = byte;
    for (let i = 0; i < digits.length; i++) {
      carry += digits[i] << 8;
      digits[i] = carry % 58;
      carry = (carry / 58) | 0;
    }
    while (carry) {
      digits.push(carry % 58);
      carry = (carry / 58) | 0;
    }
  }
  // Leading zero bytes encode as leading '1's rather than being dropped.
  let out = '';
  for (let i = 0; i < bytes.length && bytes[i] === 0; i++) out += B58[0];
  for (let i = digits.length - 1; i >= 0; i--) out += B58[digits[i]];
  return out;
}

/// The uid a secret key belongs to: "z" + base58btc(33-byte compressed pubkey).
/// Derived locally so a mistyped or wrong-account key is caught before any
/// request goes out. Mirrors `identityFromPrivKey` in meta-core's identity.go.
export function uidFromSecretKey(secretKeyHex: string): string | null {
  const raw = hexToBytes(secretKeyHex);
  if (!raw) return null;
  try {
    return 'z' + base58Encode(secp.getPublicKey(raw, true));
  } catch {
    return null; // not a valid scalar
  }
}

/// Whether this page can sign at all.
///
/// noble's signAsync takes its sha256 from crypto.subtle, which browsers expose
/// only in a secure context. The dashboard behind Caddy is https and fine; the
/// debug-direct port (18083) is plain http, where every signature would fail
/// with a cryptic error unless we say so first.
export function canSign(): boolean {
  return Boolean(globalThis.crypto?.subtle);
}

export const INSECURE_CONTEXT_MESSAGE =
  'This page cannot sign over a plain-http connection — browsers only expose the ' +
  'needed crypto on https (or localhost). Reach the dashboard through Caddy ' +
  '(https://metacore-dev.localhost:8083) rather than the debug-direct port.';

/// Ask meta-core for a challenge, sign it locally, and hand back the proof.
///
/// Throws with a message fit to show the user. The caller passes the proof
/// straight into the reveal/delete request body.
export async function proveOwnership(
  uid: string,
  action: IdentityAction,
  secretKeyHex: string
): Promise<{ challenge: string; signature: string }> {
  if (!canSign()) throw new Error(INSECURE_CONTEXT_MESSAGE);

  const raw = hexToBytes(secretKeyHex);
  if (!raw) {
    throw new Error('That is not a valid secret key: expected 64 hex characters.');
  }
  const derived = uidFromSecretKey(secretKeyHex);
  if (!derived) throw new Error('That is not a valid secp256k1 secret key.');
  if (derived !== uid) {
    // Caught here rather than by the server, so the message can say which
    // account the key actually is — the useful half of the answer.
    throw new Error(`That key belongs to a different account (${derived}), not this one.`);
  }

  const cRes = await fetch('/api/identity/challenge', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ uid, action }),
  });
  const cBody = await cRes.json().catch(() => ({}));
  if (!cRes.ok || !cBody.challenge) {
    throw new Error(cBody.message ?? `Could not start verification (HTTP ${cRes.status}).`);
  }

  // signAsync prehashes with sha256 and returns a 64-byte compact (r‖s)
  // signature — noble v3 dropped DER. meta-core's identity.Verify accepts both.
  const sig = await secp.signAsync(new TextEncoder().encode(cBody.challenge), raw);
  return { challenge: cBody.challenge, signature: bytesToBase64(sig) };
}
