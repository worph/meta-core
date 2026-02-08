import { useEffect, useState } from 'react';

interface Watcher {
  id: string;
  path: string;
  intervalMs: number;
  enabled: boolean;
  createdAt: number;
  active: boolean;
  lastScan: number;
  fileCount: number;
  isScanning: boolean;
}

const cardStyle: React.CSSProperties = {
  background: '#16213e',
  borderRadius: '8px',
  padding: '1.5rem',
  marginBottom: '1rem',
};

const buttonStyle: React.CSSProperties = {
  padding: '0.5rem 1rem',
  background: '#0f3460',
  color: '#fff',
  border: 'none',
  borderRadius: '4px',
  cursor: 'pointer',
  marginRight: '0.5rem',
};

const dangerButtonStyle: React.CSSProperties = {
  ...buttonStyle,
  background: '#7f1d1d',
};

const inputStyle: React.CSSProperties = {
  width: '100%',
  padding: '0.5rem',
  marginBottom: '0.5rem',
  background: '#1a1a2e',
  border: '1px solid #0f3460',
  borderRadius: '4px',
  color: '#fff',
};

function formatTimeAgo(timestamp: number): string {
  if (!timestamp) return 'Never';
  const seconds = Math.floor((Date.now() - timestamp) / 1000);
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return new Date(timestamp).toLocaleDateString();
}

