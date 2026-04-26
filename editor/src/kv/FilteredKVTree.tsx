import { useEffect, useMemo, useState } from 'react';
import { KVAPI, KVFindMatch } from '../api/kvApi';

interface Props {
  matches: KVFindMatch[];
  truncated: boolean;
  searching: boolean;
  selectedKey: string | null;
  expandedPaths: Set<string>;
  onSelectLeaf: (key: string) => void;
  onSelectBranch: (path: string) => void;
  onToggleBranch: (path: string, willOpen: boolean) => void;
}

// FilteredKVTree is the search-active counterpart to KVTree. Instead of
// replacing the tree with a flat results list, it shows only the file
// branches that contain matches, with the matched (field, value) pairs
// rendered inline as sub-lines under each branch.
//
// Branches remain expandable: clicking one fetches the full subtree via
// KVAPI.treeAll just like the main tree, so the user can drill into every
// key of a matching file without clearing the search first.
export function FilteredKVTree({
  matches, truncated, searching,
  selectedKey, expandedPaths,
  onSelectLeaf, onSelectBranch, onToggleBranch,
}: Props) {
  // Group matches by their entryPath (file:<hash>/) so each entry is one row
  // with potentially multiple hit previews.
  const groups = useMemo(() => {
    const m = new Map<string, KVFindMatch[]>();
    for (const x of matches) {
      const arr = m.get(x.entryPath);
      if (arr) arr.push(x);
      else m.set(x.entryPath, [x]);
    }
    return m;
  }, [matches]);

  return (
    <div className="kv-filtered">
      <div className="kv-search-header">
        {searching
          ? 'searching…'
          : `${matches.length} match${matches.length === 1 ? '' : 'es'} in ${groups.size} file${groups.size === 1 ? '' : 's'}${truncated ? ' (truncated)' : ''}`}
      </div>
      {[...groups.entries()]
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([entryPath, hits]) => (
          <FilteredEntry
            key={entryPath}
            entryPath={entryPath}
            hits={hits}
            selectedKey={selectedKey}
            expanded={expandedPaths.has(entryPath)}
            onSelectLeaf={onSelectLeaf}
            onSelectBranch={onSelectBranch}
            onToggleBranch={onToggleBranch}
          />
        ))}
    </div>
  );
}

interface EntryProps {
  entryPath: string;
  hits: KVFindMatch[];
  selectedKey: string | null;
  expanded: boolean;
  onSelectLeaf: (key: string) => void;
  onSelectBranch: (path: string) => void;
  onToggleBranch: (path: string, willOpen: boolean) => void;
}

function FilteredEntry({
  entryPath, hits, selectedKey, expanded,
  onSelectLeaf, onSelectBranch, onToggleBranch,
}: EntryProps) {
  // We deliberately do NOT mark the matched-leaf rows as "selected" via the
  // tree row's selected style — the inline hit lines are themselves the
  // selected indicator (they're highlighted when their key is current).
  const isBranchSelected = entryPath === selectedKey;
  const HIT_PREVIEW = 4;

  return (
    <div className="kv-filtered-entry">
      <div
        className={`kv-tree-row kv-tree-branch${isBranchSelected ? ' selected' : ''}`}
        style={{ paddingLeft: 4 }}
        onClick={() => {
          onToggleBranch(entryPath, !expanded);
          onSelectBranch(entryPath);
        }}
      >
        <span className="kv-tree-toggle">{expanded ? '▾' : '▸'}</span>
        <span className="kv-tree-label">{entryPath}</span>
        <span className="kv-filtered-count" title={`${hits.length} match${hits.length === 1 ? '' : 'es'}`}>
          {hits.length}
        </span>
      </div>

      <div className="kv-filtered-hits">
        {hits.slice(0, HIT_PREVIEW).map((h) => (
          <div
            key={h.key}
            className={`kv-filtered-hit${h.key === selectedKey ? ' selected' : ''}`}
            onClick={(e) => { e.stopPropagation(); onSelectLeaf(h.key); }}
            title={h.value}
          >
            <span className="kv-filtered-hit-field">{h.field}</span>
            <span className="kv-filtered-hit-value">{h.value}</span>
          </div>
        ))}
        {hits.length > HIT_PREVIEW && (
          <div className="kv-filtered-more">
            +{hits.length - HIT_PREVIEW} more match{hits.length - HIT_PREVIEW === 1 ? '' : 'es'}
            {!expanded && ' · expand to see all keys'}
          </div>
        )}
      </div>

      {expanded && (
        <LazySubtree
          path={entryPath}
          depth={1}
          selectedKey={selectedKey}
          onSelectLeaf={onSelectLeaf}
          onSelectBranch={onSelectBranch}
        />
      )}
    </div>
  );
}

