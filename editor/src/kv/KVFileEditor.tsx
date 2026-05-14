import { useEffect, useMemo, useRef, useState } from 'react';
import { KVAPI } from '../api/kvApi';
import { SchemaFieldInfo, useSchema } from '../api/schemaApi';
import { LanguageCombobox, formatLanguageOption } from '../components/LanguageCombobox';
import { LeafValueEditor } from './leafRenderers';
import { schemaToFieldType } from './schemaLookup';
import { detectType } from './typeHeuristics';
import { FieldType } from './types';

interface Props {
  /** Branch path of a file, e.g. "file:<hash>/". */
  branchPath: string;
  reloadToken: number;
  onChanged: (info: string, errors?: string[]) => void;
  /** Navigate to a single leaf for the manual deep editor. */
  onSelectLeaf: (key: string) => void;
  /**
   * When set, scroll to and pulse the row matching this logical field path
   * (e.g. "releasedate", "plot", "stream/0"). The `token` lets the same field
   * fire a fresh scroll across consecutive clicks.
   */
  scrollToField?: { field: string; token: number } | null;
}

const LANG_CODE_RE = /^[a-z]{2,3}$/;

interface LeafRow {
  kind: 'leaf';
  fieldKey: string;
  value: string;
  fieldType: FieldType;
  info?: SchemaFieldInfo;
}

interface LanguageRow {
  kind: 'language-group';
  fieldKey: string;
  /** Current language → text map. */
  languageMap: Record<string, string>;
  info?: SchemaFieldInfo;
}

type Row = LeafRow | LanguageRow;

interface RowGroup {
  prefix: string;
  isCluster: boolean;
  rows: Row[];
}

