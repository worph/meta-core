const API_BASE = '/api/snapshot';

export type ImportMode = 'replace' | 'merge';
export type ImportConflict = 'mine' | 'source';

export interface FileImportResult {
  hashId: string;
  added: number;
  updated: number;
  unchanged: number;
  kept: number;
  deleted: number;
  error?: string;
}

export interface PluginOutputResult {
  written: number;
  skipped: number;
}

export interface ImportResult {
  mode: ImportMode;
  conflict?: ImportConflict;
  dryRun: boolean;
  totalFiles: number;
  filesOk: number;
  filesFailed: number;
  files?: FileImportResult[];
  pluginOutput?: PluginOutputResult;
}

export interface WipeResult {
  metadataDeleted: number;
}

export class SnapshotAPI {
  /**
   * Trigger a download of a metadata snapshot ZIP.
   * Browser handles the download via a synthetic <a> click.
   */
  static downloadExport(includes: { pluginOutput?: boolean } = {}): void {
    const parts: string[] = [];
    if (includes.pluginOutput) parts.push('plugin-output');
    const qs = parts.length ? `?include=${parts.join(',')}` : '';
    const a = document.createElement('a');
    a.href = `${API_BASE}/export${qs}`;
    a.rel = 'noopener';
    document.body.appendChild(a);
    a.click();
    a.remove();
  }

  /**
   * Upload a snapshot ZIP and apply it.
   * Mode replace deletes and rewrites each CID's properties.
   * Mode merge preserves untouched fields; conflict picks who wins on overlap.
   */
  static async runImport(
    file: File,
    opts: { mode: ImportMode; conflict?: ImportConflict; dryRun?: boolean }
  ): Promise<ImportResult> {
    const params = new URLSearchParams();
    params.set('mode', opts.mode);
    if (opts.mode === 'merge') {
      params.set('conflict', opts.conflict ?? 'source');
    }
    if (opts.dryRun) {
      params.set('dry_run', 'true');
    }
    const fd = new FormData();
    fd.append('snapshot', file);
    const response = await fetch(`${API_BASE}/import?${params.toString()}`, {
      method: 'POST',
      body: fd,
    });
    if (!response.ok) {
      const err = await response.json().catch(() => ({}));
      throw new Error(err.message || err.error || 'Import failed');
    }
    return response.json();
  }

  /**
   * Wipe Redis metadata. Pass scope=metadata.
   */
  static async wipe(scope: 'metadata'): Promise<WipeResult> {
    const response = await fetch(`${API_BASE}/wipe?scope=${scope}`, {
      method: 'POST',
    });
    if (!response.ok) {
      const err = await response.json().catch(() => ({}));
      throw new Error(err.message || err.error || 'Wipe failed');
    }
    return response.json();
  }
}
