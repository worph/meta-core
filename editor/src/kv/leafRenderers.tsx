import { useEffect, useState } from 'react';
import { LanguageCombobox } from '../components/LanguageCombobox';
import { FieldType } from './types';

interface ValueRendererProps {
  value: string;
  onChange: (v: string) => void;
}

interface LeafValueEditorProps extends ValueRendererProps {
  type: FieldType;
}

/**
 * Type-dispatched leaf value editor. Used by both the single-leaf editor
 * (KVLeafEditor) and the per-row editor in the file-detail view.
 */
export function LeafValueEditor({ type, value, onChange }: LeafValueEditorProps) {
  switch (type) {
    case 'bool':
      return (
        <select
          className="kv-leaf-input"
          value={value === 'true' ? 'true' : value === 'false' ? 'false' : ''}
          onChange={(e) => onChange(e.target.value)}
        >
          <option value="">(unset)</option>
          <option value="true">true</option>
          <option value="false">false</option>
        </select>
      );
    case 'number':
      return (
        <input
          type="number"
          className="kv-leaf-input"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    case 'datetime':
      return <DateTimeLeaf value={value} onChange={onChange} />;
    case 'lang':
      return <LanguageCombobox value={value} onChange={onChange} />;
    case 'url':
      return (
        <div className="kv-leaf-url">
          <input
            type="url"
            className="kv-leaf-input"
            value={value}
            onChange={(e) => onChange(e.target.value)}
          />
          {value && (
            <a className="kv-leaf-url-open" href={value} target="_blank" rel="noreferrer">
              open ↗
            </a>
          )}
        </div>
      );
    case 'json':
      return (
        <textarea
          className="kv-leaf-textarea kv-leaf-mono"
          rows={12}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          spellCheck={false}
        />
      );
    case 'text':
      return (
        <textarea
          className="kv-leaf-textarea"
          rows={8}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    case 'hash':
      return <CidLeaf value={value} onChange={onChange} />;
    case 'auto':
    case 'string':
    default:
      return (
        <input
          type="text"
          className="kv-leaf-input"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      );
  }
}

interface CidFileInfo {
  exists: boolean;
  contentType?: string;
  size?: number;
  filePath?: string;
}

/**
 * CID leaf editor. Shows the value as an editable monospace input plus a
 * content-typed preview when meta-core can resolve the CID to a file on disk
 * (`/api/file/{cid}/info` → image/video/audio rendering, else download-only).
 */
function CidLeaf({ value, onChange }: ValueRendererProps) {
  const [info, setInfo] = useState<CidFileInfo | null>(null);
  const cid = value.trim();
  const fileUrl = cid ? `/file/${cid}` : '';
  const downloadName = info?.filePath?.split('/').pop() || cid;

  useEffect(() => {
    if (!cid) {
      setInfo(null);
      return;
    }
    let alive = true;
    fetch(`/api/file/${cid}/info`)
      .then((r) => (r.ok ? r.json() : { exists: false }))
      .then((data: CidFileInfo) => {
        if (alive) setInfo(data);
      })
      .catch(() => {
        if (alive) setInfo({ exists: false });
      });
    return () => {
      alive = false;
    };
  }, [cid]);

  const renderPreview = () => {
    if (!info?.exists || !info.contentType || !cid) return null;
    if (info.contentType.startsWith('image/')) {
      return <img src={fileUrl} alt="" className="kv-leaf-cid-preview" loading="lazy" />;
    }
    if (info.contentType.startsWith('video/')) {
      return (
        <video
          src={fileUrl}
          className="kv-leaf-cid-preview kv-leaf-cid-preview-media"
          controls
          preload="metadata"
        />
      );
    }
    if (info.contentType.startsWith('audio/')) {
      return (
        <audio
          src={fileUrl}
          className="kv-leaf-cid-preview-media"
          controls
          preload="metadata"
        />
      );
    }
    return null;
  };

  return (
    <div className="kv-leaf-cid">
      {renderPreview()}
      <div className="kv-leaf-cid-controls">
        <input
          type="text"
          className="kv-leaf-input kv-leaf-mono"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          spellCheck={false}
        />
        <button
          type="button"
          className="kv-btn-icon"
          onClick={() => navigator.clipboard.writeText(value)}
          title="copy CID"
        >
          ⧉
        </button>
      </div>
      {info?.exists && cid && (
        <div className="kv-leaf-cid-actions">
          <a
            href={fileUrl}
            download={downloadName}
            className="kv-btn kv-btn-primary"
            title={`Download ${downloadName}${info.size ? ` (${formatBytes(info.size)})` : ''}`}
          >
            ⬇ Download
          </a>
          <a href={fileUrl} className="kv-btn" target="_blank" rel="noreferrer">
            open ↗
          </a>
          <span className="kv-leaf-cid-meta">
            {info.contentType}
            {info.size != null ? ` · ${formatBytes(info.size)}` : ''}
          </span>
        </div>
      )}
    </div>
  );
}

/** Timestamp shapes we round-trip through the picker. */
type TimestampShape = 'iso-date' | 'iso-datetime' | 'unix-s' | 'unix-ms';

function detectShape(raw: string): TimestampShape | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;
  if (/^\d{4}-\d{2}-\d{2}$/.test(trimmed)) return 'iso-date';
  if (/^\d+$/.test(trimmed)) {
    const n = Number.parseInt(trimmed, 10);
    if (n >= 1_000_000_000 && n <= 4_102_444_800) return 'unix-s';
    if (n >= 1_000_000_000_000 && n <= 4_102_444_800_000) return 'unix-ms';
    return null;
  }
  if (!Number.isNaN(Date.parse(trimmed))) return 'iso-datetime';
  return null;
}

function rawToDate(raw: string, shape: TimestampShape): Date | null {
  const trimmed = raw.trim();
  if (!trimmed) return null;
  if (shape === 'unix-s') return new Date(Number.parseInt(trimmed, 10) * 1000);
  if (shape === 'unix-ms') return new Date(Number.parseInt(trimmed, 10));
  const d = new Date(trimmed);
  return Number.isNaN(d.getTime()) ? null : d;
}

function dateToRaw(d: Date, shape: TimestampShape): string {
  switch (shape) {
    case 'unix-s':
      return String(Math.floor(d.getTime() / 1000));
    case 'unix-ms':
      return String(d.getTime());
    case 'iso-date':
      return d.toISOString().slice(0, 10);
    case 'iso-datetime':
      return d.toISOString();
  }
}

/**
 * Datetime leaf editor. Preserves the original storage shape — ISO date stays
 * ISO date, unix seconds stay unix seconds, etc. Unknown shapes fall back to
 * iso-datetime.
 */
function DateTimeLeaf({ value, onChange }: ValueRendererProps) {
  const shape: TimestampShape = detectShape(value) ?? 'iso-datetime';
  const date = rawToDate(value, shape);
  const inputType = shape === 'iso-date' ? 'date' : 'datetime-local';

  const inputValue = (() => {
    if (!date || Number.isNaN(date.getTime())) return '';
    if (inputType === 'date') return date.toISOString().slice(0, 10);
    // datetime-local wants YYYY-MM-DDTHH:mm in the user's local timezone.
    const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000);
    return local.toISOString().slice(0, 16);
  })();

  return (
    <div className="kv-leaf-datetime">
      <input
        type={inputType}
        className="kv-leaf-input"
        value={inputValue}
        onChange={(e) => {
          const v = e.target.value;
          if (!v) {
            onChange('');
            return;
          }
          const parsed = new Date(v);
          if (Number.isNaN(parsed.getTime())) return;
          onChange(dateToRaw(parsed, shape));
        }}
      />
      <span className="kv-leaf-datetime-shape" title="Storage format preserved on save">
        {shape}
      </span>
    </div>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 10 ? 0 : 1)} ${units[i]}`;
}