export function KVFileEditor({
  branchPath,
  reloadToken,
  onChanged,
  onSelectLeaf,
  scrollToField,
}: Props) {
  const [metadata, setMetadata] = useState<Record<string, string> | null>(null);
  const [edits, setEdits] = useState<Map<string, string | null>>(new Map());
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(new Set());
  const [pulse, setPulse] = useState<string | null>(null);
  const [newFieldKey, setNewFieldKey] = useState('');
  const [newFieldValue, setNewFieldValue] = useState('');
  const [addingField, setAddingField] = useState(false);
  // Track whether we've seeded collapsedGroups for the current branch; resets
  // when the branch changes so each file gets its defaults reapplied.
  const defaultsAppliedRef = useRef<string | null>(null);

  const { schema } = useSchema();

  // When the parent passes a target field, expand its prefix group (if any)
  // and scroll the row into view. We also briefly pulse the row to draw the
  // eye — the pulse is reset after the animation duration.
  useEffect(() => {
    if (!scrollToField) return;
    const { field } = scrollToField;
    const prefix = field.split('/', 1)[0];
    setCollapsedGroups((cur) => {
      if (!cur.has(prefix)) return cur;
      const next = new Set(cur);
      next.delete(prefix);
      return next;
    });
    requestAnimationFrame(() => {
      const el = document.querySelector(
        `[data-row-key="${cssEscape(field)}"]`,
      ) as HTMLElement | null;
      if (el) {
        el.scrollIntoView({ behavior: 'smooth', block: 'center' });
        setPulse(field);
        window.setTimeout(() => setPulse((p) => (p === field ? null : p)), 1400);
      }
    });
  }, [scrollToField?.field, scrollToField?.token]);

  const hashId = branchPath.slice('file:'.length, -1);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    KVAPI.treeAll(branchPath, '/', 5000)
      .then(async (resp) => {
        // resp.leaves = full keys under branchPath. Recursive descent: also
        // walk branch children (they're paths to sub-objects with their own
        // leaves, e.g. file:<h>/plot/ → plot/eng).
        const all: string[] = [...resp.leaves];
        const queue = [...resp.branches];
        while (queue.length > 0) {
          const next = queue.shift()!;
          try {
            const sub = await KVAPI.treeAll(next, '/', 5000);
            all.push(...sub.leaves);
            queue.push(...sub.branches);
          } catch {
            // Ignore failures on a sub-branch; show what we can.
          }
        }
        if (cancelled) return;
        // MGET-equivalent: parallel fetches with a small cap to avoid burst.
        const values = await Promise.all(
          all.map((k) => KVAPI.get(k).then((r) => [k, r.exists ? r.value : null] as const).catch(() => [k, null] as const)),
        );
        if (cancelled) return;
        const flat: Record<string, string> = {};
        for (const [k, v] of values) {
          if (v == null) continue;
          // Strip the branch prefix so keys are the logical field path
          // ("releasedate", "plot/eng", "stream/0").
          const fieldKey = k.slice(branchPath.length);
          if (fieldKey) flat[fieldKey] = v;
        }
        setMetadata(flat);
        setEdits(new Map());
      })
      .catch(() => {
        if (!cancelled) setMetadata({});
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [branchPath, reloadToken]);

  const groups = useMemo(
    () => groupRows(buildRows(metadata ?? {}, schema)),
    [metadata, schema],
  );

  // Default state: every cluster group (e.g. stream/*, cid_*) starts collapsed.
  // Reapplied when the user switches to a different file.
  useEffect(() => {
    if (groups.length === 0) return;
    if (defaultsAppliedRef.current === branchPath) return;
    defaultsAppliedRef.current = branchPath;
    setCollapsedGroups(new Set(groups.filter((g) => g.isCluster).map((g) => g.prefix)));
  }, [groups, branchPath]);

  if (loading) {
    return <div className="kv-pane-status">loading file…</div>;
  }

  if (!metadata) {
    return <div className="kv-pane-status">no data for {branchPath}</div>;
  }

  const fileName = metadata['fileName'];
  const filePath = metadata['filePath'];

  const onLeafChange = (fieldKey: string, value: string) => {
    setEdits((prev) => {
      const next = new Map(prev);
      const original = metadata[fieldKey] ?? null;
      if (value === original) next.delete(fieldKey);
      else next.set(fieldKey, value);
      return next;
    });
  };

  /**
   * Toggle a field's pending-delete state. Re-clicking restores; if the field
   * is brand-new (no original value) we just drop it from the edits map.
   */
  const onLeafDeleteToggle = (fieldKey: string) => {
    setEdits((prev) => {
      const next = new Map(prev);
      if (next.get(fieldKey) === null) {
        // Currently pending delete → undo.
        next.delete(fieldKey);
      } else {
        next.set(fieldKey, null);
      }
      return next;
    });
  };

  const handleAddField = async () => {
    const key = newFieldKey.trim();
    if (!key) return;
    const fullKey = branchPath + key;
    setAddingField(true);
    try {
      await KVAPI.put(fullKey, newFieldValue);
      setNewFieldKey('');
      setNewFieldValue('');
      onChanged(`Added ${fullKey}`);
      // Refetch this file so the new field appears in the row list.
      const r = await KVAPI.get(fullKey);
      if (r.exists) {
        setMetadata((cur) => ({ ...(cur ?? {}), [key]: r.value }));
      }
    } catch (e: any) {
      onChanged(`Failed to add ${fullKey}`, [e.message]);
    } finally {
      setAddingField(false);
    }
  };

  const onLanguageGroupChange = (
    groupKey: string,
    nextMap: Record<string, string>,
    originalMap: Record<string, string>,
  ) => {
    setEdits((prev) => {
      const next = new Map(prev);
      const allLangs = new Set([...Object.keys(originalMap), ...Object.keys(nextMap)]);
      for (const lang of allLangs) {
        const fieldKey = `${groupKey}/${lang}`;
        const orig = originalMap[lang] ?? null;
        const cur = nextMap[lang] ?? null;
        if (cur === orig) next.delete(fieldKey);
        else if (cur == null) next.set(fieldKey, null);
        else next.set(fieldKey, cur);
      }
      return next;
    });
  };

  const handleSave = async () => {
    if (edits.size === 0) return;
    setSaving(true);
    const errors: string[] = [];
    for (const [fieldKey, newValue] of edits) {
      const fullKey = branchPath + fieldKey;
      try {
        if (newValue === null) await KVAPI.del(fullKey);
        else await KVAPI.put(fullKey, newValue);
      } catch (e: any) {
        errors.push(`${fieldKey}: ${e.message}`);
      }
    }
    setSaving(false);
    if (errors.length === 0) {
      onChanged(`Saved ${edits.size} field${edits.size === 1 ? '' : 's'} on ${fileName ?? hashId}`);
    } else {
      onChanged(
        `Saved with ${errors.length} failure(s) on ${fileName ?? hashId}`,
        errors,
      );
    }
  };

  const handleDiscard = () => setEdits(new Map());

  const toggleGroup = (prefix: string) => {
    setCollapsedGroups((cur) => {
      const next = new Set(cur);
      if (next.has(prefix)) next.delete(prefix);
      else next.add(prefix);
      return next;
    });
  };

  return (
    <div className="kv-file">
      <div className="kv-file-header">
        <div className="kv-file-name">
          {fileName ?? <span className="kv-file-name-fallback">(no fileName)</span>}
        </div>
        <div className="kv-file-meta">
          <code className="kv-file-hash" title={hashId}>
            {hashId.slice(0, 8)}…{hashId.slice(-6)}
          </code>
          {filePath && <span className="kv-file-path" title={filePath}>{filePath}</span>}
        </div>
        <div className="kv-file-actions">
          <button
            className="kv-btn kv-btn-primary"
            disabled={edits.size === 0 || saving}
            onClick={handleSave}
          >
            {saving ? 'saving…' : `Save (${edits.size})`}
          </button>
          <button
            className="kv-btn"
            disabled={edits.size === 0 || saving}
            onClick={handleDiscard}
          >
            Discard
          </button>
        </div>
      </div>

      <div className="kv-file-rows">
        {groups.length === 0 && (
          <div className="kv-pane-status">No data yet for this file.</div>
        )}
        {groups.map((g) => (
          <FieldGroupView
            key={g.prefix}
            group={g}
            edits={edits}
            collapsed={collapsedGroups.has(g.prefix)}
            pulse={pulse}
            onToggle={() => toggleGroup(g.prefix)}
            onLeafChange={onLeafChange}
            onLeafDeleteToggle={onLeafDeleteToggle}
            onLanguageChange={onLanguageGroupChange}
            onJumpToLeaf={(fieldKey) => onSelectLeaf(branchPath + fieldKey)}
          />
        ))}
      </div>

      <div className="kv-file-add">
        <span className="kv-file-add-prefix">{branchPath}</span>
        <input
          type="text"
          className="kv-leaf-input kv-leaf-mono"
          placeholder="field path (may contain /)"
          value={newFieldKey}
          onChange={(e) => setNewFieldKey(e.target.value)}
          disabled={addingField}
        />
        <input
          type="text"
          className="kv-leaf-input"
          placeholder="value"
          value={newFieldValue}
          onChange={(e) => setNewFieldValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && newFieldKey.trim()) handleAddField();
          }}
          disabled={addingField}
        />
        <button
          className="kv-btn kv-btn-primary"
          disabled={!newFieldKey.trim() || addingField}
          onClick={handleAddField}
        >
          {addingField ? 'adding…' : '+ Add field'}
        </button>
      </div>
    </div>
  );
}

