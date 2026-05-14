import { useEffect, useState } from 'react';
import { KVAPI } from '../api/kvApi';

const FILE_BRANCH_RE = /^file:[^/]+\/$/;
const fileNameCache = new Map<string, string | null>();
const fileNamePromises = new Map<string, Promise<void>>();
const fileNameSubscribers = new Set<() => void>();

function notifyFileNameSubscribers() {
  for (const cb of fileNameSubscribers) cb();
}

function ensureFileName(branchPath: string): void {
  if (fileNameCache.has(branchPath) || fileNamePromises.has(branchPath)) return;
  const p = KVAPI.get(branchPath + 'fileName')
    .then((r) => {
      fileNameCache.set(branchPath, r.exists && r.value ? r.value : null);
    })
    .catch(() => {
      fileNameCache.set(branchPath, null);
    })
    .finally(() => {
      fileNamePromises.delete(branchPath);
      notifyFileNameSubscribers();
    });
  fileNamePromises.set(branchPath, p);
}

function useFileName(branchPath: string | null): string | null {
  const [, force] = useState(0);
  useEffect(() => {
    if (!branchPath || !FILE_BRANCH_RE.test(branchPath)) return;
    ensureFileName(branchPath);
    const cb = () => force((n) => n + 1);
    fileNameSubscribers.add(cb);
    return () => {
      fileNameSubscribers.delete(cb);
    };
  }, [branchPath]);
  if (!branchPath || !FILE_BRANCH_RE.test(branchPath)) return null;
  return fileNameCache.get(branchPath) ?? null;
}

function isFileBranch(path: string): boolean {
  return FILE_BRANCH_RE.test(path);
}

function shortHashFromFileBranch(path: string): string {
  // path = "file:<hash>/"  →  last 6 chars of hash
  const hash = path.slice('file:'.length, -1);
  if (hash.length <= 12) return hash;
  return `…${hash.slice(-6)}`;
}

// A node in the lazy tree. branch-typed nodes have an associated path that
// ends with the delimiter and lazy-load their children on first expand.
type NodeKind = 'branch' | 'leaf';

interface TreeNode {
  kind: NodeKind;
  // Display label: the segment of the path under the parent.
  label: string;
  // Full key path. For branches, ends with the delimiter.
  path: string;
}

interface BranchState {
  loaded: boolean;
  loading: boolean;
  error?: string;
  children: TreeNode[];
}

// Sub-segment of a path used as the visible label for a branch / leaf.
// Splits on either ":" or "/" so file:abc/title/eng becomes file: > abc > title > eng.
function labelOf(path: string, parentPath: string): string {
  const rest = path.slice(parentPath.length);
  // The "rest" includes a trailing delimiter for branches; strip it for display.
  return rest.replace(/[/:]+$/, '');
}

// Hide internal Redis structures from the tree:
//   - the `meta:` namespace (event streams, internal state)
//   - bare top-level leaves under a colon-namespace like file:__index__ and
//     file:events (Redis sets / streams that aren't user data).
function isHiddenNode(node: TreeNode): boolean {
  if (node.kind === 'branch' && node.path === 'meta:') return true;
  if (node.kind === 'leaf' && /^[^/]+:[^/]+$/.test(node.path)) return true;
  return false;
}

interface Props {
  // Selected leaf path (full key) — highlighted.
  selectedKey: string | null;
  // When set, the tree forces these branches open (used for auto-expand on
  // search hit or deep-link).
  expandedPaths: Set<string>;
  onSelectLeaf: (key: string) => void;
  onSelectBranch: (path: string) => void;
  onToggleBranch: (path: string, willOpen: boolean) => void;
  // Bumping this triggers a full reload of root and any expanded branches.
  reloadToken: number;
}

