import { useEffect, useRef, useState } from 'react';

import {
  INSECURE_CONTEXT_MESSAGE,
  canSign,
  proveOwnership,
  uidFromSecretKey,
  type IdentityAction,
} from '../lib/sign';

// Admin console for meta-core's account keystore.
//
// meta-core stores one secp256k1 keypair per file under
// {META_CORE_PATH}/identity/accounts/<uid>.json and has NO notion of a current
// user — which account is "you" is decided by whichever client signs in
// (meta-watch). So this page lists accounts; it never speaks of an active one.
//
// The private key is returned by exactly two calls: POST /api/identity/generate
// (once, at creation) and POST /api/identity/reveal (on demand, for an account
// that already exists). Both land in the same exclusive SecretKeyPanel.
//
// Reveal and remove both require the account's own secret key, entered here and
// used to sign a one-shot challenge locally — the key itself is never sent. So
// this page can enumerate and create, but it holds no authority over an
// existing account that its operator does not already have the key for. There
// is deliberately no admin override: on this node the private key *is* the
// account.

interface Account {
  uid: string;
  curve: string;
  createdAt: number;
}

// What one account holds in the User Data Layer. Absent from
// /api/udl/users/stats means zero, not unknown.
interface Stats {
  records: number;
  cids: number;
  keys: number;
}

// A secret on screen. Carries no createdAt: /api/identity/reveal answers with
// {uid, curve, privateKeyHex} only.
interface SecretKeyView {
  uid: string;
  privateKeyHex: string;
  kind: 'generated' | 'revealed';
}

// Which row has an open confirmation, and for what. One discriminated value
// rather than a pair of ids, so at most one confirm panel can ever be open.
interface Pending {
  uid: string;
  kind: 'remove' | 'reveal';
}

// Both confirmable operations map to a server-side action name, and the
// challenge is bound to it: a challenge minted to reveal a key cannot delete
// the account. Keeping the mapping in one place stops the two drifting.
const ACTION_FOR: Record<Pending['kind'], IdentityAction> = {
  reveal: 'reveal',
  remove: 'delete',
};

// meta-watch's sentinel cell for profile records: display name and avatar are
// about the *user*, not a content file, so they live at (uid, "self", ...).
// meta-core stores them opaquely and knows nothing of the convention.
const PROFILE_CID = 'self';
const PROFILE_NAME_KEY = 'profile:name';

const cardStyle: React.CSSProperties = {
  background: '#16213e',
  borderRadius: '8px',
  padding: '1.5rem',
  marginBottom: '1rem',
};

const buttonStyle: React.CSSProperties = {
  padding: '0.5rem 1rem',
  background: '#0f3460',
  color: '#fff',
  border: 'none',
  borderRadius: '4px',
  cursor: 'pointer',
  marginRight: '0.5rem',
};

const dangerButtonStyle: React.CSSProperties = {
  ...buttonStyle,
  background: '#7f1d1d',
};

const inputStyle: React.CSSProperties = {
  width: '100%',
  padding: '0.5rem',
  marginBottom: '0.5rem',
  background: '#1a1a2e',
  border: '1px solid #0f3460',
  borderRadius: '4px',
  color: '#fff',
  fontFamily: 'monospace',
};

const monoStyle: React.CSSProperties = {
  fontFamily: 'monospace',
  fontSize: '0.85rem',
  wordBreak: 'break-all',
  background: '#0a0a18',
  border: '1px solid #0f3460',
  padding: '0.75rem',
  borderRadius: '4px',
};

const dangerPanelStyle: React.CSSProperties = {
  background: '#1f0a0a',
  border: '1px solid #7f1d1d',
  padding: '0.75rem 1rem',
  borderRadius: '4px',
  marginTop: '0.75rem',
};

const errorTextStyle: React.CSSProperties = {
  color: '#f87171',
  background: '#1f0a0a',
  padding: '0.5rem 0.75rem',
  borderRadius: '4px',
};

function formatCreatedAt(unixSec?: number): string {
  if (!unixSec) return '';
  try {
    return new Date(unixSec * 1000).toISOString().replace('T', ' ').replace(/\..+$/, ' UTC');
  } catch {
    return String(unixSec);
  }
}

