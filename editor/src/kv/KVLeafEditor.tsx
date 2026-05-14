import { useEffect, useState } from 'react';
import { KVAPI } from '../api/kvApi';
import { useSchema } from '../api/schemaApi';
import { ALL_TYPES, describeType, detectType } from './typeHeuristics';
import { lookupSchemaForKey } from './schemaLookup';
import { LeafValueEditor } from './leafRenderers';
import { FieldType } from './types';

interface Props {
  // Full Redis key path of the selected leaf.
  selectedKey: string;
  // Bumped after a delete or external change to force refetch.
  reloadToken: number;
  onSaved: () => void;
  onDeleted: () => void;
  onError: (msg: string) => void;
}

export function KVLeafEditor({ selectedKey, reloadToken, onSaved, onDeleted, onError }: Props) {
  const [original, setOriginal] = useState<string>('');
  const [draft, setDraft] = useState<string>('');
  const [exists, setExists] = useState<boolean>(true);
  const [redisType, setRedisType] = useState<string>('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  // Manual override for the inferred type. "auto" defers to schema → heuristic.
  const [typeOverride, setTypeOverride] = useState<FieldType>('auto');
  const { schema } = useSchema();
  const schemaLookup = lookupSchemaForKey(schema, selectedKey);
  // Schema is authoritative when it has an opinion; heuristics fill the gap.
  const detected = schemaLookup.fieldType ?? detectType(selectedKey, original);
  const effectiveType = typeOverride === 'auto' ? detected : typeOverride;

  // Reset override when the selected key changes — the previous override
  // doesn't apply to a different leaf.
  useEffect(() => { setTypeOverride('auto'); }, [selectedKey]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    KVAPI.get(selectedKey)
      .then((r) => {
        if (cancelled) return;
        setOriginal(r.value);
        setDraft(r.value);
        setExists(r.exists);
        setRedisType(r.type);
      })
      .catch((e) => onError(e.message))
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [selectedKey, reloadToken, onError]);

  const dirty = draft !== original;
  const editable = exists ? redisType === 'string' || redisType === '' : true;

  const save = async () => {
    setSaving(true);
    try {
      await KVAPI.put(selectedKey, draft);
      setOriginal(draft);
      setExists(true);
      setRedisType('string');
      onSaved();
    } catch (e: any) {
      onError(e.message);
    } finally {
      setSaving(false);
    }
  };

  const del = async () => {
    if (!confirm(`Delete ${selectedKey}?`)) return;
    setSaving(true);
    try {
      await KVAPI.del(selectedKey);
      onDeleted();
    } catch (e: any) {
      onError(e.message);
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return <div className="kv-pane-status">loading…</div>;
  }

  if (!editable) {
    return (
      <div className="kv-leaf">
        <KeyHeader keyPath={selectedKey} />
        <div className="kv-pane-status">
          Key has Redis type <code>{redisType}</code> — only string-typed values are editable here.
        </div>
      </div>
    );
  }

  return (
    <div className="kv-leaf">
      <KeyHeader keyPath={selectedKey} />

      <div className="kv-leaf-meta">
        <label className="kv-leaf-label">Type</label>
        <select
          className="kv-leaf-type"
          value={typeOverride}
          onChange={(e) => setTypeOverride(e.target.value as FieldType)}
        >
          {ALL_TYPES.map((t) => (
            <option key={t} value={t}>
              {t === 'auto' ? `auto (${describeType(detected)})` : describeType(t)}
            </option>
          ))}
        </select>
        {!exists && <span className="kv-leaf-hint">new key — save to create</span>}
      </div>

      <LeafValueEditor type={effectiveType} value={draft} onChange={setDraft} />

      <div className="kv-leaf-actions">
        <button
          className="kv-btn kv-btn-primary"
          onClick={save}
          disabled={!dirty || saving}
        >{saving ? 'saving…' : exists ? 'Save' : 'Create'}</button>
        <button
          className="kv-btn"
          onClick={() => setDraft(original)}
          disabled={!dirty || saving}
        >Reset</button>
        {exists && (
          <button
            className="kv-btn kv-btn-danger"
            onClick={del}
            disabled={saving}
          >Delete</button>
        )}
      </div>
    </div>
  );
}

function KeyHeader({ keyPath }: { keyPath: string }) {
  return (
    <div className="kv-leaf-key">
      <code className="kv-leaf-key-text">{keyPath}</code>
      <button
        className="kv-btn-icon"
        onClick={() => navigator.clipboard.writeText(keyPath)}
        title="copy key"
      >⧉</button>
    </div>
  );
}

// Leaf value rendering moved to leafRenderers.tsx (LeafValueEditor)
// so the file-detail view can reuse it per row.
