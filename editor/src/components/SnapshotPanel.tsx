import { useState } from 'react';
import {
  SnapshotAPI,
  ImportMode,
  ImportConflict,
  ImportResult,
} from '../api/snapshotApi';
import './SnapshotPanel.css';

export function SnapshotPanel() {
  const [exportPluginOutput, setExportPluginOutput] = useState(false);
  const [importFile, setImportFile] = useState<File | null>(null);
  const [importMode, setImportMode] = useState<ImportMode>('merge');
  const [importConflict, setImportConflict] = useState<ImportConflict>('source');
  const [importing, setImporting] = useState(false);
  const [importResult, setImportResult] = useState<ImportResult | null>(null);
  const [importError, setImportError] = useState<string | null>(null);

  const [wipeBusy, setWipeBusy] = useState(false);
  const [wipeMessage, setWipeMessage] = useState<string | null>(null);

  const runImport = async (dryRun: boolean) => {
    if (!importFile) {
      setImportError('Pick a snapshot ZIP first.');
      return;
    }
    setImporting(true);
    setImportError(null);
    setImportResult(null);
    try {
      const result = await SnapshotAPI.runImport(importFile, {
        mode: importMode,
        conflict: importMode === 'merge' ? importConflict : undefined,
        dryRun,
      });
      setImportResult(result);
    } catch (e: any) {
      setImportError(e.message || String(e));
    } finally {
      setImporting(false);
    }
  };

  const runWipe = async () => {
    const ok = window.confirm(
      'This deletes ALL metadata in Redis. Files on disk are not touched, but every manual ' +
        'edit you made via this editor will be lost. Continue?'
    );
    if (!ok) return;
    setWipeBusy(true);
    setWipeMessage(null);
    try {
      const r = await SnapshotAPI.wipe('metadata');
      setWipeMessage(`Deleted ${r.metadataDeleted} file metadata entries.`);
    } catch (e: any) {
      setWipeMessage(`Failed: ${e.message || e}`);
    } finally {
      setWipeBusy(false);
    }
  };

  return (
    <div className="snapshot-panel">
      <h2>Snapshot</h2>
      <p className="snapshot-intro">
        Export, import, or wipe the metadata KV store. Snapshots are portable ZIP
        archives — one nested-JSON file per CID, plus a manifest.
      </p>

      <section className="snapshot-section">
        <h3>Export</h3>
        <p>Downloads every CID's metadata as a ZIP. Optional cache files can be added below.</p>
        <label className="snapshot-checkbox">
          <input
            type="checkbox"
            checked={exportPluginOutput}
            onChange={(e) => setExportPluginOutput(e.target.checked)}
          />
          <span>
            Include plugin output
            <span className="snapshot-hint">
              {' '}— TMDB posters/backdrops, extracted subtitles. Adds ~120 MB on a populated stack.
            </span>
          </span>
        </label>
        <button
          className="snapshot-btn primary"
          onClick={() =>
            SnapshotAPI.downloadExport({ pluginOutput: exportPluginOutput })
          }
        >
          Download snapshot.zip
        </button>
      </section>

      <section className="snapshot-section">
        <h3>Import</h3>
        <div className="snapshot-row">
          <label className="snapshot-file">
            <span>Snapshot ZIP</span>
            <input
              type="file"
              accept=".zip,application/zip"
              onChange={(e) => {
                setImportFile(e.target.files?.[0] ?? null);
                setImportResult(null);
                setImportError(null);
              }}
            />
          </label>
        </div>

        <div className="snapshot-row">
          <fieldset className="snapshot-fieldset">
            <legend>Mode</legend>
            <label>
              <input
                type="radio"
                name="mode"
                value="merge"
                checked={importMode === 'merge'}
                onChange={() => setImportMode('merge')}
              />
              Merge — keep existing fields not in the snapshot
            </label>
            <label>
              <input
                type="radio"
                name="mode"
                value="replace"
                checked={importMode === 'replace'}
                onChange={() => setImportMode('replace')}
              />
              Replace — wipe each imported CID and rewrite from snapshot
            </label>
          </fieldset>

          <fieldset className="snapshot-fieldset" disabled={importMode !== 'merge'}>
            <legend>Conflict</legend>
            <label>
              <input
                type="radio"
                name="conflict"
                value="source"
                checked={importConflict === 'source'}
                onChange={() => setImportConflict('source')}
              />
              Source wins (overwrite my edits)
            </label>
            <label>
              <input
                type="radio"
                name="conflict"
                value="mine"
                checked={importConflict === 'mine'}
                onChange={() => setImportConflict('mine')}
              />
              Mine wins (keep my edits, fill in only missing fields)
            </label>
          </fieldset>
        </div>

        <div className="snapshot-row">
          <button
            className="snapshot-btn"
            disabled={!importFile || importing}
            onClick={() => runImport(true)}
          >
            Dry run
          </button>
          <button
            className="snapshot-btn primary"
            disabled={!importFile || importing}
            onClick={() => runImport(false)}
          >
            {importing ? 'Importing…' : 'Apply import'}
          </button>
        </div>

        {importError && <div className="snapshot-error">{importError}</div>}
        {importResult && <ImportResultView result={importResult} />}
      </section>

      <section className="snapshot-section danger">
        <h3>Wipe metadata</h3>
        <p>
          Deletes every <code>file:*</code> key plus <code>file:__index__</code>.
          Files on disk are untouched.
        </p>
        <button
          className="snapshot-btn danger"
          disabled={wipeBusy}
          onClick={runWipe}
        >
          {wipeBusy ? 'Wiping…' : 'Wipe all metadata'}
        </button>
        {wipeMessage && <div className="snapshot-info">{wipeMessage}</div>}
      </section>
    </div>
  );
}

