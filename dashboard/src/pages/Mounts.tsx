import { useEffect, useState } from 'react';

interface Mount {
  id: string;
  name: string;
  type: 'nfs' | 'smb' | 'rclone';
  enabled: boolean;
  desiredMounted: boolean;
  mountPath: string;
  mounted: boolean;
  error?: string;
  lastChecked: number;
}

interface RcloneRemote {
  name: string;
  type: string;
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

export default function Mounts() {
  const [mounts, setMounts] = useState<Mount[]>([]);
  const [remotes, setRemotes] = useState<RcloneRemote[]>([]);
  const [loading, setLoading] = useState(true);
  const [showForm, setShowForm] = useState(false);
  const [formData, setFormData] = useState({
    name: '',
    type: 'nfs' as 'nfs' | 'smb' | 'rclone',
    nfsServer: '',
    nfsPath: '',
    smbServer: '',
    smbShare: '',
    smbUsername: '',
    smbPassword: '',
    rcloneRemote: '',
    rclonePath: '',
  });

  const fetchMounts = async () => {
    try {
      const res = await fetch('/api/mounts');
      if (res.ok) {
        const data = await res.json();
        setMounts(data.mounts || []);
      }
    } catch (err) {
      console.error('Failed to fetch mounts:', err);
    } finally {
      setLoading(false);
    }
  };

  const fetchRemotes = async () => {
    try {
      const res = await fetch('/api/mounts/rclone/remotes');
      if (res.ok) {
        const data = await res.json();
        setRemotes(data.remotes || []);
      }
    } catch (err) {
      console.error('Failed to fetch remotes:', err);
    }
  };

  useEffect(() => {
    fetchMounts();
    fetchRemotes();
    const interval = setInterval(fetchMounts, 5000);
    return () => clearInterval(interval);
  }, []);

  const handleMount = async (id: string) => {
    await fetch(`/api/mounts/${id}/mount`, { method: 'POST' });
    fetchMounts();
  };

  const handleUnmount = async (id: string) => {
    await fetch(`/api/mounts/${id}/unmount`, { method: 'POST' });
    fetchMounts();
  };

  const handleDelete = async (id: string) => {
    if (!confirm('Are you sure you want to delete this mount?')) return;
    await fetch(`/api/mounts/${id}`, { method: 'DELETE' });
    fetchMounts();
  };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    const body: Record<string, unknown> = {
      name: formData.name,
      type: formData.type,
    };

    if (formData.type === 'nfs') {
      body.nfsServer = formData.nfsServer;
      body.nfsPath = formData.nfsPath;
    } else if (formData.type === 'smb') {
      body.smbServer = formData.smbServer;
      body.smbShare = formData.smbShare;
      if (formData.smbUsername) body.smbUsername = formData.smbUsername;
      if (formData.smbPassword) body.smbPassword = formData.smbPassword;
    } else if (formData.type === 'rclone') {
      body.rcloneRemote = formData.rcloneRemote;
      body.rclonePath = formData.rclonePath;
    }

    await fetch('/api/mounts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });

    setShowForm(false);
    setFormData({
      name: '',
      type: 'nfs',
      nfsServer: '',
      nfsPath: '',
      smbServer: '',
      smbShare: '',
      smbUsername: '',
      smbPassword: '',
      rcloneRemote: '',
      rclonePath: '',
    });
    fetchMounts();
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

  if (loading) return <div style={{ padding: '2rem' }}>Loading...</div>;

  return (
    <div style={{ padding: '2rem', maxWidth: '1200px', margin: '0 auto' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '2rem' }}>
        <h1>Mount Management</h1>
        <div style={{ display: 'flex', gap: '0.5rem' }}>
          <a
            href="/rclone/"
            target="_blank"
            rel="noopener noreferrer"
            style={{ ...buttonStyle, textDecoration: 'none', display: 'inline-flex', alignItems: 'center', gap: '0.5rem' }}
          >
            rclone UI
          </a>
          <button style={buttonStyle} onClick={() => setShowForm(!showForm)}>
            {showForm ? 'Cancel' : 'Add Mount'}
          </button>
        </div>
      </div>

      {showForm && (
        <div style={cardStyle}>
          <h3 style={{ marginBottom: '1rem' }}>Add New Mount</h3>
          <form onSubmit={handleCreate}>
            <input
              style={inputStyle}
              placeholder="Mount Name"
              value={formData.name}
              onChange={(e) => setFormData({ ...formData, name: e.target.value })}
              required
            />
            <select
              style={inputStyle}
              value={formData.type}
              onChange={(e) => setFormData({ ...formData, type: e.target.value as 'nfs' | 'smb' | 'rclone' })}
            >
              <option value="nfs">NFS</option>
              <option value="smb">SMB/CIFS</option>
              <option value="rclone">rclone</option>
            </select>

            {formData.type === 'nfs' && (
              <>
                <input
                  style={inputStyle}
                  placeholder="NFS Server (e.g., 192.168.1.100)"
                  value={formData.nfsServer}
                  onChange={(e) => setFormData({ ...formData, nfsServer: e.target.value })}
                  required
                />
                <input
                  style={inputStyle}
                  placeholder="NFS Path (e.g., /export/media)"
                  value={formData.nfsPath}
                  onChange={(e) => setFormData({ ...formData, nfsPath: e.target.value })}
                  required
                />
              </>
            )}

            {formData.type === 'smb' && (
              <>
                <input
                  style={inputStyle}
                  placeholder="SMB Server (e.g., 192.168.1.100)"
                  value={formData.smbServer}
                  onChange={(e) => setFormData({ ...formData, smbServer: e.target.value })}
                  required
                />
                <input
                  style={inputStyle}
                  placeholder="Share Name (e.g., media)"
                  value={formData.smbShare}
                  onChange={(e) => setFormData({ ...formData, smbShare: e.target.value })}
                  required
                />
                <input
                  style={inputStyle}
                  placeholder="Username (optional)"
                  value={formData.smbUsername}
                  onChange={(e) => setFormData({ ...formData, smbUsername: e.target.value })}
                />
                <input
                  style={inputStyle}
                  type="password"
                  placeholder="Password (optional)"
                  value={formData.smbPassword}
                  onChange={(e) => setFormData({ ...formData, smbPassword: e.target.value })}
                />
              </>
            )}

            {formData.type === 'rclone' && (
              <>
                <select
                  style={inputStyle}
                  value={formData.rcloneRemote}
                  onChange={(e) => setFormData({ ...formData, rcloneRemote: e.target.value })}
                  required
                >
                  <option value="">Select Remote</option>
                  {remotes.map((r) => (
                    <option key={r.name} value={r.name}>
                      {r.name} ({r.type})
                    </option>
                  ))}
                </select>
                <input
                  style={inputStyle}
                  placeholder="Remote Path (e.g., /path/to/folder)"
                  value={formData.rclonePath}
                  onChange={(e) => setFormData({ ...formData, rclonePath: e.target.value })}
                />
              </>
            )}

            <button type="submit" style={buttonStyle}>
              Create Mount
            </button>
          </form>
        </div>
      )}

      {mounts.length === 0 ? (
        <div style={cardStyle}>
          <p>No mounts configured. Click "Add Mount" to get started.</p>
        </div>
      ) : (
        mounts.map((mount) => (
          <div key={mount.id} style={cardStyle}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
              <div>
                <h3>{mount.name}</h3>
                <p style={{ color: '#888' }}>
                  Type: {mount.type} | Path: {mount.mountPath}
                </p>
                <p>
                  Status:{' '}
                  <span style={{ color: mount.mounted ? '#4ade80' : '#f87171' }}>
                    {mount.mounted ? 'Mounted' : 'Not mounted'}
                  </span>
                </p>
                {mount.error && (
                  <p style={{ color: '#f87171', marginTop: '0.5rem' }}>Error: {mount.error}</p>
                )}
              </div>
              <div>
                {mount.mounted ? (
                  <button style={buttonStyle} onClick={() => handleUnmount(mount.id)}>
                    Unmount
                  </button>
                ) : (
                  <button style={buttonStyle} onClick={() => handleMount(mount.id)}>
                    Mount
                  </button>
                )}
                <button style={dangerButtonStyle} onClick={() => handleDelete(mount.id)}>
                  Delete
                </button>
              </div>
            </div>
          </div>
        ))
      )}
    </div>
  );
}
