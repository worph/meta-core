import { useState, useEffect } from 'react';

interface ServiceInfo {
  name: string;
  hostname: string;
  baseUrl: string;
  status: string;
  lastHeartbeat: string;
  role?: string; // "leader", "follower", or undefined (for non-meta-core services)
}

interface ServicesResponse {
  services: ServiceInfo[];
  current: string;
}

const serviceIcons: Record<string, string> = {
  'meta-core': '🗄️',
  'meta-sort': '📁',
  'meta-fuse': '🗂️',
  'meta-stremio': '🎬',
  'meta-orbit': '🌐',
  'default': '📦'
};

function formatServiceName(name: string): string {
  return name.split('-').map(word =>
    word.charAt(0).toUpperCase() + word.slice(1)
  ).join(' ');
}

function ServiceNav() {
  const [services, setServices] = useState<ServiceInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const currentService = 'meta-core';

  useEffect(() => {
    const fetchServices = async () => {
      try {
        const response = await fetch('/services');
        if (response.ok) {
          const data: ServicesResponse = await response.json();
          setServices(data.services || []);
        }
      } catch (error) {
        console.error('Failed to fetch services:', error);
      } finally {
        setLoading(false);
      }
    };

    fetchServices();
    const interval = setInterval(fetchServices, 30000);
    return () => clearInterval(interval);
  }, []);

  if (loading) return null;

  // Filter out follower meta-core instances (only show leader)
  const filteredServices = services.filter(
    s => s.name !== 'meta-core' || s.role === 'leader'
  );
  const sortedServices = [...filteredServices].sort((a, b) => a.name.localeCompare(b.name));

  if (sortedServices.length === 0) return null;

  return (
    <nav className="services-nav">
      <span className="services-nav-label">Services:</span>
      <div className="services-nav-items">
        {sortedServices.map(service => {
          const icon = serviceIcons[service.name] || serviceIcons.default;
          const isActive = service.name === currentService;
          return (
            <a
              key={service.name}
              href={isActive ? '#' : service.baseUrl}
              className={`service-link${isActive ? ' active' : ''}`}
              onClick={(e) => {
                e.preventDefault();
                if (!isActive) {
                  window.location.href = service.baseUrl;
                }
              }}
            >
              <span className="service-icon">{icon}</span>
              <span>{formatServiceName(service.name)}</span>
              <span className="service-status"></span>
            </a>
          );
        })}
      </div>

      <style>{`
        .services-nav {
          display: flex;
          align-items: center;
          gap: 0.5rem;
        }

        .services-nav-label {
          color: #888;
          font-size: 0.8rem;
          white-space: nowrap;
        }

        .services-nav-items {
          display: flex;
          gap: 0.35rem;
        }

        .service-link {
          display: inline-flex;
          align-items: center;
          gap: 0.3rem;
          padding: 0.35rem 0.6rem;
          border-radius: 6px;
          text-decoration: none;
          font-size: 0.8rem;
          font-weight: 500;
          transition: all 0.2s;
          color: #ccc;
          background: #0f3460;
          border: 1px solid #1a4a7a;
        }

        .service-link:hover {
          background: #1a4a7a;
          border-color: rgba(78, 205, 196, 0.5);
          text-decoration: none;
          color: #fff;
        }

        .service-link.active {
          background: linear-gradient(135deg, rgba(78, 205, 196, 0.2), rgba(68, 160, 141, 0.2));
          border-color: rgba(78, 205, 196, 0.5);
          color: #fff;
        }

        .service-link .service-icon {
          font-size: 0.9rem;
        }

        .service-link .service-status {
          width: 5px;
          height: 5px;
          border-radius: 50%;
          background: #4ecdc4;
        }

        @media (max-width: 800px) {
          .services-nav-label {
            display: none;
          }
        }
      `}</style>
    </nav>
  );
}

export default ServiceNav;
