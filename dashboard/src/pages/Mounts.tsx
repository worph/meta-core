import { useEffect, useState } from 'react';

interface MountIOStats {
  source: 'rclone';
  bytesRead: number;
  bytesWritten?: number;
  readBps: number;
  writeBps?: number;
  readOps?: number;
  writeOps?: number;
  readIops?: number;
  writeIops?: number;
  transfersPerSec?: number;
  lastSampleAt: number;
  intervalMs?: number;
}

interface Mount {
  id: string;
  name: string;
  type: 'smb' | 'rclone';
  enabled: boolean;
  desiredMounted: boolean;
  mountPath: string;
  mounted: boolean;
  error?: string;
  lastChecked: number;
  // VFS cache configuration applied at mount time. Empty string means "use
  // server default" (50G / 24h / 5m).
  cacheMaxSize?: string;
  cacheMaxAge?: string;
  dirCacheTime?: string;
  pollingEnabled?: boolean;
  pollingIntervalMs?: number;
  pollingActive?: boolean;
  lastPolledScan?: number;
  // Adaptive scan progress (set by the meta-core poller)
  scanning?: boolean;
  currentScanStartedAt?: number; // ms; only while scanning
  lastScanDurationMs?: number;
  nextScanAt?: number; // ms; planned start of next adaptive scan
  // Live IO — populated by StatsPoller every ~5s. Daemon-global from rclone,
  // so the same numbers appear on every mount card.
  ioStats?: MountIOStats;
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

// Format byte counts/rates with K/M/G/T prefix (decimal — matches what users
// expect for network speeds; binary prefixes feel pedantic on a "MB/s"
// readout). Returns e.g. "12.4 MB", "850 KB", "2.3 GB".
function formatBytes(n: number): string {
  if (!isFinite(n) || n < 0) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return v >= 100 || i === 0 ? `${v.toFixed(0)} ${units[i]}` : `${v.toFixed(1)} ${units[i]}`;
}

function formatRate(bps: number): string {
  return `${formatBytes(bps)}/s`;
}

// Render a humanised duration: 5s, 1m 23s, 2h 14m. Sub-second collapses to 0s
// rather than "0ms" — at the second-tick cadence the UI uses, finer precision
// is just noise.
function formatDuration(ms: number): string {
  if (ms < 0 || !isFinite(ms)) return '—';
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  const remS = s % 60;
  if (m < 60) return remS ? `${m}m ${remS}s` : `${m}m`;
  const h = Math.floor(m / 60);
  const remM = m % 60;
  return remM ? `${h}h ${remM}m` : `${h}h`;
}

// IOStatsLine renders a single line of live throughput info beneath each
// mount card. Source is always rclone after the rclone-only consolidation,
// so we report file-level transfers/s rather than kernel IOPS.
function IOStatsLine({ stats }: { stats: MountIOStats }) {
  const opsLabel = 'transfers/s';
  const opsValue = stats.transfersPerSec;

  const ageMs = Date.now() - stats.lastSampleAt;
  const stale = ageMs > 30000; // >30s old means sampler hasn't ticked recently

  return (
    <p
      style={{
        color: stale ? '#6b7280' : '#9ca3af',
        fontSize: '0.85rem',
        margin: '0.25rem 0 0',
      }}
      title={`source: ${stats.source} · sample age: ${Math.round(ageMs / 1000)}s`}
    >
      ↓ {formatRate(stats.readBps)}
      {opsValue !== undefined && opsValue > 0 && (
        <> · {opsValue.toFixed(opsValue >= 10 ? 0 : 1)} {opsLabel}</>
      )}
      <> · {formatBytes(stats.bytesRead)} read total</>
      {stats.bytesWritten !== undefined && stats.bytesWritten > 0 && (
        <> · {formatBytes(stats.bytesWritten)} written</>
      )}
    </p>
  );
}

interface ScanProgressProps {
  mount: Mount;
  now: number;
}

// Progress bar shows scan ETA when running, an idle row with a countdown to
// the next adaptive scan when waiting. We can't show true % complete (we
// don't know the file count up-front), so the running bar is filled by
// elapsed/lastDuration capped at 95% — once we exceed the previous duration,
// it pins at 95% to signal "taking longer than last time".
function ScanProgress({ mount, now }: ScanProgressProps) {
  if (!mount.pollingEnabled) return null;

  const isScanning = !!mount.scanning && !!mount.currentScanStartedAt;
  const lastDuration = mount.lastScanDurationMs ?? 0;

  let fill = 0;
  let label = '';

  if (isScanning) {
    const elapsed = Math.max(0, now - (mount.currentScanStartedAt ?? now));
    if (lastDuration > 0) {
      fill = Math.min(0.95, elapsed / lastDuration);
      label = `Scanning… ${formatDuration(elapsed)} elapsed (last took ${formatDuration(lastDuration)})`;
    } else {
      fill = 0.4; // first scan ever — no ETA available, show indeterminate-ish
      label = `Scanning… ${formatDuration(elapsed)} elapsed (first scan, no ETA yet)`;
    }
  } else {
    const sinceLast = mount.lastPolledScan ? now - mount.lastPolledScan : 0;
    const untilNext = mount.nextScanAt ? Math.max(0, mount.nextScanAt - now) : 0;
    if (lastDuration > 0 && mount.lastPolledScan) {
      label = `Last scan ${formatDuration(sinceLast)} ago (took ${formatDuration(lastDuration)})`;
      if (mount.nextScanAt) label += ` • next in ${formatDuration(untilNext)}`;
    } else if (mount.nextScanAt) {
      label = `Waiting — next scan in ${formatDuration(untilNext)}`;
    } else {
      label = 'Polling enabled — waiting for first scan';
    }
  }

  return (
    <div style={{ marginTop: '0.5rem' }}>
      <div
        style={{
          height: '6px',
          background: '#0f3460',
          borderRadius: '3px',
          overflow: 'hidden',
          position: 'relative',
        }}
      >
        <div
          style={{
            width: `${Math.round(fill * 100)}%`,
            height: '100%',
            background: isScanning ? '#4ade80' : '#374151',
            transition: 'width 0.5s linear',
          }}
        />
      </div>
      <p style={{ color: '#9ca3af', fontSize: '0.8rem', margin: '0.25rem 0 0' }}>{label}</p>
    </div>
  );
}

export default function Mounts() {
  const [mounts, setMounts] = useState<Mount[]>([]);
  const [remotes, setRemotes] = useState<RcloneRemote[]>([]);
  const [loading, setLoading] = useState(true);
  const [showForm, setShowForm] = useState(false);
  const [formData, setFormData] = useState({
    name: '',
    type: 'smb' as 'smb' | 'rclone',
    smbServer: '',
    smbShare: '',
    smbUsername: '',
    smbPassword: '',
    rcloneRemote: '',
    rclonePath: '',
    cacheMaxSize: '',
    cacheMaxAge: '',
    dirCacheTime: '',
    pollingEnabled: true,
    pollingIntervalMs: 30000,
  });
  const [scanning, setScanning] = useState<string | null>(null);
  // 1Hz ticker so the progress bar's elapsed/countdown updates between API
  // refetches (which run every 5s).
  const [now, setNow] = useState<number>(() => Date.now());

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
    const tick = setInterval(() => setNow(Date.now()), 1000);
    return () => {
      clearInterval(interval);
      clearInterval(tick);
    };
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

  const handleScan = async (id: string) => {
    setScanning(id);
    try {
      await fetch(`/api/mounts/${id}/scan`, { method: 'POST' });
      fetchMounts();
    } finally {
      setScanning(null);
    }
  };

  const handleUpdatePolling = async (id: string, pollingEnabled: boolean, pollingIntervalMs?: number) => {
    const body: Record<string, unknown> = { pollingEnabled };
    if (pollingIntervalMs !== undefined) {
      body.pollingIntervalMs = pollingIntervalMs;
    }
    await fetch(`/api/mounts/${id}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    fetchMounts();
  };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    const body: Record<string, unknown> = {
      name: formData.name,
      type: formData.type,
    };

    if (formData.type === 'smb') {
      body.smbServer = formData.smbServer;
      body.smbShare = formData.smbShare;
      if (formData.smbUsername) body.smbUsername = formData.smbUsername;
      if (formData.smbPassword) body.smbPassword = formData.smbPassword;
    } else if (formData.type === 'rclone') {
      body.rcloneRemote = formData.rcloneRemote;
      body.rclonePath = formData.rclonePath;
    }

    // Cache knobs — only forward non-empty values; the server applies defaults
    // when fields are omitted.
    if (formData.cacheMaxSize) body.cacheMaxSize = formData.cacheMaxSize;
    if (formData.cacheMaxAge) body.cacheMaxAge = formData.cacheMaxAge;
    if (formData.dirCacheTime) body.dirCacheTime = formData.dirCacheTime;

    // Add polling configuration
    body.pollingEnabled = formData.pollingEnabled;
    if (formData.pollingEnabled && formData.pollingIntervalMs >= 5000) {
      body.pollingIntervalMs = formData.pollingIntervalMs;
    }

    await fetch('/api/mounts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });

    setShowForm(false);
    setFormData({
      name: '',
      type: 'smb',
      smbServer: '',
      smbShare: '',
      smbUsername: '',
      smbPassword: '',
      rcloneRemote: '',
      rclonePath: '',
      cacheMaxSize: '',
      cacheMaxAge: '',
      dirCacheTime: '',
      pollingEnabled: true,
      pollingIntervalMs: 30000,
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
              onChange={(e) => {
                const type = e.target.value as 'smb' | 'rclone';
                setFormData({ ...formData, type });
              }}
            >
              <option value="smb">SMB (via rclone)</option>
              <option value="rclone">rclone (pre-defined remote)</option>
            </select>

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

            <div style={{ marginTop: '1rem', borderTop: '1px solid #0f3460', paddingTop: '1rem' }}>
              <p style={{ color: '#9ca3af', fontSize: '0.85rem', marginTop: 0, marginBottom: '0.5rem' }}>
                VFS cache (leave blank for defaults: 50G / 24h / 5m)
              </p>
              <input
                style={inputStyle}
                placeholder="Cache max size (e.g., 50G)"
                value={formData.cacheMaxSize}
                onChange={(e) => setFormData({ ...formData, cacheMaxSize: e.target.value })}
              />
              <input
                style={inputStyle}
                placeholder="Cache max age (e.g., 24h)"
                value={formData.cacheMaxAge}
                onChange={(e) => setFormData({ ...formData, cacheMaxAge: e.target.value })}
              />
              <input
                style={inputStyle}
                placeholder="Dir cache time (e.g., 5m)"
                value={formData.dirCacheTime}
                onChange={(e) => setFormData({ ...formData, dirCacheTime: e.target.value })}
              />
            </div>

            <div style={{ marginTop: '1rem', borderTop: '1px solid #0f3460', paddingTop: '1rem' }}>
              <label style={{ display: 'flex', alignItems: 'center', marginBottom: '0.5rem', cursor: 'pointer' }}>
                <input
                  type="checkbox"
                  checked={formData.pollingEnabled}
                  onChange={(e) => setFormData({ ...formData, pollingEnabled: e.target.checked })}
                  style={{ marginRight: '0.5rem' }}
                />
                Enable Polling (auto-scan on interval)
              </label>
              {formData.pollingEnabled && (
                <input
                  style={inputStyle}
                  type="number"
                  min="5000"
                  placeholder="Polling Interval (ms)"
                  value={formData.pollingIntervalMs}
                  onChange={(e) => setFormData({ ...formData, pollingIntervalMs: parseInt(e.target.value) || 30000 })}
                />
              )}
            </div>

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
                {mount.pollingEnabled && (
                  <p style={{ color: '#888', fontSize: '0.9rem', margin: '0.25rem 0 0' }}>
                    Polling:{' '}
                    <span style={{ color: mount.pollingActive ? '#4ade80' : '#f59e0b' }}>
                      {mount.pollingActive ? 'Active' : 'Inactive'}
                    </span>
                    {' • Floor: '}{(mount.pollingIntervalMs || 30000) / 1000}s
                    {' • Adaptive 10× idle'}
                  </p>
                )}
                {mount.ioStats && <IOStatsLine stats={mount.ioStats} />}
                <ScanProgress mount={mount} now={now} />
                {mount.error && (
                  <p style={{ color: '#f87171', marginTop: '0.5rem' }}>Error: {mount.error}</p>
                )}
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
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
                {mount.mounted && (
                  <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
                    <button
                      style={{ ...buttonStyle, marginRight: 0 }}
                      onClick={() => handleScan(mount.id)}
                      disabled={scanning === mount.id}
                    >
                      {scanning === mount.id ? 'Scanning...' : 'Scan Now'}
                    </button>
                    <label style={{ display: 'flex', alignItems: 'center', fontSize: '0.85rem', cursor: 'pointer' }}>
                      <input
                        type="checkbox"
                        checked={mount.pollingEnabled || false}
                        onChange={(e) => handleUpdatePolling(mount.id, e.target.checked, mount.pollingIntervalMs)}
                        style={{ marginRight: '0.25rem' }}
                      />
                      Poll
                    </label>
                  </div>
                )}
              </div>
            </div>
          </div>
        ))
      )}
    </div>
  );
}