function ImportResultView({ result }: { result: ImportResult }) {
  const totals = (result.files ?? []).reduce(
    (acc, f) => {
      acc.added += f.added;
      acc.updated += f.updated;
      acc.unchanged += f.unchanged;
      acc.kept += f.kept;
      acc.deleted += f.deleted;
      return acc;
    },
    { added: 0, updated: 0, unchanged: 0, kept: 0, deleted: 0 }
  );
  return (
    <div className="snapshot-result">
      <div className="snapshot-result-summary">
        <strong>{result.dryRun ? 'Dry run preview' : 'Import applied'}</strong>
        <span>
          {result.filesOk} ok / {result.filesFailed} failed of {result.totalFiles} files
        </span>
        <span>
          mode={result.mode}
          {result.conflict ? `, conflict=${result.conflict}` : ''}
        </span>
      </div>
      <table className="snapshot-result-table">
        <thead>
          <tr>
            <th>added</th>
            <th>updated</th>
            <th>unchanged</th>
            <th>kept (mine)</th>
            <th>deleted (replace)</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td>{totals.added}</td>
            <td>{totals.updated}</td>
            <td>{totals.unchanged}</td>
            <td>{totals.kept}</td>
            <td>{totals.deleted}</td>
          </tr>
        </tbody>
      </table>
      {result.pluginOutput && (
        <div className="snapshot-result-aside">
          plugin-output: {result.pluginOutput.written} written
          {result.pluginOutput.skipped > 0
            ? `, ${result.pluginOutput.skipped} skipped (existed)`
            : ''}
        </div>
      )}
      {result.files && result.files.some((f) => f.error) && (
        <details className="snapshot-result-errors">
          <summary>Per-file errors</summary>
          <ul>
            {result.files
              .filter((f) => f.error)
              .map((f) => (
                <li key={f.hashId}>
                  <code>{f.hashId}</code>: {f.error}
                </li>
              ))}
          </ul>
        </details>
      )}
    </div>
  );
}
