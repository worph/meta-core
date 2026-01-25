import { useState, useEffect } from 'react';
import { FileMetadata, PluginSchema, PluginsResponse } from '../types';
import { MetadataAPI } from '../api/metadataApi';
import { DynamicPluginSection } from './DynamicPluginSection';
import './MetadataEditor.css';

interface PluginAccordionProps {
  plugin: PluginSchema;
  metadata: FileMetadata;
  onChange: (field: string, value: any) => void;
  isExpanded: boolean;
  onToggle: () => void;
}

function PluginAccordion({ plugin, metadata, onChange, isExpanded, onToggle }: PluginAccordionProps) {
  return (
    <div className={`plugin-accordion ${isExpanded ? 'expanded' : ''}`}>
      <button
        className="plugin-accordion-header"
        onClick={onToggle}
        style={{ borderLeftColor: plugin.color || '#646cff' }}
      >
        <span className="plugin-accordion-title">{plugin.name}</span>
        <span className="plugin-accordion-icon">{isExpanded ? '▼' : '▶'}</span>
      </button>
      {isExpanded && (
        <div className="plugin-accordion-content">
          <DynamicPluginSection
            plugin={plugin}
            metadata={metadata}
            onChange={onChange}
          />
        </div>
      )}
    </div>
  );
}

interface MetadataEditorProps {
  hashId: string;
  onSaved?: () => void;
}

