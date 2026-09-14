import { useEffect, useState } from 'react';
import { getJSON } from '../lib/api';

interface HealthStatus {
  status: string;
  redis: boolean;
  timestamp: string;
}

interface FileStats {
  count: number;
  totalSize: number;
}

interface Stats {
  records: number;
  redisKeys: number;
  redisMemory: string;
  identities: number;
  files: FileStats | null;
  udlUsers: number | null;
  sweptAt: number;
  sweeping: boolean;
}

interface WatcherStatus {
  id: string;
  path: string;
  intervalMs: number;
  enabled: boolean;
  active: boolean;
  lastScan: number;
  fileCount: number;
  isScanning: boolean;
}

interface WatchersResponse {
  watchers: WatcherStatus[];
  count: number;
}

const cardStyle: React.CSSProperties = {
  background: '#16213e',
  borderRadius: '8px',
  padding: '1.5rem',
  marginBottom: '1rem',
};

const gridStyle: React.CSSProperties = {
  display: 'grid',
  // 200px, not 220: five tiles must fit one row inside the 1200px container
  // (1136 content - 4x16 gap = 1072 / 5 = 214 each) before wrapping.
  gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))',
  gap: '1rem',
  marginBottom: '1rem',
};

const statStyle: React.CSSProperties = {
  fontSize: '2rem',
  fontWeight: 'bold',
  color: '#4ade80',
  lineHeight: 1.2,
};

const labelStyle: React.CSSProperties = {
  marginBottom: '0.75rem',
  color: '#888',
  fontSize: '0.85rem',
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
};

const noteStyle: React.CSSProperties = {
  color: '#6b7a99',
  fontSize: '0.8rem',
  marginTop: '0.4rem',
};

const numberFmt = new Intl.NumberFormat();

function formatCount(n: number | null | undefined): string {
  return n === null || n === undefined ? '—' : numberFmt.format(n);
}