function FieldGroupView({
  group,
  edits,
  collapsed,
  pulse,
  onToggle,
  onLeafChange,
  onLeafDeleteToggle,
  onLanguageChange,
  onJumpToLeaf,
}: {
  group: RowGroup;
  edits: Map<string, string | null>;
  collapsed: boolean;
  pulse: string | null;
  onToggle: () => void;
  onLeafChange: (fieldKey: string, value: string) => void;
  onLeafDeleteToggle: (fieldKey: string) => void;
  onLanguageChange: (
    groupKey: string,
    nextMap: Record<string, string>,
    originalMap: Record<string, string>,
  ) => void;
  onJumpToLeaf: (fieldKey: string) => void;
}) {
  if (!group.isCluster) {
    return (
      <>
        {group.rows.map((row) => (
          <RowView
            key={row.fieldKey}
            row={row}
            edits={edits}
            pulse={pulse}
            onLeafChange={onLeafChange}
            onLeafDeleteToggle={onLeafDeleteToggle}
            onLanguageChange={onLanguageChange}
            onJumpToLeaf={onJumpToLeaf}
          />
        ))}
      </>
    );
  }
  return (
    <div className={`kv-file-group ${collapsed ? 'collapsed' : ''}`}>
      <button
        type="button"
        className="kv-file-group-header"
        onClick={onToggle}
        title={collapsed ? 'expand' : 'collapse'}
      >
        <span className="kv-file-group-chevron">{collapsed ? '▸' : '▾'}</span>
        <span className="kv-file-group-name">{group.prefix}</span>
        <span className="kv-file-group-count">{group.rows.length}</span>
      </button>
      {!collapsed && (
        <div className="kv-file-group-body">
          {group.rows.map((row) => (
            <RowView
              key={row.fieldKey}
              row={row}
              edits={edits}
              pulse={pulse}
              onLeafChange={onLeafChange}
              onLeafDeleteToggle={onLeafDeleteToggle}
              onLanguageChange={onLanguageChange}
              onJumpToLeaf={onJumpToLeaf}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function RowView({
  row,
  edits,
  pulse,
  onLeafChange,
  onLeafDeleteToggle,
  onLanguageChange,
  onJumpToLeaf,
}: {
  row: Row;
  edits: Map<string, string | null>;
  pulse: string | null;
  onLeafChange: (fieldKey: string, value: string) => void;
  onLeafDeleteToggle: (fieldKey: string) => void;
  onLanguageChange: (
    groupKey: string,
    nextMap: Record<string, string>,
    originalMap: Record<string, string>,
  ) => void;
  onJumpToLeaf: (fieldKey: string) => void;
}) {
  const pulsed = pulse === row.fieldKey;
  if (row.kind === 'leaf') {
    const pendingDelete = edits.get(row.fieldKey) === null;
    const draft = edits.has(row.fieldKey) ? (edits.get(row.fieldKey) ?? '') : row.value;
    const dirty = edits.has(row.fieldKey);
    return (
      <div
        className={`kv-file-row ${dirty ? 'dirty' : ''} ${pendingDelete ? 'pending-delete' : ''} ${pulsed ? 'pulsed' : ''}`}
        data-row-key={row.fieldKey}
      >
        <button
          type="button"
          className="kv-file-row-label"
          onClick={() => onJumpToLeaf(row.fieldKey)}
          title="open in single-leaf editor"
        >
          {row.fieldKey}
        </button>
        <div className="kv-file-row-value">
          {pendingDelete ? (
            <div className="kv-file-row-deleted-note">marked for deletion</div>
          ) : (
            <LeafValueEditor
              type={row.fieldType}
              value={draft}
              onChange={(v) => onLeafChange(row.fieldKey, v)}
            />
          )}
        </div>
        <button
          type="button"
          className="kv-file-row-delete"
          onClick={() => onLeafDeleteToggle(row.fieldKey)}
          title={pendingDelete ? 'undo deletion' : 'delete this field'}
          aria-label={pendingDelete ? 'undo deletion' : 'delete this field'}
        >
          {pendingDelete ? '↺' : '×'}
        </button>
      </div>
    );
  }
  return (
    <LanguageGroupRow
      row={row}
      edits={edits}
      pulsed={pulsed}
      onLanguageChange={onLanguageChange}
    />
  );
}

function LanguageGroupRow({
  row,
  edits,
  pulsed,
  onLanguageChange,
}: {
  row: LanguageRow;
  edits: Map<string, string | null>;
  pulsed: boolean;
  onLanguageChange: (
    groupKey: string,
    nextMap: Record<string, string>,
    originalMap: Record<string, string>,
  ) => void;
}) {
  // Apply pending edits on top of the original language map.
  const effectiveMap: Record<string, string> = { ...row.languageMap };
  for (const [k, v] of edits) {
    if (!k.startsWith(`${row.fieldKey}/`)) continue;
    const lang = k.slice(row.fieldKey.length + 1);
    if (v === null) delete effectiveMap[lang];
    else effectiveMap[lang] = v;
  }

  const entries = Object.entries(effectiveMap);
  const dirty = [...edits.keys()].some((k) => k.startsWith(`${row.fieldKey}/`));

  const updateLang = (lang: string, text: string) => {
    onLanguageChange(row.fieldKey, { ...effectiveMap, [lang]: text }, row.languageMap);
  };

  const renameLang = (oldLang: string, newLang: string) => {
    if (oldLang === newLang) return;
    const next = { ...effectiveMap };
    const text = next[oldLang];
    delete next[oldLang];
    next[newLang] = text;
    onLanguageChange(row.fieldKey, next, row.languageMap);
  };

  const removeLang = (lang: string) => {
    const next = { ...effectiveMap };
    delete next[lang];
    onLanguageChange(row.fieldKey, next, row.languageMap);
  };

  const addLang = () => {
    const used = new Set(entries.map(([k]) => k));
    const defaults = ['eng', 'jpn', 'zho', 'kor', 'fra', 'deu', 'spa', 'fre'];
    const defaultLang = defaults.find((l) => !used.has(l)) || 'eng';
    onLanguageChange(row.fieldKey, { ...effectiveMap, [defaultLang]: '' }, row.languageMap);
  };

  return (
    <div
      className={`kv-file-row kv-file-row-lang ${dirty ? 'dirty' : ''} ${pulsed ? 'pulsed' : ''}`}
      data-row-key={row.fieldKey}
    >
      <div className="kv-file-row-label-static">{row.fieldKey}</div>
      <div className="kv-file-row-value">
        <div className="kv-file-lang-entries">
          {entries.map(([lang, text]) => (
            <div key={lang} className="kv-file-lang-entry">
              <LanguageCombobox
                value={lang}
                onChange={(newLang) => renameLang(lang, newLang)}
              />
              <input
                type="text"
                className="kv-leaf-input"
                value={text}
                title={formatLanguageOption(lang)}
                onChange={(e) => updateLang(lang, e.target.value)}
              />
              <button
                type="button"
                className="kv-btn-icon kv-file-lang-remove"
                onClick={() => removeLang(lang)}
                title="remove this language"
              >
                ×
              </button>
            </div>
          ))}
          <button
            type="button"
            className="kv-btn kv-file-lang-add"
            onClick={addLang}
          >
            + Add language
          </button>
        </div>
      </div>
    </div>
  );
}

// ──────────────────────────────────────────────────────────────────────────
// Row model — kept local because it's only used here and is small.
// ──────────────────────────────────────────────────────────────────────────

function buildRows(
  flat: Record<string, string>,
  schema: ReturnType<typeof useSchema>['schema'],
): Row[] {
  const languageGroupKeys = new Set<string>();
  if (schema) {
    for (const [k, info] of Object.entries(schema.fields)) {
      if (info.key_hint === 'language-code') languageGroupKeys.add(k);
    }
  }

  const langMaps: Record<string, Record<string, string>> = {};
  const rows: Row[] = [];

  for (const [key, value] of Object.entries(flat)) {
    const slash = key.lastIndexOf('/');
    if (slash > 0) {
      const prefix = key.slice(0, slash);
      const tail = key.slice(slash + 1);
      if (languageGroupKeys.has(prefix) && LANG_CODE_RE.test(tail)) {
        if (!langMaps[prefix]) langMaps[prefix] = {};
        langMaps[prefix][tail] = value;
        continue;
      }
    }
    const info = schema?.fields[key];
    rows.push({
      kind: 'leaf',
      fieldKey: key,
      value,
      // Schema decides when it can; otherwise heuristic on (path, value).
      fieldType: schemaToFieldType(info) ?? detectType(key, value),
      info,
    });
  }

  for (const [groupKey, langMap] of Object.entries(langMaps)) {
    rows.push({
      kind: 'language-group',
      fieldKey: groupKey,
      languageMap: langMap,
      info: schema?.fields[groupKey],
    });
  }

  rows.sort((a, b) => a.fieldKey.localeCompare(b.fieldKey));
  return rows;
}

/** Escape characters that would break a CSS attribute selector value. */
function cssEscape(s: string): string {
  return s.replace(/["\\]/g, (c) => `\\${c}`);
}

function groupRows(rows: Row[]): RowGroup[] {
  const out: RowGroup[] = [];
  for (const row of rows) {
    const prefix = row.fieldKey.split('/', 1)[0];
    const last = out[out.length - 1];
    if (last && last.prefix === prefix) {
      last.rows.push(row);
    } else {
      out.push({ prefix, isCluster: false, rows: [row] });
    }
  }
  for (const g of out) g.isCluster = g.rows.length > 1;
  return out;
}
