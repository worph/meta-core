import { useEffect, useMemo, useRef, useState } from 'react';
import { KVAPI, KVFindMatch, KVInfoResponse } from '../api/kvApi';
import { ancestorBranches, KVTree } from './KVTree';
import { FilteredKVTree } from './FilteredKVTree';
import { KVLeafEditor } from './KVLeafEditor';
import { KVBranchEditor } from './KVBranchEditor';
import { KVFileEditor } from './KVFileEditor';

const FILE_BRANCH_RE = /^file:[^/]+\/$/;
import { SearchFieldChips } from './SearchFieldChips';
import { ToastStack, makeToast } from './Toast';
import { ToastEntry } from './types';
import { SnapshotPanel } from '../components/SnapshotPanel';
import './KVApp.css';

const SEARCH_FIELDS_LS_KEY = 'kv-editor.searchFields.v1';
const DEFAULT_SEARCH_FIELDS = ['originalTitle', 'filePath', 'fileName', 'plot/eng'];

function loadSearchFields(): string[] {
  try {
    const raw = localStorage.getItem(SEARCH_FIELDS_LS_KEY);
    if (!raw) return DEFAULT_SEARCH_FIELDS;
    const parsed = JSON.parse(raw);
    if (Array.isArray(parsed) && parsed.every((x) => typeof x === 'string')) return parsed;
  } catch {}
  return DEFAULT_SEARCH_FIELDS;
}

