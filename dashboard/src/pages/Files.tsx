import { useEffect, useState } from 'react';

interface FileEvent {
  type: 'add' | 'change' | 'delete' | 'rename';
  path: string;
  timestamp: number;
}

const cardStyle: React.CSSProperties = {
  background: '#16213e',
  borderRadius: '8px',
  padding: '1rem',
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  minHeight: 0,
};

export default function Files() {
  const [recentEvents, setRecentEvents] = useState<FileEvent[]>([]);

  const fetchEvents = async () => {
    try {
      const res = await fetch('/api/events/poll?limit=20');
      if (res.ok) {
        const data = await res.json();
        setRecentEvents(data.events || []);
      }
    } catch (err) {
      console.error('Failed to fetch events:', err);
    }
  };

  useEffect(() => {
    fetchEvents();
    const interval = setInterval(fetchEvents, 5000);
    return () => clearInterval(interval);
  }, []);

  const formatTime = (timestamp: number) => {
    return new Date(timestamp).toLocaleString();
  };

  return (
    <div style={{
      padding: '1rem',
      height: '100%',
      display: 'flex',
      flexDirection: 'column',
      overflow: 'hidden'
    }}>
      <h1 style={{ margin: '0 0 1rem 0', flexShrink: 0 }}>File Browser</h1>

      <div style={{
        display: 'grid',
        gridTemplateColumns: '2fr 1fr',
        gap: '1rem',
        flex: 1,
        minHeight: 0
      }}>
        {/* WebDAV Browser Iframe */}
        <div style={cardStyle}>
          <iframe
            src="/webdav-browser/"
            style={{
              width: '100%',
              flex: 1,
              border: 'none',
              borderRadius: '4px',
              background: '#fff',
              display: 'block',
              minHeight: 0
            }}
            title="File Browser"
          />
        </div>

        {/* Recent Events */}
        <div style={cardStyle}>
          <h3 style={{ margin: '0 0 0.75rem 0', flexShrink: 0 }}>Recent File Events</h3>
          {recentEvents.length === 0 ? (
            <p style={{ color: '#888', margin: 0 }}>No recent events</p>
          ) : (
            <div style={{ flex: 1, overflow: 'auto', minHeight: 0 }}>
              {recentEvents.map((event, i) => (
                <div
                  key={i}
                  style={{
                    padding: '0.5rem',
                    borderBottom: '1px solid #0f3460',
                    fontSize: '0.9rem',
                  }}
                >
                  <span
                    style={{
                      display: 'inline-block',
                      padding: '0.1rem 0.3rem',
                      borderRadius: '3px',
                      fontSize: '0.75rem',
                      marginRight: '0.5rem',
                      background:
                        event.type === 'add'
                          ? '#166534'
                          : event.type === 'delete'
                          ? '#7f1d1d'
                          : '#854d0e',
                    }}
                  >
                    {event.type}
                  </span>
                  <span style={{ color: '#888' }}>{event.path}</span>
                  <br />
                  <span style={{ color: '#555', fontSize: '0.8rem' }}>
                    {formatTime(event.timestamp)}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
