import { useEffect, useState } from 'react';

interface HealthStatus {
  status: string;
  redis: boolean;
  timestamp: string;
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

interface ServiceInfo {
  name: string;
  api: string;
  capabilities: string[];
  timestamp: number;
}

const cardStyle: React.CSSProperties = {
  background: '#16213e',
  borderRadius: '8px',
  padding: '1.5rem',
  marginBottom: '1rem',
};

const gridStyle: React.CSSProperties = {
  display: 'grid',
  gridTemplateColumns: 'repeat(auto-fit, minmax(300px, 1fr))',
  gap: '1rem',
};

const statStyle: React.CSSProperties = {
  fontSize: '2rem',
  fontWeight: 'bold',
  color: '#4ade80',
};

export default function Dashboard() {
  const [health, setHealth] = useState<HealthStatus | null>(null);
  const [watchersData, setWatchersData] = useState<WatchersResponse | null>(null);
  const [services, setServices] = useState<ServiceInfo[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const fetchData = async () => {
      try {
        // Fetch health
        const healthRes = await fetch('/health');
        if (healthRes.ok) {
          setHealth(await healthRes.json());
        }

        // Fetch watchers status
        const watchersRes = await fetch('/api/watchers');
        if (watchersRes.ok) {
          setWatchersData(await watchersRes.json());
        }

        // Fetch services
        const servicesRes = await fetch('/services');
        if (servicesRes.ok) {
          const data = await servicesRes.json();
          setServices(data.services || []);
        }

        setError(null);
      } catch (err) {
        setError(String(err));
      }
    };

    fetchData();
    const interval = setInterval(fetchData, 5000);
    return () => clearInterval(interval);
  }, []);

  const handleTriggerScan = async () => {
    try {
      await fetch('/api/watchers/scan-all', { method: 'POST' });
    } catch (err) {
      console.error('Failed to trigger scan:', err);
    }
  };

  // Aggregate watcher stats
  const totalFiles = watchersData?.watchers.reduce((sum, w) => sum + w.fileCount, 0) || 0;
  const isScanning = watchersData?.watchers.some(w => w.isScanning) || false;
  const activeWatchers = watchersData?.watchers.filter(w => w.active).length || 0;
  const lastScan = watchersData?.watchers.reduce((max, w) => Math.max(max, w.lastScan || 0), 0) || 0;

  return (
    <div style={{ padding: '2rem', maxWidth: '1200px', margin: '0 auto' }}>
      <h1 style={{ marginBottom: '2rem' }}>meta-core Dashboard</h1>

      {error && (
        <div style={{ ...cardStyle, background: '#4a1a1a', color: '#f87171' }}>
          Error: {error}
        </div>
      )}

      <div style={gridStyle}>
        {/* Health Card */}
        <div style={cardStyle}>
          <h3 style={{ marginBottom: '1rem', color: '#888' }}>Service Health</h3>
          {health ? (
            <>
              <div style={statStyle}>
                {health.status === 'ok' ? 'Healthy' : health.status}
              </div>
              <p>Redis: {health.redis ? 'Connected' : 'Disconnected'}</p>
            </>
          ) : (
            <p>Loading...</p>
          )}
        </div>

        {/* Watchers Status Card */}
        <div style={cardStyle}>
          <h3 style={{ marginBottom: '1rem', color: '#888' }}>File Watchers</h3>
          {watchersData ? (
            <>
              <div style={statStyle}>{totalFiles}</div>
              <p>Files discovered</p>
              <p>Watchers: {activeWatchers} active / {watchersData.count} total</p>
              <p>Status: {isScanning ? 'Scanning...' : 'Idle'}</p>
              {lastScan > 0 && (
                <p>Last scan: {new Date(lastScan).toLocaleString()}</p>
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
            <p>Loading...</p>
          )}
        </div>

        {/* Services Card */}
        <div style={cardStyle}>
          <h3 style={{ marginBottom: '1rem', color: '#888' }}>Connected Services</h3>
          <div style={statStyle}>{services.length}</div>
          <p>Services registered</p>
          {services.length > 0 && (
            <ul style={{ marginTop: '1rem', paddingLeft: '1.5rem' }}>
              {services.map((svc) => (
                <li key={svc.name} style={{ marginBottom: '0.5rem' }}>
                  <strong>{svc.name}</strong>
                  <br />
                  <span style={{ color: '#888', fontSize: '0.9rem' }}>
                    {svc.capabilities?.join(', ') || 'No capabilities'}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </div>
  );
}