// Top-level shell. Two panes inside the iframe:
//   left  — lazy tree of the keyspace
//   right — leaf editor (when a leaf is selected) or branch editor (branch).
//
// A sticky header carries the search bar, the breadcrumb, and basic stats.
export default function KVApp() {
  const [snapshotOpen, setSnapshotOpen] = useState(false);
  // Selected node. branchPath is set when a branch is selected; selectedKey
  // when a leaf is. Mutually exclusive.
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [branchPath, setBranchPath] = useState<string | null>(null);
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set(['']));
  const [searchInput, setSearchInput] = useState('');
  const [searchFields, setSearchFields] = useState<string[]>(loadSearchFields);
  const [searchResults, setSearchResults] = useState<KVFindMatch[] | null>(null);
  const [searchTruncated, setSearchTruncated] = useState(false);
  const [searching, setSearching] = useState(false);
  const [info, setInfo] = useState<KVInfoResponse | null>(null);
  const [reloadToken, setReloadToken] = useState(0);
  const [toasts, setToasts] = useState<ToastEntry[]>([]);
  const searchTimerRef = useRef<number | null>(null);
  // When the right pane is a file-detail view and the user clicks a leaf
  // under the same file branch in the tree, we route the click as a
  // scroll-anchor inside the file detail instead of a leaf-editor switch.
  // The token bumps each click so identical anchors still trigger a scroll.
  const [anchor, setAnchor] = useState<{ field: string; token: number } | null>(null);
  const isFileBranch = (p: string | null) => !!p && /^file:[^/]+\/$/.test(p);

  const handleLeafClick = (key: string) => {
    if (isFileBranch(branchPath) && key.startsWith(branchPath!)) {
      setAnchor({ field: key.slice(branchPath!.length), token: (anchor?.token ?? 0) + 1 });
      return;
    }
    setSelectedKey(key);
    setBranchPath(null);
    setAnchor(null);
  };

  const handleBranchClick = (path: string) => {
    setBranchPath(path);
    setSelectedKey(null);
    setAnchor(null);
  };

  const pushToast = (t: ToastEntry) => setToasts((cur) => [...cur, t]);
  const dismissToast = (id: number) => setToasts((cur) => cur.filter((t) => t.id !== id));

  useEffect(() => {
    KVAPI.info().then(setInfo).catch(() => {});
  }, [reloadToken]);

  // Deep-link: /editor/file/<hashId> opens the tree pre-expanded at that hash.
  useEffect(() => {
    const m = window.location.pathname.match(/\/file\/([a-z0-9]+)$/i);
    if (m) {
      const hashId = m[1];
      const branch = `file:${hashId}/`;
      setExpandedPaths((cur) => new Set([...cur, '', 'file:', branch]));
      setBranchPath(branch);
    }
  }, []);

  // Persist field list across reloads.
  useEffect(() => {
    try { localStorage.setItem(SEARCH_FIELDS_LS_KEY, JSON.stringify(searchFields)); } catch {}
  }, [searchFields]);

  // Debounced live value-search across configured fields. Empty input clears.
  useEffect(() => {
    if (searchTimerRef.current) window.clearTimeout(searchTimerRef.current);
    if (!searchInput.trim() || searchFields.length === 0) {
      setSearchResults(null);
      setSearchTruncated(false);
      setSearching(false);
      return;
    }
    setSearching(true);
    searchTimerRef.current = window.setTimeout(async () => {
      try {
        const r = await KVAPI.find(searchInput.trim(), searchFields, 200);
        setSearchResults(r.matches);
        setSearchTruncated(r.truncated);
      } catch (e: any) {
        pushToast(makeToast('error', `Search failed: ${e.message}`));
      } finally {
        setSearching(false);
      }
    }, 250);
    return () => {
      if (searchTimerRef.current) window.clearTimeout(searchTimerRef.current);
    };
  }, [searchInput, searchFields]);

  // Jump to a key from a search hit: select it, expand all ancestor branches.
  const jumpToKey = (key: string) => {
    const ancestors = ancestorBranches(key);
    setExpandedPaths((cur) => new Set([...cur, '', ...ancestors]));
    if (key.endsWith('/') || key.endsWith(':')) {
      setBranchPath(key);
      setSelectedKey(null);
    } else {
      setSelectedKey(key);
      setBranchPath(null);
    }
  };

  const breadcrumb = useMemo(() => {
    const target = selectedKey ?? branchPath ?? '';
    if (!target) return null;
    const parts: { label: string; path: string }[] = [];
    let i = 0;
    while (i < target.length) {
      let next = -1;
      for (const d of [':', '/']) {
        const idx = target.indexOf(d, i);
        if (idx >= 0 && (next < 0 || idx < next)) next = idx;
      }
      if (next < 0) {
        parts.push({ label: target.slice(i), path: target });
        break;
      }
      parts.push({ label: target.slice(i, next + 1), path: target.slice(0, next + 1) });
      i = next + 1;
    }
    return parts;
  }, [selectedKey, branchPath]);

  return (
    <div className="kv-app">
      <header className="kv-app-header">
        <div className="kv-app-search">
          <input
            type="search"
            className="kv-search-input"
            placeholder="filter by value across configured fields…"
            value={searchInput}
            onChange={(e) => setSearchInput(e.target.value)}
            autoFocus
          />
          {searchInput && (
            <button className="kv-btn-icon" onClick={() => setSearchInput('')} title="clear">×</button>
          )}
          <span className="kv-app-stats">
            {info ? `${info.keyCount} files · ${info.memoryUsage}` : ''}
          </span>
          <button
            className="kv-btn"
            onClick={() => setReloadToken((n) => n + 1)}
            title="refresh tree"
          >Refresh</button>
          <button
            className="kv-btn-icon kv-app-gear"
            onClick={() => setSnapshotOpen(true)}
            title="Snapshot — export / import / wipe"
            aria-label="Open snapshot settings"
          >
            ⚙
          </button>
        </div>
        <SearchFieldChips fields={searchFields} onChange={setSearchFields} />
        {breadcrumb && breadcrumb.length > 0 && (
          <div className="kv-app-breadcrumb">
            <span className="kv-crumb-label">path:</span>
            {breadcrumb.map((b, i) => (
              <button
                key={i}
                className="kv-crumb"
                onClick={() => jumpToKey(b.path)}
              >{b.label}</button>
            ))}
          </div>
        )}
      </header>

      <div className="kv-app-body">
        <aside className="kv-app-left">
          {searchResults !== null ? (
            <FilteredKVTree
              matches={searchResults}
              truncated={searchTruncated}
              searching={searching}
              selectedKey={selectedKey}
              expandedPaths={expandedPaths}
              onSelectLeaf={handleLeafClick}
              onSelectBranch={handleBranchClick}
              onToggleBranch={(p, willOpen) => {
                setExpandedPaths((cur) => {
                  const next = new Set(cur);
                  if (willOpen) next.add(p); else next.delete(p);
                  return next;
                });
              }}
            />
          ) : (
            <KVTree
              selectedKey={selectedKey}
              expandedPaths={expandedPaths}
              reloadToken={reloadToken}
              onSelectLeaf={handleLeafClick}
              onSelectBranch={handleBranchClick}
              onToggleBranch={(p, willOpen) => {
                setExpandedPaths((cur) => {
                  const next = new Set(cur);
                  if (willOpen) next.add(p); else next.delete(p);
                  return next;
                });
              }}
            />
          )}
        </aside>
        <main className="kv-app-right">
          {selectedKey ? (
            <KVLeafEditor
              selectedKey={selectedKey}
              reloadToken={reloadToken}
              onSaved={() => {
                pushToast(makeToast('success', `Saved ${selectedKey}`));
                setReloadToken((n) => n + 1);
              }}
              onDeleted={() => {
                pushToast(makeToast('success', `Deleted ${selectedKey}`));
                setSelectedKey(null);
                setReloadToken((n) => n + 1);
              }}
              onError={(msg) => pushToast(makeToast('error', msg))}
            />
          ) : branchPath && FILE_BRANCH_RE.test(branchPath) ? (
            <KVFileEditor
              key={branchPath}
              branchPath={branchPath}
              reloadToken={reloadToken}
              scrollToField={anchor}
              onChanged={(info, errors) => {
                pushToast(makeToast(errors && errors.length > 0 ? 'error' : 'success', info, errors));
                setReloadToken((n) => n + 1);
              }}
              onSelectLeaf={(k) => { setSelectedKey(k); setBranchPath(null); setAnchor(null); }}
            />
          ) : branchPath ? (
            <KVBranchEditor
              branchPath={branchPath}
              reloadToken={reloadToken}
              onChanged={(info, errors) => {
                pushToast(makeToast(errors && errors.length > 0 ? 'error' : 'success', info, errors));
                setReloadToken((n) => n + 1);
              }}
            />
          ) : (
            <div className="kv-pane-status kv-pane-empty">
              Select a key from the tree.
              <div className="kv-pane-hint">
                Click a branch to add / rename / delete a subtree, or a leaf to edit its value.
              </div>
            </div>
          )}
        </main>
      </div>

      {snapshotOpen && (
        <div
          className="kv-modal-backdrop"
          onClick={() => setSnapshotOpen(false)}
        >
          <div
            className="kv-modal"
            onClick={(e) => e.stopPropagation()}
            role="dialog"
            aria-modal="true"
          >
            <button
              className="kv-modal-close"
              onClick={() => setSnapshotOpen(false)}
              aria-label="Close"
              title="Close"
            >×</button>
            <SnapshotPanel />
          </div>
        </div>
      )}

      <ToastStack toasts={toasts} onDismiss={dismissToast} />
    </div>
  );
}

