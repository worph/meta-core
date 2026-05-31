import { useEffect, useState } from 'react';

// Status returned by GET /api/identity. Private key is never present here —
// it is shown exactly once at generation time (see GeneratedKeyPanel).
interface IdentityStatus {
  hasIdentity: boolean;
  uid?: string;
  curve?: string;
  createdAt?: number;
}

interface GeneratedKey {
  uid: string;
  curve: string;
  createdAt: number;
  privateKeyHex: string;
}

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

function GeneratedKeyPanel({ data, onDismiss }: { data: GeneratedKey; onDismiss: () => void }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(data.privateKeyHex);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard API blocked (non-HTTPS context, etc.) — fall back to selection.
      const sel = window.getSelection();
      const range = document.createRange();
      const el = document.getElementById('mm-privkey-box');
      if (sel && el) {
        range.selectNodeContents(el);
        sel.removeAllRanges();
        sel.addRange(range);
      }
    }
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
        <strong>This private key is shown ONCE.</strong> Save it now in a password manager.
        It will never be displayed again — there is no recovery path. Losing it means losing access
        to every record signed under this identity.
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
          I have saved it
        </button>
      </div>
    </div>
  );
}

function NoIdentityPanel({
  onGenerated,
  onImported,
}: {
  onGenerated: (g: GeneratedKey) => void;
  onImported: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [importing, setImporting] = useState(false);
  const [importHex, setImportHex] = useState('');
  const [error, setError] = useState<string | null>(null);

  const generate = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await fetch('/api/identity/generate', { method: 'POST' });
      if (!res.ok) {
        setError(await errorMessage(res, 'Generate failed'));
        return;
      }
      const data = (await res.json()) as GeneratedKey;
      onGenerated(data);
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
      <h3 style={{ marginTop: 0 }}>No identity yet</h3>
      <p style={{ color: '#9ca3af' }}>
        The User Data Layer needs a signing key for your ratings, watch progress, and "liked" flags
        to be attributed to you across devices. Nothing happens automatically — generate one below,
        or import a key you already have.
      </p>
      {error && (
        <p style={{ color: '#f87171', background: '#1f0a0a', padding: '0.5rem 0.75rem', borderRadius: '4px' }}>
          {error}
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

function HasIdentityPanel({ status, onReplace }: { status: IdentityStatus; onReplace: () => void }) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const remove = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await fetch('/api/identity?confirm=true', { method: 'DELETE' });
      if (!res.ok && res.status !== 204) {
        setError(await errorMessage(res, 'Delete failed'));
        return;
      }
      onReplace();
    } catch (e) {
      setError(`Delete failed: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  };

  return (
    <div style={cardStyle}>
      <h3 style={{ marginTop: 0 }}>Active identity</h3>
      <p style={{ color: '#9ca3af', margin: '0 0 0.5rem' }}>uid:</p>
      <div style={monoStyle}>{status.uid}</div>
      <p style={{ color: '#888', marginTop: '1rem' }}>
        Curve: {status.curve} · Created: {formatCreatedAt(status.createdAt)}
      </p>
      <p style={{ color: '#9ca3af', fontSize: '0.85rem' }}>
        The private key lives at <code>/meta-core/identity/identity.json</code> on this volume and
        is never returned through the API after generation. Other services on this stack (e.g.
        meta-watch) sign records by calling <code>POST /api/identity/sign</code>.
      </p>
      {error && (
        <p style={{ color: '#f87171', background: '#1f0a0a', padding: '0.5rem 0.75rem', borderRadius: '4px' }}>
          {error}
        </p>
      )}
      {!confirming ? (
        <button style={dangerButtonStyle} onClick={() => setConfirming(true)} disabled={busy}>
          Replace identity
        </button>
      ) : (
        <div
          style={{
            background: '#1f0a0a',
            border: '1px solid #7f1d1d',
            padding: '0.75rem 1rem',
            borderRadius: '4px',
            marginTop: '0.5rem',
          }}
        >
          <p style={{ color: '#fecaca', marginTop: 0 }}>
            Replacing the identity deletes the current keypair from disk. Records signed by the
            current uid will keep verifying for anyone who has them cached, but this node will no
            longer be able to write under that uid. <strong>Make sure you have backed up the key
            first.</strong>
          </p>
          <button style={dangerButtonStyle} onClick={remove} disabled={busy}>
            {busy ? 'Deleting…' : 'Yes, delete identity'}
          </button>
          <button style={buttonStyle} onClick={() => setConfirming(false)} disabled={busy}>
            Cancel
          </button>
        </div>
      )}
    </div>
  );
}

export default function Identity() {
  const [status, setStatus] = useState<IdentityStatus | null>(null);
  const [generated, setGenerated] = useState<GeneratedKey | null>(null);
  const [loading, setLoading] = useState(true);
  const [fetchError, setFetchError] = useState<string | null>(null);

  const refresh = async () => {
    setLoading(true);
    setFetchError(null);
    try {
      const res = await fetch('/api/identity');
      if (!res.ok) {
        setFetchError(await errorMessage(res, 'Failed to load identity status'));
        return;
      }
      setStatus((await res.json()) as IdentityStatus);
    } catch (e) {
      setFetchError(`Failed to load identity status: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    refresh();
  }, []);

  return (
    <div style={{ padding: '2rem', maxWidth: '900px', margin: '0 auto' }}>
      <h1>Identity</h1>
      <p style={{ color: '#9ca3af' }}>
        Signing key for the User Data Layer (per-user ratings, progress, "liked" flags). One
        identity per node. The same key on another device makes that device count as "you".
      </p>

      {loading && <div style={cardStyle}>Loading…</div>}
      {fetchError && (
        <div style={{ ...cardStyle, border: '1px solid #7f1d1d' }}>
          <p style={{ color: '#f87171', margin: 0 }}>{fetchError}</p>
          <button style={buttonStyle} onClick={refresh}>
            Retry
          </button>
        </div>
      )}

      {!loading && !fetchError && generated && (
        <GeneratedKeyPanel
          data={generated}
          onDismiss={() => {
            setGenerated(null);
            refresh();
          }}
        />
      )}

      {!loading && !fetchError && !generated && status && !status.hasIdentity && (
        <NoIdentityPanel onGenerated={(g) => setGenerated(g)} onImported={refresh} />
      )}

      {!loading && !fetchError && !generated && status && status.hasIdentity && (
        <HasIdentityPanel status={status} onReplace={refresh} />
      )}
    </div>
  );
}