export function MetadataEditor({ hashId, onSaved }: MetadataEditorProps) {
  const [metadata, setMetadata] = useState<FileMetadata | null>(null);
  const [editedMetadata, setEditedMetadata] = useState<FileMetadata | null>(null);
  const [plugins, setPlugins] = useState<PluginSchema[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [successMessage, setSuccessMessage] = useState<string | null>(null);
  const [expandedPlugins, setExpandedPlugins] = useState<Set<string>>(new Set());

  // Fetch plugins schema on mount
  useEffect(() => {
    fetchPlugins();
  }, []);

  // Fetch metadata when hashId changes
  useEffect(() => {
    loadMetadata();
  }, [hashId]);

  const fetchPlugins = async () => {
    try {
      const response = await fetch('/api/plugins');
      if (response.ok) {
        const data: PluginsResponse = await response.json();
        // Sort by priority (lower = higher priority)
        const sorted = [...data.plugins].sort((a, b) =>
          (a.priority ?? 100) - (b.priority ?? 100)
        );
        setPlugins(sorted);
      }
    } catch (err) {
      console.error('Failed to fetch plugin schemas:', err);
    }
  };

  const loadMetadata = async () => {
    try {
      setIsLoading(true);
      setError(null);
      const data = await MetadataAPI.getMetadata(hashId);
      setMetadata(data);
      setEditedMetadata(data);
    } catch (err: any) {
      setError(err.message);
    } finally {
      setIsLoading(false);
    }
  };

  const handleFieldChange = (field: string, value: any) => {
    if (!editedMetadata) return;

    // Handle path-style keys like "stream/duration" or "nfo/title"
    const parts = field.split('/');

    if (parts.length === 1) {
      // Simple key
      setEditedMetadata({
        ...editedMetadata,
        [field]: value,
      });
    } else {
      // Nested key - need to update nested structure
      const newMetadata = { ...editedMetadata };
      let current: any = newMetadata;

      for (let i = 0; i < parts.length - 1; i++) {
        if (!current[parts[i]]) {
          current[parts[i]] = {};
        } else {
          current[parts[i]] = { ...current[parts[i]] };
        }
        current = current[parts[i]];
      }

      current[parts[parts.length - 1]] = value;
      setEditedMetadata(newMetadata);
    }
  };

  const handleSave = async () => {
    if (!editedMetadata) return;

    try {
      setIsSaving(true);
      setError(null);
      setSuccessMessage(null);

      await MetadataAPI.updateMetadata(hashId, editedMetadata);
      setMetadata(editedMetadata);
      setSuccessMessage('Metadata saved successfully!');

      if (onSaved) {
        onSaved();
      }

      // Clear success message after 3 seconds
      setTimeout(() => setSuccessMessage(null), 3000);
    } catch (err: any) {
      setError(err.message);
    } finally {
      setIsSaving(false);
    }
  };

  const handleReset = () => {
    setEditedMetadata(metadata);
    setError(null);
    setSuccessMessage(null);
  };

  const togglePlugin = (pluginId: string) => {
    setExpandedPlugins(prev => {
      const next = new Set(prev);
      if (next.has(pluginId)) {
        next.delete(pluginId);
      } else {
        next.add(pluginId);
      }
      return next;
    });
  };

  const scrollToPlugin = (pluginId: string) => {
    // Expand the plugin if not already expanded
    if (!expandedPlugins.has(pluginId)) {
      setExpandedPlugins(prev => new Set(prev).add(pluginId));
    }
    // Scroll to the plugin after a short delay to allow expansion
    setTimeout(() => {
      const element = document.getElementById(`plugin-${pluginId}`);
      if (element) {
        element.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }
    }, 100);
  };

  if (isLoading) {
    return <div className="metadata-editor-loading">Loading metadata...</div>;
  }

  if (error && !metadata) {
    return (
      <div className="metadata-editor-error">
        <p>Error loading metadata: {error}</p>
        <button onClick={loadMetadata}>Retry</button>
      </div>
    );
  }

  if (!metadata || !editedMetadata) {
    return <div className="metadata-editor-empty">No metadata available</div>;
  }

  const hasChanges = JSON.stringify(metadata) !== JSON.stringify(editedMetadata);

  // Filter plugins that have data
  const pluginsWithData = plugins.filter(plugin => {
    if (!plugin.metadataSchema) return false;
    return hasPluginData(plugin, editedMetadata);
  });

  // Get file path for download
  const filePath = editedMetadata?.filePath as string | undefined;
  const downloadUrl = filePath ? `/api/file/download?path=${encodeURIComponent(filePath)}` : null;

  return (
    <div className="metadata-editor">
      <div className="metadata-editor-header">
        <h2>Edit Metadata</h2>
        <div className="metadata-editor-actions">
          {downloadUrl && (
            <a
              href={downloadUrl}
              className="download-button"
              title={`Download: ${filePath}`}
              download
            >
              ⬇ Download
            </a>
          )}
          {hasChanges && (
            <button onClick={handleReset} disabled={isSaving}>
              Reset
            </button>
          )}
          <button
            onClick={handleSave}
            disabled={isSaving || !hasChanges}
            className="primary"
          >
            {isSaving ? 'Saving...' : 'Save Changes'}
          </button>
        </div>
      </div>

      {successMessage && (
        <div className="metadata-editor-message success">{successMessage}</div>
      )}
      {error && <div className="metadata-editor-message error">{error}</div>}

      <div className="metadata-editor-hashes">
        <div className="hashes-header">
          <h3>Content Identifiers</h3>
        </div>
        <div className="hashes-grid">
          <HashValue label="midhash256" value={hashId} primary />
          {getHashValues(editedMetadata).map(({ key, label, value }) => (
            <HashValue key={key} label={label} value={value} />
          ))}
        </div>
      </div>

      {pluginsWithData.length > 0 && (
        <div className="metadata-editor-chips">
          {pluginsWithData.map((plugin) => (
            <button
              key={plugin.id}
              className={`plugin-chip ${expandedPlugins.has(plugin.id) ? 'active' : ''}`}
              style={{
                '--chip-color': plugin.color || '#646cff',
                backgroundColor: expandedPlugins.has(plugin.id) ? plugin.color || '#646cff' : 'transparent'
              } as React.CSSProperties}
              onClick={() => scrollToPlugin(plugin.id)}
            >
              {plugin.name}
            </button>
          ))}
        </div>
      )}

      <div className="metadata-editor-form">
        {pluginsWithData.length === 0 ? (
          <div className="metadata-editor-empty">
            No metadata found for this file
          </div>
        ) : (
          pluginsWithData.map((plugin) => (
            <div key={plugin.id} id={`plugin-${plugin.id}`}>
              <PluginAccordion
                plugin={plugin}
                metadata={editedMetadata}
                onChange={handleFieldChange}
                isExpanded={expandedPlugins.has(plugin.id)}
                onToggle={() => togglePlugin(plugin.id)}
              />
            </div>
          ))
        )}
      </div>
    </div>
  );
}

/**
 * Check if a plugin has any data in the metadata
 */
function hasPluginData(plugin: PluginSchema, metadata: FileMetadata): boolean {
  const schema = plugin.metadataSchema;
  if (!schema) return false;

  for (const fieldKey of Object.keys(schema)) {
    const fieldSchema = schema[fieldKey];

    // For JSON type with pattern, check if any matching keys exist
    if (fieldSchema.type === 'json' && fieldSchema.pattern) {
      const patternPrefix = fieldSchema.pattern.replace(/\{n\}.*$/, '').replace(/\/$/, '');
      if (hasKeyWithPrefix(metadata, patternPrefix)) {
        return true;
      }
      continue;
    }

    // For regular fields, check if value exists
    if (getNestedValue(metadata, fieldKey) !== undefined) {
      return true;
    }
  }
  return false;
}

function getNestedValue(obj: any, path: string): any {
  const parts = path.split('/');
  let value = obj;
  for (const part of parts) {
    if (value === undefined || value === null) return undefined;
    value = value[part];
  }
  return value;
}

function hasKeyWithPrefix(obj: any, prefix: string, currentPath: string = ''): boolean {
  if (!obj || typeof obj !== 'object') return false;

  for (const key of Object.keys(obj)) {
    const newPath = currentPath ? `${currentPath}/${key}` : key;
    if (newPath.startsWith(prefix)) {
      return true;
    }
    if (typeof obj[key] === 'object' && obj[key] !== null) {
      if (hasKeyWithPrefix(obj[key], prefix, newPath)) {
        return true;
      }
    }
  }
  return false;
}

/**
 * Extract all hash values (cid_*) from metadata
 */
function getHashValues(metadata: FileMetadata): { key: string; label: string; value: string }[] {
  const hashOrder = [
    { key: 'cid_sha2-256', label: 'SHA-256' },
    { key: 'cid_sha3-256', label: 'SHA3-256' },
    { key: 'cid_sha3-384', label: 'SHA3-384' },
    { key: 'cid_sha1', label: 'SHA-1' },
    { key: 'cid_md5', label: 'MD5' },
    { key: 'cid_crc32', label: 'CRC32' },
    { key: 'cid_btih_v2', label: 'BitTorrent v2' },
  ];

  return hashOrder
    .filter(({ key }) => (metadata as any)[key])
    .map(({ key, label }) => ({
      key,
      label,
      value: (metadata as any)[key],
    }));
}

/**
 * Display a single hash value with copy functionality
 */
function HashValue({ label, value, primary }: { label: string; value: string; primary?: boolean }) {
  const handleCopy = () => {
    navigator.clipboard.writeText(value);
  };

  return (
    <div className={`hash-value ${primary ? 'hash-value-primary' : ''}`}>
      <span className="hash-label">{label}</span>
      <code className="hash-code" onClick={handleCopy} title="Click to copy">
        {value}
      </code>
    </div>
  );
}
