import { useEffect, useState } from 'react';

interface HealthStatus {
  status: string;
  role: string;
  redis: boolean;
  timestamp: string;
}

interface ScanStatus {
  status: string;
  scanning: boolean;
  lastScan: number;
  fileCount: number;
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
  const [scanStatus, setScanStatus] = useState<ScanStatus | null>(null);
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

        // Fetch scan status
        const scanRes = await fetch('/api/scan/status');
        if (scanRes.ok) {
          setScanStatus(await scanRes.json());
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
      await fetch('/api/scan/trigger', { method: 'POST' });
    } catch (err) {
      console.error('Failed to trigger scan:', err);
    }
  };

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
              <p>Role: <strong>{health.role}</strong></p>
              <p>Redis: {health.redis ? 'Connected' : 'Disconnected'}</p>
            </>
          ) : (
            <p>Loading...</p>
          )}
        </div>

        {/* Scan Status Card */}
        <div style={cardStyle}>
          <h3 style={{ marginBottom: '1rem', color: '#888' }}>File Watcher</h3>
          {scanStatus ? (
            <>
              <div style={statStyle}>{scanStatus.fileCount}</div>
              <p>Files discovered</p>
              <p>Status: {scanStatus.scanning ? 'Scanning...' : scanStatus.status}</p>
              {scanStatus.lastScan > 0 && (
                <p>Last scan: {new Date(scanStatus.lastScan).toLocaleString()}</p>
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
                Trigger Scan
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
