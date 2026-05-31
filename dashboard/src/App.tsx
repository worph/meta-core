import { BrowserRouter, Routes, Route, NavLink } from 'react-router-dom';
import Dashboard from './pages/Dashboard';
import Mounts from './pages/Mounts';
import Watchers from './pages/Watchers';
import Files from './pages/Files';
import Identity from './pages/Identity';
import ServiceNav from './components/ServiceNav';

const navStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  gap: '1rem',
  padding: '0.75rem 2rem',
  background: '#16213e',
  borderBottom: '1px solid #0f3460',
  flexShrink: 0,
};

const linkStyle: React.CSSProperties = {
  color: '#888',
  textDecoration: 'none',
  padding: '0.5rem 1rem',
  borderRadius: '4px',
  transition: 'all 0.2s',
};

const activeLinkStyle: React.CSSProperties = {
  ...linkStyle,
  color: '#fff',
  background: '#0f3460',
};

const contentStyle: React.CSSProperties = {
  flex: 1,
  overflow: 'hidden',
  minHeight: 0,
};

// Editor page with iframe to standalone editor
function EditorPage() {
  return (
    <iframe
      src="/editor/"
      style={{ width: '100%', height: '100%', border: 'none', display: 'block' }}
      title="Metadata Editor"
    />
  );
}

export default function App() {
  return (
    <BrowserRouter>
      <nav style={navStyle}>
        <NavLink
          to="/"
          style={({ isActive }) => (isActive ? activeLinkStyle : linkStyle)}
        >
          Dashboard
        </NavLink>
        <NavLink
          to="/mounts"
          style={({ isActive }) => (isActive ? activeLinkStyle : linkStyle)}
        >
          Mounts
        </NavLink>
        <NavLink
          to="/watchers"
          style={({ isActive }) => (isActive ? activeLinkStyle : linkStyle)}
        >
          Watchers
        </NavLink>
        <NavLink
          to="/files"
          style={({ isActive }) => (isActive ? activeLinkStyle : linkStyle)}
        >
          Files
        </NavLink>
        <NavLink
          to="/identity"
          style={({ isActive }) => (isActive ? activeLinkStyle : linkStyle)}
        >
          Identity
        </NavLink>
        <NavLink
          to="/editor"
          style={({ isActive }) => (isActive ? activeLinkStyle : linkStyle)}
        >
          Editor
        </NavLink>
        <div style={{ flex: 1 }} />
        <ServiceNav />
      </nav>
      <div style={contentStyle}>
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/mounts" element={<Mounts />} />
          <Route path="/watchers" element={<Watchers />} />
          <Route path="/files" element={<Files />} />
          <Route path="/identity" element={<Identity />} />
          <Route path="/editor" element={<EditorPage />} />
        </Routes>
      </div>
    </BrowserRouter>
  );
}