// Read the JSON body of a non-2xx response if present, fall back to status text.
async function errorMessage(res: Response, fallback: string): Promise<string> {
  try {
    const j = await res.json();
    if (j && (j.message || j.error)) return j.message ?? j.error;
  } catch {
    // not JSON
  }
  return `${fallback} (HTTP ${res.status})`;
}

// Copy text, falling back to selecting the element that displays it.
//
// The fallback is load-bearing, not politeness: the dashboard is served over
// plain HTTP, where navigator.clipboard is undefined — without it the copy
// buttons do nothing at all on a real deployment.
async function copyToClipboard(text: string, fallbackElementId: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const sel = window.getSelection();
    const el = document.getElementById(fallbackElementId);
    if (sel && el) {
      const range = document.createRange();
      range.selectNodeContents(el);
      sel.removeAllRanges();
      sel.addRange(range);
    }
  }
}

async function fetchAccounts(): Promise<Account[]> {
  const res = await fetch('/api/identity/accounts');
  if (!res.ok) throw new Error(await errorMessage(res, 'Failed to load accounts'));
  const data = (await res.json()) as { accounts?: Account[] };
  // Backend order is CreatedAt ascending then uid (identity.List) — stable, so
  // render it as given rather than re-sorting here.
  return data.accounts ?? [];
}

// Resolve an account's display name from the User Data Layer, or null.
//
// Never throws and never reports an error upward: a name is decoration on top
// of the uid, and the account list must render identically whether Redis is up
// (identity itself is file-backed, the UDL is not). The name is also *unsigned*
// — meta-core stores the plaintext beside the signed record and verifies
// nothing — so the uid stays on screen as the real identity, and the length cap
// bounds what a hostile writer can do to the layout.
async function fetchDisplayName(uid: string): Promise<string | null> {
  const url =
    `/api/udl/record?uid=${encodeURIComponent(uid)}` +
    `&cid=${encodeURIComponent(PROFILE_CID)}` +
    `&key=${encodeURIComponent(PROFILE_NAME_KEY)}`;
  try {
    const res = await fetch(url);
    if (!res.ok) return null; // 503 when storage is down, 500, …
    const data = (await res.json()) as { exists?: boolean; value?: unknown };
    // Key off `value`, not `exists`: clearing a name writes a tombstone, which
    // leaves the cell existing with no plaintext. Private-tier cells look the
    // same, which is exactly right — there is nothing to show for either.
    if (typeof data.value !== 'string') return null;
    const name = data.value.trim();
    return name ? name.slice(0, 64) : null;
  } catch {
    return null;
  }
}

// How much each account holds. One call for the whole page, not one per row:
// meta-core answers it from a keyspace SCAN (there is no per-user cid index),
// so per-row calls would walk the keyspace once per account.
//
// Never throws, for the same reason fetchDisplayName does not: the counts are
// context on top of the uid, and the list must render identically when Redis is
// down — accounts are file-backed, their data is not.
//
// Returns null on failure rather than an empty map. The difference is
// load-bearing: an empty map renders as "no stored data" against every account,
// which inside a delete confirmation is a false statement about what is at
// stake. Unknown has to look like unknown.
async function fetchStats(): Promise<Record<string, Stats> | null> {
  try {
    const res = await fetch('/api/udl/users/stats');
    if (!res.ok) return null;
    const data = (await res.json()) as { users?: Record<string, Stats> };
    return data.users ?? {};
  } catch {
    return null;
  }
}