export default function Watchers() {
  const [watchers, setWatchers] = useState<Watcher[]>([]);
  const [loading, setLoading] = useState(true);
  const [showForm, setShowForm] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [formData, setFormData] = useState({
    path: '',
    intervalMs: 30000,
    enabled: true,
  });
  const [scanning, setScanning] = useState<string | null>(null);
  const [scanningAll, setScanningAll] = useState(false);

  const fetchWatchers = async () => {
    try {
      const res = await fetch('/api/watchers');
      if (res.ok) {
        const data = await res.json();
        setWatchers(data.watchers || []);
      }
    } catch (err) {
      console.error('Failed to fetch watchers:', err);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchWatchers();
    const interval = setInterval(fetchWatchers, 5000);
    return () => clearInterval(interval);
  }, []);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();

    const body = {
      path: formData.path,
      intervalMs: formData.intervalMs,
      enabled: formData.enabled,
    };

    const res = await fetch('/api/watchers', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });

    if (res.ok) {
      setShowForm(false);
      setFormData({ path: '', intervalMs: 30000, enabled: true });
      fetchWatchers();
    }
  };

  const handleUpdate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!editingId) return;

    const body = {
      path: formData.path,
      intervalMs: formData.intervalMs,
      enabled: formData.enabled,
    };

    const res = await fetch(`/api/watchers/${editingId}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });

    if (res.ok) {
      setEditingId(null);
      setFormData({ path: '', intervalMs: 30000, enabled: true });
      fetchWatchers();
    }
  };

  const handleDelete = async (id: string) => {
    if (!confirm('Are you sure you want to delete this watcher?')) return;
    await fetch(`/api/watchers/${id}`, { method: 'DELETE' });
    fetchWatchers();
  };

  const handleScan = async (id: string) => {
    setScanning(id);
    try {
      await fetch(`/api/watchers/${id}/scan`, { method: 'POST' });
      fetchWatchers();
    } finally {
      setScanning(null);
    }
  };

  const handleScanAll = async () => {
    setScanningAll(true);
    try {
      await fetch('/api/watchers/scan-all', { method: 'POST' });
      fetchWatchers();
    } finally {
      setScanningAll(false);
    }
  };

  const handleToggleEnabled = async (id: string, enabled: boolean) => {
    await fetch(`/api/watchers/${id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled }),
    });
    fetchWatchers();
  };

  const startEdit = (watcher: Watcher) => {
    setEditingId(watcher.id);
    setFormData({
      path: watcher.path,
      intervalMs: watcher.intervalMs,
      enabled: watcher.enabled,
    });
    setShowForm(false);
  };

  const cancelEdit = () => {
    setEditingId(null);
    setFormData({ path: '', intervalMs: 30000, enabled: true });
  };

  if (loading) return <div style={{ padding: '2rem' }}>Loading...</div>;

  return (
    <div style={{ padding: '2rem', maxWidth: '1200px', margin: '0 auto' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '2rem' }}>
        <h1>Watchers</h1>
        <div style={{ display: 'flex', gap: '0.5rem' }}>
          <button
            style={buttonStyle}
            onClick={handleScanAll}
            disabled={scanningAll}
          >
            {scanningAll ? 'Scanning...' : 'Scan All Watchers'}
          </button>
          <button
            style={buttonStyle}
            onClick={() => {
              setShowForm(!showForm);
              setEditingId(null);
              setFormData({ path: '', intervalMs: 30000, enabled: true });
            }}
          >
            {showForm ? 'Cancel' : 'Add Watcher'}
          </button>
        </div>
      </div>

      {showForm && (
        <div style={cardStyle}>
          <h3 style={{ marginBottom: '1rem' }}>Add New Watcher</h3>
          <form onSubmit={handleCreate}>
            <input
              style={inputStyle}
              placeholder="Path (e.g., /files/watch)"
              value={formData.path}
              onChange={(e) => setFormData({ ...formData, path: e.target.value })}
              required
            />
            <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.5rem' }}>
              <input
                style={{ ...inputStyle, flex: 1, marginBottom: 0 }}
                type="number"
                min="5"
                placeholder="Interval (seconds)"
                value={formData.intervalMs / 1000}
                onChange={(e) => setFormData({ ...formData, intervalMs: parseInt(e.target.value) * 1000 || 30000 })}
              />
              <span style={{ color: '#888', alignSelf: 'center' }}>seconds</span>
            </div>
            <label style={{ display: 'flex', alignItems: 'center', marginBottom: '1rem', cursor: 'pointer' }}>
              <input
                type="checkbox"
                checked={formData.enabled}
                onChange={(e) => setFormData({ ...formData, enabled: e.target.checked })}
                style={{ marginRight: '0.5rem' }}
              />
              Enabled
            </label>
            <button type="submit" style={buttonStyle}>
              Create Watcher
            </button>
          </form>
        </div>
      )}

      {watchers.length === 0 ? (
        <div style={cardStyle}>
          <p>No watchers configured. Click "Add Watcher" to get started.</p>
        </div>
      ) : (
        watchers.map((watcher) => (
          <div key={watcher.id} style={cardStyle}>
            {editingId === watcher.id ? (
              <form onSubmit={handleUpdate}>
                <h3 style={{ marginBottom: '1rem' }}>Edit Watcher</h3>
                <input
                  style={inputStyle}
                  placeholder="Path (e.g., /files/watch)"
                  value={formData.path}
                  onChange={(e) => setFormData({ ...formData, path: e.target.value })}
                  required
                />
                <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.5rem' }}>
                  <input
                    style={{ ...inputStyle, flex: 1, marginBottom: 0 }}
                    type="number"
                    min="5"
                    placeholder="Interval (seconds)"
                    value={formData.intervalMs / 1000}
                    onChange={(e) => setFormData({ ...formData, intervalMs: parseInt(e.target.value) * 1000 || 30000 })}
                  />
                  <span style={{ color: '#888', alignSelf: 'center' }}>seconds</span>
                </div>
                <label style={{ display: 'flex', alignItems: 'center', marginBottom: '1rem', cursor: 'pointer' }}>
                  <input
                    type="checkbox"
                    checked={formData.enabled}
                    onChange={(e) => setFormData({ ...formData, enabled: e.target.checked })}
                    style={{ marginRight: '0.5rem' }}
                  />
                  Enabled
                </label>
                <button type="submit" style={buttonStyle}>
                  Save
                </button>
                <button type="button" style={buttonStyle} onClick={cancelEdit}>
                  Cancel
                </button>
              </form>
            ) : (
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                <div>
                  <h3 style={{ marginBottom: '0.5rem' }}>{watcher.path}</h3>
                  <p style={{ color: '#888', marginBottom: '0.25rem' }}>
                    Interval: {watcher.intervalMs / 1000}s
                    {' | '}
                    Last scan: {formatTimeAgo(watcher.lastScan)}
                    {watcher.fileCount > 0 && ` | ${watcher.fileCount} files`}
                  </p>
                  <p>
                    Status:{' '}
                    {watcher.isScanning ? (
                      <span style={{ color: '#f59e0b' }}>Scanning...</span>
                    ) : watcher.active ? (
                      <span style={{ color: '#4ade80' }}>Active</span>
                    ) : watcher.enabled ? (
                      <span style={{ color: '#f59e0b' }}>Pending</span>
                    ) : (
                      <span style={{ color: '#888' }}>Disabled</span>
                    )}
                  </p>
                </div>
                <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                  <div>
                    <button
                      style={{ ...buttonStyle, marginRight: 0 }}
                      onClick={() => handleScan(watcher.id)}
                      disabled={scanning === watcher.id || watcher.isScanning}
                    >
                      {scanning === watcher.id || watcher.isScanning ? 'Scanning...' : 'Scan Now'}
                    </button>
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <label style={{ display: 'flex', alignItems: 'center', fontSize: '0.85rem', cursor: 'pointer' }}>
                      <input
                        type="checkbox"
                        checked={watcher.enabled}
                        onChange={(e) => handleToggleEnabled(watcher.id, e.target.checked)}
                        style={{ marginRight: '0.25rem' }}
                      />
                      Enabled
                    </label>
                    <button style={buttonStyle} onClick={() => startEdit(watcher)}>
                      Edit
                    </button>
                    <button style={dangerButtonStyle} onClick={() => handleDelete(watcher.id)}>
                      Delete
                    </button>
                  </div>
                </div>
              </div>
            )}
          </div>
        ))
      )}
    </div>
  );
}