export function KVTree({
  selectedKey,
  expandedPaths,
  onSelectLeaf,
  onSelectBranch,
  onToggleBranch,
  reloadToken,
}: Props) {
  // Map of branch path → load state. The empty key "" represents the root.
  const [branches, setBranches] = useState<Record<string, BranchState>>({});

  // Load (or reload) a branch's children from the API. The root is special-
  // cased: we list at prefix="" with delimiter=":" to surface the namespaces.
  const loadBranch = async (path: string) => {
    setBranches((b) => ({
      ...b,
      [path]: { loaded: false, loading: true, children: b[path]?.children ?? [] },
    }));
    try {
      const isRoot = path === '';
      const delim = isRoot ? ':' : '/';
      const resp = await KVAPI.treeAll(path, delim, 5000);
      const branchNodes: TreeNode[] = resp.branches.map((p) => ({
        kind: 'branch',
        label: labelOf(p, path) + delim,
        path: p,
      }));
      const leafNodes: TreeNode[] = resp.leaves.map((p) => ({
        kind: 'leaf',
        label: labelOf(p, path),
        path: p,
      }));
      const children = [...branchNodes, ...leafNodes];
      setBranches((b) => ({
        ...b,
        [path]: { loaded: true, loading: false, children },
      }));
    } catch (err: any) {
      setBranches((b) => ({
        ...b,
        [path]: { loaded: false, loading: false, children: [], error: err.message },
      }));
    }
  };

  // On reloadToken change, refetch root and every currently-expanded branch.
  useEffect(() => {
    loadBranch('');
    for (const p of expandedPaths) {
      if (p !== '') loadBranch(p);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reloadToken]);

  // Whenever expandedPaths gains a path we haven't loaded yet, lazy-load it.
  useEffect(() => {
    for (const p of expandedPaths) {
      if (p === '') continue;
      const state = branches[p];
      if (!state || (!state.loaded && !state.loading)) {
        loadBranch(p);
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [expandedPaths]);

  return (
    <div className="kv-tree">
      <Branch
        path=""
        state={branches['']}
        depth={0}
        expandedPaths={expandedPaths}
        branches={branches}
        selectedKey={selectedKey}
        onSelectLeaf={onSelectLeaf}
        onSelectBranch={onSelectBranch}
        onToggleBranch={(p, open) => {
          onToggleBranch(p, open);
          if (open && (!branches[p] || !branches[p].loaded)) loadBranch(p);
        }}
      />
    </div>
  );
}

interface BranchProps {
  path: string;
  state: BranchState | undefined;
  depth: number;
  expandedPaths: Set<string>;
  branches: Record<string, BranchState>;
  selectedKey: string | null;
  onSelectLeaf: (key: string) => void;
  onSelectBranch: (path: string) => void;
  onToggleBranch: (path: string, willOpen: boolean) => void;
}

function Branch({
  state, depth, expandedPaths, branches,
  selectedKey, onSelectLeaf, onSelectBranch, onToggleBranch,
}: BranchProps) {
  if (!state) {
    return null;
  }
  if (state.loading && state.children.length === 0) {
    return <div className="kv-tree-loading" style={{ paddingLeft: depth * 14 + 18 }}>loading…</div>;
  }
  if (state.error) {
    return <div className="kv-tree-error" style={{ paddingLeft: depth * 14 + 18 }}>error: {state.error}</div>;
  }
  const visibleChildren = state.children.filter((c) => !isHiddenNode(c));
  if (visibleChildren.length === 0) {
    return <div className="kv-tree-empty" style={{ paddingLeft: depth * 14 + 18 }}>(empty)</div>;
  }
  return (
    <ul className="kv-tree-list">
      {visibleChildren.map((node) => {
        if (node.kind === 'branch') {
          const open = expandedPaths.has(node.path);
          const childState = branches[node.path];
          return (
            <li key={node.path}>
              <BranchRow
                node={node}
                open={open}
                depth={depth}
                onToggle={() => {
                  onToggleBranch(node.path, !open);
                  onSelectBranch(node.path);
                }}
              />
              {open && (
                <Branch
                  path={node.path}
                  state={childState}
                  depth={depth + 1}
                  expandedPaths={expandedPaths}
                  branches={branches}
                  selectedKey={selectedKey}
                  onSelectLeaf={onSelectLeaf}
                  onSelectBranch={onSelectBranch}
                  onToggleBranch={onToggleBranch}
                />
              )}
            </li>
          );
        }
        const isSelected = node.path === selectedKey;
        return (
          <li key={node.path}>
            <div
              className={`kv-tree-row kv-tree-leaf${isSelected ? ' selected' : ''}`}
              style={{ paddingLeft: depth * 14 + 4 }}
              onClick={() => onSelectLeaf(node.path)}
            >
              <span className="kv-tree-leaf-dot">•</span>
              <span className="kv-tree-label">{node.label}</span>
            </div>
          </li>
        );
      })}
    </ul>
  );
}

interface BranchRowProps {
  node: TreeNode;
  open: boolean;
  depth: number;
  onToggle: () => void;
}

function BranchRow({ node, open, depth, onToggle }: BranchRowProps) {
  // Surface the actual filename next to file:<hash>/ branches so the tree
  // reads like a file list at that level instead of a wall of hashes.
  const fileName = useFileName(isFileBranch(node.path) ? node.path : null);

  return (
    <div
      className={`kv-tree-row kv-tree-branch${open ? ' open' : ''}`}
      style={{ paddingLeft: depth * 14 + 4 }}
      onClick={onToggle}
      title={isFileBranch(node.path) ? node.path : undefined}
    >
      <span className="kv-tree-toggle">{open ? '▾' : '▸'}</span>
      {fileName ? (
        <>
          <span className="kv-tree-label kv-tree-filename">{fileName}</span>
          <span className="kv-tree-hash-pill">{shortHashFromFileBranch(node.path)}</span>
        </>
      ) : (
        <span className="kv-tree-label">{node.label}</span>
      )}
    </div>
  );
}

// Helper: given a key path with both ":" and "/" separators, return the list
// of ancestor branch paths that need to be expanded to reveal it.
export function ancestorBranches(key: string): string[] {
  const out: string[] = [];
  let i = 0;
  while (i < key.length) {
    const next = nextSep(key, i);
    if (next < 0) break;
    out.push(key.slice(0, next + 1));
    i = next + 1;
  }
  return out;
}

function nextSep(s: string, from: number): number {
  let best = -1;
  for (const d of [':', '/']) {
    const idx = s.indexOf(d, from);
    if (idx >= 0 && (best < 0 || idx < best)) best = idx;
  }
  return best;
}

// Re-export for convenience in the shell
export type { TreeNode };
