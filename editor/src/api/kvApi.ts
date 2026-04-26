// Client for the meta-core /api/kv endpoints. The keyspace is a slash-tree of
// string-valued keys; this module wraps the S3-style listing, search, and
// per-key get/put/delete operations.

const API_BASE = '/api/kv';

export interface KVTreeResponse {
  prefix: string;
  delimiter: string;
  branches: string[];
  leaves: string[];
  cursor: string;
  hasMore: boolean;
}

export interface KVSearchResponse {
  contains: string;
  keys: string[];
  truncated: boolean;
}

export interface KVFindMatch {
  key: string;
  value: string;
  entryPath: string;
  field: string;
}

export interface KVFindResponse {
  contains: string;
  fields: string[];
  matches: KVFindMatch[];
  truncated: boolean;
}

export interface KVValueResponse {
  key: string;
  type: string;
  value: string;
  exists: boolean;
}

export interface KVInfoResponse {
  prefix: string;
  fileCount: number;
  keyCount: number;
  totalSize: number;
  memoryUsage: string;
}

async function jsonOrThrow<T>(res: Response): Promise<T> {
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const body = await res.json();
      if (body?.message) msg = body.message;
      else if (body?.error) msg = body.error;
    } catch {}
    throw new Error(msg);
  }
  return res.json();
}

export const KVAPI = {
  async info(): Promise<KVInfoResponse> {
    return jsonOrThrow(await fetch(`${API_BASE}/info`));
  },

  async tree(params: {
    prefix?: string;
    delimiter?: string;
    cursor?: string;
    max?: number;
  }): Promise<KVTreeResponse> {
    const q = new URLSearchParams();
    if (params.prefix !== undefined) q.set('prefix', params.prefix);
    if (params.delimiter !== undefined) q.set('delimiter', params.delimiter);
    if (params.cursor) q.set('cursor', params.cursor);
    if (params.max) q.set('max', String(params.max));
    return jsonOrThrow(await fetch(`${API_BASE}/tree?${q}`));
  },

  // Walk multiple SCAN cycles until the keyspace is exhausted or a soft cap
  // is reached. Useful for branches with up to a few thousand children where
  // pagination UX would just be a worse spinner.
  async treeAll(prefix: string, delimiter: string, softCap = 5000): Promise<KVTreeResponse> {
    const seenLeaves = new Set<string>();
    const seenBranches = new Set<string>();
    let cursor = '';
    while (true) {
      const page = await this.tree({ prefix, delimiter, cursor, max: 1000 });
      for (const k of page.leaves) seenLeaves.add(k);
      for (const k of page.branches) seenBranches.add(k);
      cursor = page.cursor;
      if (!page.hasMore) break;
      if (seenLeaves.size + seenBranches.size >= softCap) break;
    }
    return {
      prefix,
      delimiter,
      branches: [...seenBranches].sort(),
      leaves: [...seenLeaves].sort(),
      cursor,
      hasMore: cursor !== '0' && cursor !== '',
    };
  },

  async search(contains: string, limit = 500): Promise<KVSearchResponse> {
    const q = new URLSearchParams({ contains, limit: String(limit) });
    return jsonOrThrow(await fetch(`${API_BASE}/search?${q}`));
  },

  async find(contains: string, fields: string[], limit = 200): Promise<KVFindResponse> {
    const q = new URLSearchParams({ contains, fields: fields.join(','), limit: String(limit) });
    return jsonOrThrow(await fetch(`${API_BASE}/find?${q}`));
  },

  async get(key: string): Promise<KVValueResponse> {
    const q = new URLSearchParams({ key });
    return jsonOrThrow(await fetch(`${API_BASE}/value?${q}`));
  },

  async put(key: string, value: string): Promise<KVValueResponse> {
    const res = await fetch(`${API_BASE}/value`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ key, value }),
    });
    return jsonOrThrow(res);
  },

  async del(key: string): Promise<{ key: string; deleted: boolean }> {
    const q = new URLSearchParams({ key });
    return jsonOrThrow(await fetch(`${API_BASE}/value?${q}`, { method: 'DELETE' }));
  },
};
