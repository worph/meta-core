import { useEffect, useState } from 'react';
import { KVAPI } from '../api/kvApi';

interface Props {
  // Branch path, ends with the delimiter (":" or "/").
  branchPath: string;
  reloadToken: number;
  onChanged: (info: string, errors?: string[]) => void;
}

// Branch operations are non-atomic — each per-key write/delete goes one round
// trip to Redis and may fail individually. Failures collect into a list and
// surface via the parent's toast stack. Per the design, we accept partial
// failure and surface what failed.
export function KVBranchEditor({ branchPath, reloadToken, onChanged }: Props) {
  const [count, setCount] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [newChild, setNewChild] = useState('');
  const [newValue, setNewValue] = useState('');
  const [renameTo, setRenameTo] = useState('');

  // Use the search endpoint with the branch path as the substring to count
  // descendants. Truncated when there are more than the limit; we just show
  // an approximate count.
  useEffect(() => {
    let cancelled = false;
    KVAPI.search(branchPath, 5000)
      .then((r) => {
        if (cancelled) return;
        // Filter to true descendants (those starting with branchPath).
        const descendants = r.keys.filter((k) => k.startsWith(branchPath));
        setCount(descendants.length);
      })
      .catch(() => { if (!cancelled) setCount(null); });
    return () => { cancelled = true; };
  }, [branchPath, reloadToken]);

  // Walk all descendants and run an op against each, collecting failures.
  const forEachDescendant = async (op: (key: string) => Promise<void>): Promise<string[]> => {
    const r = await KVAPI.search(branchPath, 5000);
    const keys = r.keys.filter((k) => k.startsWith(branchPath));
    const errors: string[] = [];
    for (const k of keys) {
      try { await op(k); } catch (e: any) { errors.push(`${k}: ${e.message}`); }
    }
    return errors;
  };

  const addChild = async () => {
    if (!newChild) return;
    const childKey = branchPath + newChild;
    setBusy(true);
    try {
      await KVAPI.put(childKey, newValue);
      setNewChild('');
      setNewValue('');
      onChanged(`Created ${childKey}`);
    } catch (e: any) {
      onChanged(`Failed to create ${childKey}`, [e.message]);
    } finally {
      setBusy(false);
    }
  };

  const deleteSubtree = async () => {
    if (!confirm(`Delete every key under ${branchPath} (${count ?? '?'} entries)?`)) return;
    setBusy(true);
    try {
      const errors = await forEachDescendant((k) => KVAPI.del(k).then(() => undefined));
      if (errors.length === 0) {
        onChanged(`Deleted subtree ${branchPath}`);
      } else {
        onChanged(`Deleted subtree ${branchPath} with ${errors.length} failure(s)`, errors);
      }
    } finally {
      setBusy(false);
    }
  };

  const renameBranch = async () => {
    if (!renameTo) return;
    const target = renameTo.endsWith('/') || renameTo.endsWith(':') ? renameTo : renameTo + '/';
    if (target === branchPath) return;
    if (!confirm(`Rename ${branchPath} → ${target}? This copies ${count ?? '?'} keys then deletes the originals.`)) return;
    setBusy(true);
    try {
      // Pass 1: copy
      const r = await KVAPI.search(branchPath, 5000);
      const keys = r.keys.filter((k) => k.startsWith(branchPath));
      const copyErrors: string[] = [];
      for (const oldKey of keys) {
        try {
          const v = await KVAPI.get(oldKey);
          if (!v.exists || v.type !== 'string') {
            copyErrors.push(`${oldKey}: skipped (type ${v.type || 'missing'})`);
            continue;
          }
          const newKey = target + oldKey.slice(branchPath.length);
          await KVAPI.put(newKey, v.value);
        } catch (e: any) {
          copyErrors.push(`${oldKey}: ${e.message}`);
        }
      }
      // Pass 2: delete originals (only the ones we successfully copied)
      const failedOldKeys = new Set(copyErrors.map((e) => e.split(':')[0]));
      const deleteErrors: string[] = [];
      for (const oldKey of keys) {
        if (failedOldKeys.has(oldKey)) continue;
        try { await KVAPI.del(oldKey); } catch (e: any) { deleteErrors.push(`${oldKey}: ${e.message}`); }
      }
      const allErrors = [...copyErrors, ...deleteErrors];
      if (allErrors.length === 0) {
        onChanged(`Renamed ${branchPath} → ${target}`);
      } else {
        onChanged(`Renamed ${branchPath} → ${target} with ${allErrors.length} failure(s)`, allErrors);
      }
      setRenameTo('');
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="kv-branch">
      <div className="kv-leaf-key">
        <code className="kv-leaf-key-text">{branchPath}</code>
        <button
          className="kv-btn-icon"
          onClick={() => navigator.clipboard.writeText(branchPath)}
          title="copy path"
        >⧉</button>
      </div>

      <div className="kv-pane-status">
        {count == null ? 'counting…' : `${count} descendant key${count === 1 ? '' : 's'}`}
      </div>

      <section className="kv-branch-section">
        <h4>Add child</h4>
        <div className="kv-branch-row">
          <span className="kv-branch-prefix">{branchPath}</span>
          <input
            className="kv-leaf-input"
            placeholder="child path (may contain /)"
            value={newChild}
            onChange={(e) => setNewChild(e.target.value)}
            disabled={busy}
          />
        </div>
        <input
          className="kv-leaf-input"
          placeholder="value"
          value={newValue}
          onChange={(e) => setNewValue(e.target.value)}
          disabled={busy}
        />
        <button className="kv-btn kv-btn-primary" disabled={!newChild || busy} onClick={addChild}>
          Add
        </button>
      </section>

      <section className="kv-branch-section">
        <h4>Rename branch</h4>
        <div className="kv-branch-row">
          <span className="kv-branch-prefix">→</span>
          <input
            className="kv-leaf-input"
            placeholder="new prefix (e.g. file:newhash/)"
            value={renameTo}
            onChange={(e) => setRenameTo(e.target.value)}
            disabled={busy}
          />
        </div>
        <p className="kv-branch-note">
          Non-atomic: copies each descendant to the new prefix then deletes the original.
          Failures are surfaced individually.
        </p>
        <button className="kv-btn" disabled={!renameTo || busy} onClick={renameBranch}>
          Rename
        </button>
      </section>

      <section className="kv-branch-section">
        <h4>Delete subtree</h4>
        <p className="kv-branch-note">
          Deletes every descendant key. Cannot be undone.
        </p>
        <button className="kv-btn kv-btn-danger" disabled={busy || count === 0} onClick={deleteSubtree}>
          Delete {count ?? ''} key{count === 1 ? '' : 's'}
        </button>
      </section>
    </div>
  );
}
