import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import './index.css';
import KVApp from './kv/KVApp.tsx';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <KVApp />
  </StrictMode>,
);