// Binary units: this counts bytes on disk, and every other tool the operator
// compares against (du, docker, the mount page) reports GiB.
function formatBytes(bytes: number | null | undefined): string {
  if (bytes === null || bytes === undefined) return '—';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value.toFixed(value >= 100 || unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function Tile({
  label,
  value,
  note,
  pending,
}: {
  label: string;
  value: string;
  note?: string;
  pending?: boolean;
}) {
  return (
    <div style={cardStyle}>
      <div style={labelStyle}>{label}</div>
      <div style={{ ...statStyle, color: pending ? '#6b7a99' : statStyle.color }}>
        {value}
      </div>
      {note && <div style={noteStyle}>{note}</div>}
    </div>
  );
}

export default function Dashboard() {
  const [health, setHealth] = useState<HealthStatus | null>(null);
  const [stats, setStats] = useState<Stats | null>(null);
  const [watchersData, setWatchersData] = useState<WatchersResponse | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    const fetchData = async () => {
      // Settled, not all: one dead endpoint must not blank every tile.
      const [healthRes, statsRes, watchersRes] = await Promise.allSettled([
        getJSON<HealthStatus>('/health'),
        getJSON<Stats>('/api/stats'),
        getJSON<WatchersResponse>('/api/watchers'),
      ]);
      if (cancelled) return;

      if (healthRes.status === 'fulfilled') setHealth(healthRes.value);
      if (statsRes.status === 'fulfilled') setStats(statsRes.value);
      if (watchersRes.status === 'fulfilled') setWatchersData(watchersRes.value);

      const failures = [healthRes, statsRes, watchersRes]
        .filter((r): r is PromiseRejectedResult => r.status === 'rejected')
        .map((r) => String(r.reason?.message ?? r.reason));
      setError(failures.length ? failures.join(' · ') : null);
    };

    fetchData();
    const interval = setInterval(fetchData, 5000);
    return () => {
      cancelled = true;
      clearInterval(interval);
    };
  }, []);

  const handleTriggerScan = async () => {
    try {
      await fetch('/api/watchers/scan-all', { method: 'POST' });
    } catch (err) {
      console.error('Failed to trigger scan:', err);
    }
  };

  const isScanning = watchersData?.watchers.some((w) => w.isScanning) || false;
  const activeWatchers = watchersData?.watchers.filter((w) => w.active).length || 0;
  const lastScan =
    watchersData?.watchers.reduce((max, w) => Math.max(max, w.lastScan || 0), 0) || 0;

  // Records we hold metadata for but have no local file: gateway/cid-rooted
  // entries, plus anything whose file went away. The interesting number on a
  // client box, where it should be most of them.
  const fileless =
    stats && stats.files ? Math.max(0, stats.records - stats.files.count) : null;

  const sweepPending = !!stats && !stats.files;
  const sweptNote = (() => {
    if (!stats) return undefined;
    if (sweepPending) return stats.sweeping ? 'scanning…' : 'not scanned yet';
    if (stats.sweeping) return 'refreshing…';
    if (stats.sweptAt) return `as of ${new Date(stats.sweptAt).toLocaleTimeString()}`;
    return undefined;
  })();

  return (
    <div style={{ padding: '2rem', maxWidth: '1200px', margin: '0 auto', overflowY: 'auto', height: '100%' }}>
      <h1 style={{ marginBottom: '2rem' }}>meta-core Dashboard</h1>

      {error && (
        <div style={{ ...cardStyle, background: '#4a1a1a', color: '#f87171' }}>
          {error}
        </div>
      )}

      <div style={gridStyle}>
        <Tile
          label="Files discovered"
          value={formatCount(stats?.files?.count)}
          note={sweptNote}
          pending={sweepPending}
        />
        <Tile
          label="Library size"
          value={formatBytes(stats?.files?.totalSize)}
          note={sweptNote}
          pending={sweepPending}
        />
        <Tile
          label="Metadata entries"
          value={formatCount(stats?.records)}
          note={
            fileless === null
              ? 'records in the index'
              : `${formatCount(fileless)} with no local file`
          }
        />
        <Tile
          label="Redis keys"
          value={formatCount(stats?.redisKeys)}
          note={stats?.redisMemory ? `${stats.redisMemory} in use` : undefined}
        />
        <Tile
          label="Identities"
          value={formatCount(stats?.identities)}
          note={
            stats?.udlUsers !== null && stats?.udlUsers !== undefined
              ? `${formatCount(stats.udlUsers)} with user data`
              : 'signing accounts'
          }
        />
      </div>

      <div style={{ ...gridStyle, gridTemplateColumns: 'repeat(auto-fit, minmax(300px, 1fr))' }}>
        <div style={cardStyle}>
          <div style={labelStyle}>Service health</div>
          {health ? (
            <>
              <div style={statStyle}>
                {health.status === 'ok' ? 'Healthy' : health.status}
              </div>
              <p style={noteStyle}>
                Redis: {health.redis ? 'Connected' : 'Disconnected'}
              </p>
            </>
          ) : (
            <p style={noteStyle}>Loading…</p>
          )}
        </div>

        <div style={cardStyle}>
          <div style={labelStyle}>File watchers</div>
          {watchersData ? (
            <>
              <div style={statStyle}>
                {activeWatchers} / {watchersData.count}
              </div>
              <p style={noteStyle}>active watchers</p>
              <p style={noteStyle}>Status: {isScanning ? 'Scanning…' : 'Idle'}</p>
              {lastScan > 0 && (
                <p style={noteStyle}>Last scan: {new Date(lastScan).toLocaleString()}</p>
              )}
              <button
                onClick={handleTriggerScan}
                style={{
                  marginTop: '1rem',
                  padding: '0.5rem 1rem',
                  background: '#0f3460',
                  color: '#fff',
                  border: 'none',
                  borderRadius: '4px',
                  cursor: 'pointer',
                }}
              >
                Scan All
              </button>
            </>
          ) : (
            <p style={noteStyle}>Loading…</p>
          )}
        </div>
      </div>
    </div>
  );
}