// The secret-key box shared by the reveal and remove confirmations.
//
// Separate from the surrounding panel so both operations ask the same way, and
// so the key lives in this component's state rather than the page's — it is
// discarded with the panel instead of lingering in a parent that outlives it.
function SecretKeyConfirm({
  label,
  actionLabel,
  busy,
  onConfirm,
  onCancel,
}: {
  label: string;
  actionLabel: string;
  busy: boolean;
  onConfirm: (secretKeyHex: string) => void;
  onCancel: () => void;
}) {
  const [secret, setSecret] = useState('');
  const signable = canSign();
  // Derived locally, purely to tell the operator they have the wrong key before
  // they hit the button. The server would refuse either way.
  const derived = secret.trim() ? uidFromSecretKey(secret) : null;
  const mismatch = Boolean(secret.trim() && derived && derived !== label);

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        onConfirm(secret.trim());
      }}
    >
      <p style={{ color: '#fecaca', margin: '0 0 0.5rem' }}>
        Paste this account's secret key to prove it is yours. It is used to sign a one-time
        challenge <em>in this browser</em> and is never sent to the server.
      </p>
      {!signable && <p style={errorTextStyle}>{INSECURE_CONTEXT_MESSAGE}</p>}
      <input
        style={inputStyle}
        type="password"
        autoComplete="off"
        placeholder="64 hex characters"
        value={secret}
        onChange={(e) => setSecret(e.target.value)}
        disabled={busy || !signable}
        autoFocus
      />
      {mismatch && (
        <p style={{ ...errorTextStyle, marginTop: 0 }}>
          That key belongs to a different account ({derived}).
        </p>
      )}
      <button
        type="submit"
        style={dangerButtonStyle}
        disabled={busy || !signable || !secret.trim() || mismatch}
      >
        {busy ? 'Verifying…' : actionLabel}
      </button>
      <button type="button" style={buttonStyle} onClick={onCancel} disabled={busy}>
        Cancel
      </button>
    </form>
  );
}

// The one place a private key is ever displayed. Rendered exclusively (the
// account list is hidden while it is open) so the "save it now" contract of a
// freshly generated key is not competing with anything, and so the hardcoded
// element id below stays unique.
function SecretKeyPanel({ data, onDismiss }: { data: SecretKeyView; onDismiss: () => void }) {
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    await copyToClipboard(data.privateKeyHex, 'mm-privkey-box');
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <div style={{ ...cardStyle, border: '2px solid #dc2626' }}>
      <div
        style={{
          background: '#7f1d1d',
          color: '#fee2e2',
          padding: '0.75rem 1rem',
          borderRadius: '4px',
          marginBottom: '1rem',
        }}
      >
        {data.kind === 'generated' ? (
          <>
            <strong>This private key is shown ONCE.</strong> Save it now in a password manager.
            You can ask for it again later with <em>Reveal private key</em>, but only for as long
            as this account exists on this node — delete it and the key is gone for good.
          </>
        ) : (
          <>
            <strong>This is the live private key for an existing account.</strong> Anyone holding
            it can sign as this uid forever, on any machine. There is no revocation and no
            rotation. Do not paste it into chat, tickets or screenshots.
          </>
        )}
      </div>
      <p style={{ color: '#9ca3af', margin: '0 0 0.5rem' }}>uid (public):</p>
      <div style={monoStyle}>{data.uid}</div>
      <p style={{ color: '#9ca3af', margin: '1rem 0 0.5rem' }}>privateKeyHex (secret):</p>
      <div id="mm-privkey-box" style={{ ...monoStyle, background: '#1f0a0a', borderColor: '#7f1d1d' }}>
        {data.privateKeyHex}
      </div>
      <div style={{ marginTop: '1rem' }}>
        <button style={buttonStyle} onClick={copy}>
          {copied ? 'Copied' : 'Copy private key'}
        </button>
        <button style={dangerButtonStyle} onClick={onDismiss}>
          {data.kind === 'generated' ? 'I have saved it' : 'Done — hide it'}
        </button>
      </div>
    </div>
  );
}

