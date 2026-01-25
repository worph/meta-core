import { useState, useEffect, useCallback } from 'react';
import './KVTreeView.css';

interface KeyValue {
  key: string;
  type?: string;
  value?: Record<string, any> | string;
  expanded?: boolean;
  loading?: boolean;
}

interface KVInfo {
  prefix: string;
  fileCount: number;
  keyCount: number;
  totalSize: number;
  memoryUsage?: string;
}

const API_BASE = '/api/kv';

export function KVTreeView() {
  const [keys, setKeys] = useState<KeyValue[]>([]);
  const [cursor, setCursor] = useState<string>('0');
  const [hasMore, setHasMore] = useState(true);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [info, setInfo] = useState<KVInfo | null>(null);

  // Load KV info on mount
  useEffect(() => {
    fetch(`${API_BASE}/info`)
      .then(res => res.json())
      .then(data => setInfo(data))
      .catch(err => console.error('Failed to load KV info:', err));
  }, []);

  // Load initial keys
  const loadKeys = useCallback(async (reset = false) => {
    if (loading) return;

    const currentCursor = reset ? '0' : cursor;
    if (!reset && currentCursor === '0' && keys.length > 0) return;

    setLoading(true);
    setError(null);

    try {
      const res = await fetch(`${API_BASE}/keys?cursor=${currentCursor}&count=50`);
      if (!res.ok) throw new Error('Failed to load keys');

      const data = await res.json();

      const newKeys: KeyValue[] = data.keys.map((key: string) => ({
        key,
        expanded: false,
        loading: false
      }));

      if (reset) {
        setKeys(newKeys);
      } else {
        setKeys(prev => [...prev, ...newKeys]);
      }

      setCursor(data.cursor);
      setHasMore(data.hasMore);
    } catch (err: any) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, [cursor, keys.length, loading]);

  // Load on mount
  useEffect(() => {
    loadKeys(true);
  }, []);

  // Toggle key expansion and load value
  const toggleKey = async (index: number) => {
    const key = keys[index];

    if (key.expanded) {
      // Collapse
      setKeys(prev => prev.map((k, i) =>
        i === index ? { ...k, expanded: false } : k
      ));
      return;
    }

    // Expand and load value if not already loaded
    if (key.value === undefined) {
      setKeys(prev => prev.map((k, i) =>
        i === index ? { ...k, loading: true } : k
      ));

      try {
        const res = await fetch(`${API_BASE}/key/${encodeURIComponent(key.key)}`);
        if (!res.ok) throw new Error('Failed to load value');

        const data = await res.json();

        setKeys(prev => prev.map((k, i) =>
          i === index ? {
            ...k,
            type: data.type,
            value: data.value,
            expanded: true,
            loading: false
          } : k
        ));
      } catch (err) {
        setKeys(prev => prev.map((k, i) =>
          i === index ? { ...k, loading: false, value: 'Error loading value' } : k
        ));
      }
    } else {
      setKeys(prev => prev.map((k, i) =>
        i === index ? { ...k, expanded: true } : k
      ));
    }
  };

  const formatValue = (value: any): string => {
    if (value === null) return 'null';
    if (value === undefined) return 'undefined';
    if (typeof value === 'object') {
      return JSON.stringify(value, null, 2);
    }
    return String(value);
  };

  const formatSize = (bytes: number): string => {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
    return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
  };

  return (
    <div className="kv-tree">
      <div className="kv-tree-header">
        <h3>KV Database Browser</h3>
        {info && (
          <div className="kv-tree-info">
            <span>Keys: {info.keyCount}</span>
            <span>Files: {formatSize(info.totalSize)}</span>
            {info.memoryUsage && <span>Memory: {info.memoryUsage}</span>}
          </div>
        )}
        <button
          className="kv-tree-refresh"
          onClick={() => loadKeys(true)}
          disabled={loading}
        >
          {loading ? 'Loading...' : 'Refresh'}
        </button>
      </div>

      {error && <div className="kv-tree-error">{error}</div>}

      <div className="kv-tree-content">
        {keys.map((item, index) => (
          <div key={item.key} className="kv-tree-node">
            <div
              className={`kv-tree-key ${item.expanded ? 'expanded' : ''}`}
              onClick={() => toggleKey(index)}
            >
              <span className="kv-tree-toggle">
                {item.loading ? '...' : (item.expanded ? '▼' : '▶')}
              </span>
              <span className="kv-tree-key-name">{item.key}</span>
              {item.type && <span className="kv-tree-type">{item.type}</span>}
            </div>

            {item.expanded && item.value && (
              <div className="kv-tree-value">
                {typeof item.value === 'object' ? (
                  <table className="kv-tree-fields">
                    <tbody>
                      {Object.entries(item.value).map(([field, val]) => (
                        <tr key={field}>
                          <td className="kv-field-name">{field}</td>
                          <td className="kv-field-value">
                            <pre>{formatValue(val)}</pre>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                ) : (
                  <pre>{formatValue(item.value)}</pre>
                )}
              </div>
            )}
          </div>
        ))}

        {hasMore && (
          <button
            className="kv-tree-load-more"
            onClick={() => loadKeys()}
            disabled={loading}
          >
            {loading ? 'Loading...' : 'Load More'}
          </button>
        )}

        {!loading && keys.length === 0 && (
          <div className="kv-tree-empty">No keys found</div>
        )}
      </div>
    </div>
  );
}