interface SubtreeProps {
  path: string;
  depth: number;
  selectedKey: string | null;
  onSelectLeaf: (key: string) => void;
  onSelectBranch: (path: string) => void;
}

interface SubtreeData {
  loading: boolean;
  branches: string[];
  leaves: string[];
  error?: string;
}

// LazySubtree fetches its children once on mount and renders them. Sub-
// branches are themselves LazyBranch components that fetch their own
// children on first expand — recursion all the way down without the global
// expandedPaths state of the main KVTree.
function LazySubtree({ path, depth, selectedKey, onSelectLeaf, onSelectBranch }: SubtreeProps) {
  const [data, setData] = useState<SubtreeData>({ loading: true, branches: [], leaves: [] });

  useEffect(() => {
    let cancelled = false;
    KVAPI.treeAll(path, '/', 5000)
      .then((r) => { if (!cancelled) setData({ loading: false, branches: r.branches, leaves: r.leaves }); })
      .catch((e) => { if (!cancelled) setData({ loading: false, branches: [], leaves: [], error: e.message }); });
    return () => { cancelled = true; };
  }, [path]);

  if (data.loading) {
    return <div className="kv-tree-loading" style={{ paddingLeft: depth * 14 + 18 }}>loading…</div>;
  }
  if (data.error) {
    return <div className="kv-tree-error" style={{ paddingLeft: depth * 14 + 18 }}>{data.error}</div>;
  }

  return (
    <>
      {data.branches.map((b) => (
        <LazyBranch
          key={b}
          path={b}
          parentPath={path}
          depth={depth}
          selectedKey={selectedKey}
          onSelectLeaf={onSelectLeaf}
          onSelectBranch={onSelectBranch}
        />
      ))}
      {data.leaves.map((l) => (
        <div
          key={l}
          className={`kv-tree-row kv-tree-leaf${l === selectedKey ? ' selected' : ''}`}
          style={{ paddingLeft: depth * 14 + 4 }}
          onClick={() => onSelectLeaf(l)}
        >
          <span className="kv-tree-leaf-dot">•</span>
          <span className="kv-tree-label">{l.slice(path.length)}</span>
        </div>
      ))}
    </>
  );
}

interface LazyBranchProps {
  path: string;
  parentPath: string;
  depth: number;
  selectedKey: string | null;
  onSelectLeaf: (key: string) => void;
  onSelectBranch: (path: string) => void;
}

function LazyBranch({ path, parentPath, depth, selectedKey, onSelectLeaf, onSelectBranch }: LazyBranchProps) {
  const [expanded, setExpanded] = useState(false);
  const label = path.slice(parentPath.length);
  return (
    <>
      <div
        className={`kv-tree-row kv-tree-branch${path === selectedKey ? ' selected' : ''}`}
        style={{ paddingLeft: depth * 14 + 4 }}
        onClick={() => { setExpanded(!expanded); onSelectBranch(path); }}
      >
        <span className="kv-tree-toggle">{expanded ? '▾' : '▸'}</span>
        <span className="kv-tree-label">{label}</span>
      </div>
      {expanded && (
        <LazySubtree
          path={path}
          depth={depth + 1}
          selectedKey={selectedKey}
          onSelectLeaf={onSelectLeaf}
          onSelectBranch={onSelectBranch}
        />
      )}
    </>
  );
}