// Create / import. Always available, not just on an empty keystore: this is an
// admin console, so adding an account to a populated node has to be possible.
function NewAccountPanel({
  onGenerated,
  onImported,
}: {
  onGenerated: (s: SecretKeyView) => void;
  onImported: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [importing, setImporting] = useState(false);
  const [importHex, setImportHex] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const generate = async () => {
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const res = await fetch('/api/identity/generate', { method: 'POST' });
      if (!res.ok) {
        setError(await errorMessage(res, 'Generate failed'));
        return;
      }
      const data = (await res.json()) as { uid: string; privateKeyHex: string };
      onGenerated({ uid: data.uid, privateKeyHex: data.privateKeyHex, kind: 'generated' });
    } catch (e) {
      setError(`Generate failed: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setBusy(false);
    }
  };

  const importKey = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const res = await fetch('/api/identity/import', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ privateKeyHex: importHex.trim() }),
      });
      if (!res.ok) {
        setError(await errorMessage(res, 'Import failed'));
        return;
      }
      // Import is idempotent — presenting the key IS the proof of ownership, so
      // a key this node already holds is a success, not a conflict. `created`
      // only decides the wording.
      const data = (await res.json()) as { created?: boolean };
      setNotice(
        data.created
          ? 'Imported — a new account was added to this keystore.'
          : 'That key was already in this keystore; nothing changed.'
      );
      setImportHex('');
      setImporting(false);
      onImported();
    } catch (e) {
      setError(`Import failed: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div style={cardStyle}>
      <h3 style={{ marginTop: 0 }}>Add an account</h3>
      <p style={{ color: '#9ca3af', marginTop: 0 }}>
        Generating mints a fresh secp256k1 keypair here. Importing adds a key created elsewhere —
        it is also how an existing account is moved onto this node, so importing a key the
        keystore already holds succeeds and changes nothing.
      </p>
      {error && <p style={errorTextStyle}>{error}</p>}
      {notice && (
        <p style={{ color: '#9ca3af', background: '#0a0a18', padding: '0.5rem 0.75rem', borderRadius: '4px' }}>
          {notice}
        </p>
      )}
      <div>
        <button style={buttonStyle} disabled={busy} onClick={generate}>
          {busy ? 'Working…' : 'Generate new keypair'}
        </button>
        <button
          style={buttonStyle}
          disabled={busy}
          onClick={() => {
            setImporting((x) => !x);
            setError(null);
          }}
        >
          {importing ? 'Cancel import' : 'Import existing private key'}
        </button>
      </div>
      {importing && (
        <form onSubmit={importKey} style={{ marginTop: '1rem' }}>
          <p style={{ color: '#9ca3af', margin: '0 0 0.5rem' }}>
            64 hex chars (32-byte secp256k1 scalar). A "0x" prefix is fine.
          </p>
          <input
            style={inputStyle}
            placeholder="e.g. a3f1…"
            value={importHex}
            onChange={(e) => setImportHex(e.target.value)}
            required
            autoFocus
          />
          <button type="submit" style={buttonStyle} disabled={busy || !importHex.trim()}>
            {busy ? 'Importing…' : 'Import'}
          </button>
        </form>
      )}
    </div>
  );
}

function AccountCard({
  account,
  name,
  stats,
  busy,
  disabled,
  pending,
  error,
  onAsk,
  onCancel,
  onReveal,
  onRemove,
}: {
  account: Account;
  name?: string;
  // undefined means "counted, and it holds nothing"; null means "could not be
  // counted". The two must not read the same on a confirmation screen.
  stats?: Stats | null;
  busy: boolean;
  disabled: boolean;
  pending: Pending | null;
  error: string | null;
  onAsk: (p: Pending) => void;
  onCancel: () => void;
  onReveal: (uid: string, secretKeyHex: string) => void;
  onRemove: (uid: string, secretKeyHex: string) => void;
}) {
  const [copied, setCopied] = useState(false);
  const uidBoxId = `mm-uid-${account.uid}`;
  const label = name ?? account.uid;
  const counted = stats !== null;
  const records = stats?.records ?? 0;
  const titles = stats?.cids ?? 0;

  const copyUid = async () => {
    await copyToClipboard(account.uid, uidBoxId);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  return (
    <div style={cardStyle}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '1rem' }}>
        <div style={{ minWidth: 0, flex: 1 }}>
          <h3 style={{ margin: '0 0 0.5rem', color: name ? '#fff' : '#888' }}>
            {name ?? 'Unnamed account'}
          </h3>
          <div id={uidBoxId} style={monoStyle}>
            {account.uid}
          </div>
          <p style={{ color: '#888', margin: '0.75rem 0 0', fontSize: '0.85rem' }}>
            Curve: {account.curve} · Created: {formatCreatedAt(account.createdAt)}
          </p>
          <p style={{ color: '#888', margin: '0.25rem 0 0', fontSize: '0.85rem' }}>
            {/* An account that has never been used legitimately holds nothing, so
                zero is a real answer — but only once the count actually ran. */}
            {!counted
              ? 'Stored data: could not be counted (storage unavailable)'
              : records === 0
                ? 'No stored data'
                : `${records.toLocaleString()} stored ${records === 1 ? 'record' : 'records'}` +
                  ` across ${titles.toLocaleString()} ${titles === 1 ? 'title' : 'titles'}`}
          </p>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem', flexShrink: 0 }}>
          <button style={{ ...buttonStyle, marginRight: 0 }} onClick={copyUid} disabled={disabled}>
            {copied ? 'Copied' : 'Copy uid'}
          </button>
          <button
            style={{ ...dangerButtonStyle, marginRight: 0 }}
            onClick={() => onAsk({ uid: account.uid, kind: 'reveal' })}
            disabled={disabled}
          >
            Reveal private key
          </button>
          <button
            style={{ ...dangerButtonStyle, marginRight: 0 }}
            onClick={() => onAsk({ uid: account.uid, kind: 'remove' })}
            disabled={disabled}
          >
            Remove
          </button>
        </div>
      </div>

      {error && <p style={{ ...errorTextStyle, marginBottom: 0 }}>{error}</p>}

      {pending?.uid === account.uid && pending.kind === 'reveal' && (
        <div style={dangerPanelStyle}>
          <p style={{ color: '#fecaca', marginTop: 0 }}>
            Show the private key for <strong>{label}</strong>?
          </p>
          <p style={{ color: '#fecaca', marginTop: 0 }}>
            The key <em>is</em> the account. Anyone who copies it can sign records as this uid on
            any machine, forever — there is no way to revoke or rotate it afterwards. Because
            proving ownership already requires the key, this only confirms that the copy this node
            holds matches yours.
          </p>
          <SecretKeyConfirm
            label={account.uid}
            actionLabel="Show the private key"
            busy={busy}
            onConfirm={(secret) => onReveal(account.uid, secret)}
            onCancel={onCancel}
          />
        </div>
      )}

      {pending?.uid === account.uid && pending.kind === 'remove' && (
        <div style={dangerPanelStyle}>
          <p style={{ color: '#fecaca', marginTop: 0 }}>
            Remove <strong>{label}</strong> from this node?
          </p>
          <ul style={{ color: '#fecaca', paddingLeft: '1.2rem', margin: '0 0 0.75rem' }}>
            <li>
              <strong>The key is gone for good.</strong> The uid <em>is</em> the public key, and
              the private half exists only in this node's keystore. If nobody saved it, nothing —
              not you, not meta-core — can ever sign as this uid again. Reveal and copy it first
              if there is any doubt.
            </li>
            <li>
              <strong>Nothing is retracted.</strong> Records already signed under this uid keep
              verifying on every peer that replicated them. Deleting a key is not a recall.
            </li>
            <li>
              <strong>Its data is destroyed, not orphaned.</strong>{' '}
              {!counted
                ? 'This account\u2019s records could not be counted because storage is unavailable — ' +
                  'so meta-core will refuse this delete rather than remove the key and strand them.'
                : records === 0
                  ? 'This account holds no User Data Layer records, so there is nothing to purge.'
                  : `All ${records.toLocaleString()} User Data Layer ${
                      records === 1 ? 'record' : 'records'
                    } — likes, ratings, watch progress across ${titles.toLocaleString()} ${
                      titles === 1 ? 'title' : 'titles'
                    } — are permanently deleted from Redis, along with every index that names this uid.`}{' '}
              This cannot be undone and there is no backup.
            </li>
            <li>
              <strong>This signs nobody out elsewhere.</strong> Any device that holds this key
              still holds it, and can import it back here at any time.
            </li>
          </ul>
          <SecretKeyConfirm
            label={account.uid}
            actionLabel="Remove this account and all its data"
            busy={busy}
            onConfirm={(secret) => onRemove(account.uid, secret)}
            onCancel={onCancel}
          />
        </div>
      )}
    </div>
  );
}

export default function Identity() {
  const [accounts, setAccounts] = useState<Account[] | null>(null);
  const [names, setNames] = useState<Record<string, string>>({});
  const [stats, setStats] = useState<Record<string, Stats> | null>({});
  const [notice, setNotice] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [fetchError, setFetchError] = useState<string | null>(null);
  const [secret, setSecret] = useState<SecretKeyView | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const [busyUid, setBusyUid] = useState<string | null>(null);
  const [rowError, setRowError] = useState<{ uid: string; message: string } | null>(null);

  // Bumped per refresh so a slow response from a superseded load cannot
  // overwrite a newer one. The page does not poll (unlike the rest of the
  // dashboard) — a key list has no business changing under the cursor.
  const reqGen = useRef(0);

  const refresh = async () => {
    const gen = ++reqGen.current;
    setLoading(true);
    setFetchError(null);
    let list: Account[];
    try {
      list = await fetchAccounts();
    } catch (e) {
      if (gen !== reqGen.current) return;
      setFetchError(e instanceof Error ? e.message : String(e));
      setLoading(false);
      return;
    }
    if (gen !== reqGen.current) return;
    // Render the list before resolving names, so a down Redis delays nothing.
    setAccounts(list);
    setLoading(false);

    // Names and counts both come after the list is on screen, and neither can
    // fail the page: a down Redis costs decoration, not the account list.
    const [settled, counts] = await Promise.all([
      Promise.allSettled(list.map((a) => fetchDisplayName(a.uid))),
      fetchStats(),
    ]);
    if (gen !== reqGen.current) return;
    const next: Record<string, string> = {};
    settled.forEach((r, i) => {
      if (r.status === 'fulfilled' && r.value) next[list[i].uid] = r.value;
    });
    // Replace rather than merge, so a removed account's name and counts go with
    // it instead of lingering against a uid that no longer exists.
    setNames(next);
    setStats(counts);
  };

  useEffect(() => {
    refresh();
  }, []);

  const reveal = async (uid: string, secretKeyHex: string) => {
    setBusyUid(uid);
    setRowError(null);
    try {
      const proof = await proveOwnership(uid, ACTION_FOR.reveal, secretKeyHex);
      // POST with a body rather than ?uid=, so neither the target nor the
      // signature reaches the proxy's access log. The handler accepts the uid
      // either way; the proof must be in the body.
      const res = await fetch('/api/identity/reveal', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ confirm: true, uid, ...proof }),
      });
      if (!res.ok) {
        setRowError({ uid, message: await errorMessage(res, 'Reveal failed') });
        return;
      }
      const data = (await res.json()) as { uid: string; privateKeyHex: string };
      setPending(null);
      setSecret({ uid: data.uid, privateKeyHex: data.privateKeyHex, kind: 'revealed' });
    } catch (e) {
      setRowError({ uid, message: `Reveal failed: ${e instanceof Error ? e.message : String(e)}` });
    } finally {
      setBusyUid(null);
    }
  };

  const remove = async (uid: string, secretKeyHex: string) => {
    setBusyUid(uid);
    setRowError(null);
    setNotice(null);
    try {
      const proof = await proveOwnership(uid, ACTION_FOR.remove, secretKeyHex);
      // The uid is mandatory: the signature authorises one specific account, so
      // the backend refuses an unqualified delete rather than guess a target.
      const res = await fetch('/api/identity', {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ confirm: true, uid, ...proof }),
      });
      if (!res.ok) {
        setRowError({ uid, message: await errorMessage(res, 'Remove failed') });
        return;
      }
      // Report what the purge actually removed. "Account removed" on its own
      // would not distinguish a clean delete from one that stranded the data.
      const data = (await res.json().catch(() => ({}))) as { purged?: Stats };
      const purged = data.purged;
      setNotice(
        purged && purged.records > 0
          ? `Account removed. Purged ${purged.records.toLocaleString()} User Data Layer ` +
            `${purged.records === 1 ? 'record' : 'records'} across ` +
            `${purged.cids.toLocaleString()} ${purged.cids === 1 ? 'title' : 'titles'}.`
          : 'Account removed. It held no User Data Layer records.'
      );
      setPending(null);
      await refresh();
    } catch (e) {
      setRowError({ uid, message: `Remove failed: ${e instanceof Error ? e.message : String(e)}` });
    } finally {
      setBusyUid(null);
    }
  };

  const dismissSecret = () => {
    setSecret(null);
    refresh();
  };

  const ask = (p: Pending) => {
    setRowError(null);
    setNotice(null);
    setPending(p);
  };

  const showList = !loading && !fetchError && !secret;

  return (
    <div style={{ padding: '2rem', maxWidth: '900px', margin: '0 auto' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <h1 style={{ marginBottom: 0 }}>Identity</h1>
        <button style={{ ...buttonStyle, marginRight: 0 }} onClick={refresh} disabled={loading}>
          {loading ? 'Loading…' : 'Refresh'}
        </button>
      </div>
      <p style={{ color: '#9ca3af' }}>
        Every signing key in this node's keystore. meta-core has no "current user" — it stores
        accounts, and which one is <em>you</em> is decided by whichever client signs in (e.g.
        meta-watch). Keys live one JSON file per account under{' '}
        <code>{'{META_CORE_PATH}'}/identity/accounts/&lt;uid&gt;.json</code>, and are used to sign
        User Data Layer records (ratings, watch progress, "liked" flags).
      </p>

      <div style={{ ...dangerPanelStyle, marginTop: 0, marginBottom: '1rem' }}>
        <p style={{ color: '#fecaca', margin: 0 }}>
          <strong>The key is the only authority.</strong> Revealing or removing an account requires
          that account's secret key, entered here and used to sign a one-time challenge in your
          browser — there is no admin override, and holding this page is not one. Listing and
          creating accounts are still open to anything that can reach this API, so the perimeter in
          front of this port is what keeps the roster private.
        </p>
        <p style={{ color: '#fecaca', margin: '0.5rem 0 0' }}>
          What this does <em>not</em> defend against: anyone with filesystem access to{' '}
          <code>{'{META_CORE_PATH}'}/identity/accounts/</code> reads every private key directly.
          The keys are stored in plaintext, matching the trust model of the rest of{' '}
          <code>/meta-core</code>.
        </p>
      </div>

      {notice && (
        <div style={{ ...cardStyle, border: '1px solid #0f3460' }}>
          <p style={{ color: '#9ca3af', margin: 0 }}>{notice}</p>
        </div>
      )}

      {loading && <div style={cardStyle}>Loading…</div>}

      {fetchError && (
        <div style={{ ...cardStyle, border: '1px solid #7f1d1d' }}>
          <p style={{ color: '#f87171', margin: '0 0 1rem' }}>{fetchError}</p>
          <button style={buttonStyle} onClick={refresh}>
            Retry
          </button>
        </div>
      )}

      {!loading && !fetchError && secret && <SecretKeyPanel data={secret} onDismiss={dismissSecret} />}

      {showList && (
        <>
          <NewAccountPanel onGenerated={(s) => setSecret(s)} onImported={refresh} />

          <h2 style={{ fontSize: '1.1rem', color: '#9ca3af', margin: '1.5rem 0 1rem' }}>
            Accounts ({accounts?.length ?? 0})
          </h2>

          {accounts && accounts.length === 0 && (
            <div style={cardStyle}>
              <p style={{ color: '#9ca3af', margin: 0 }}>
                No accounts in this keystore. Accounts are normally created by a client when
                someone signs up; use <em>Add an account</em> above if you are bootstrapping this
                node.
              </p>
            </div>
          )}

          {accounts?.map((a) => (
            <AccountCard
              key={a.uid}
              account={a}
              name={names[a.uid]}
              stats={stats === null ? null : stats[a.uid]}
              busy={busyUid === a.uid}
              disabled={busyUid !== null}
              pending={pending}
              error={rowError?.uid === a.uid ? rowError.message : null}
              onAsk={ask}
              onCancel={() => setPending(null)}
              onReveal={reveal}
              onRemove={remove}
            />
          ))}
        </>
      )}
    </div>
  );
}
