import { useState, useEffect } from 'react';
import { FileMetadata, PluginSchema } from '../types';
import { MetadataAPI } from '../api/metadataApi';
import { DynamicFieldRenderer } from './DynamicFieldRenderer';
import './BulkMetadataEditor.css';

interface PluginsResponse {
  plugins: PluginSchema[];
}

interface BulkMetadataEditorProps {
  hashIds: string[];
  onSaved?: () => void;
  onCancel?: () => void;
}

export function BulkMetadataEditor({ hashIds, onSaved, onCancel }: BulkMetadataEditorProps) {
  const [plugins, setPlugins] = useState<PluginSchema[]>([]);
  const [commonValues, setCommonValues] = useState<Record<string, any>>({});
  const [editedFields, setEditedFields] = useState<Partial<FileMetadata>>({});
  const [isLoading, setIsLoading] = useState(true);
  const [isSaving, setIsSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [successMessage, setSuccessMessage] = useState<string | null>(null);

  // Load plugins and metadata on mount
  useEffect(() => {
    const loadData = async () => {
      try {
        setIsLoading(true);
        setError(null);

        // Fetch plugins
        const pluginsResponse = await fetch('/api/plugins');
        if (pluginsResponse.ok) {
          const data: PluginsResponse = await pluginsResponse.json();
          // Sort by priority
          const sorted = [...data.plugins].sort((a, b) =>
            (a.priority ?? 100) - (b.priority ?? 100)
          );
          setPlugins(sorted);
        }

        // Load metadata for all selected files
        const metadataPromises = hashIds.map((hashId) => MetadataAPI.getMetadata(hashId));
        const metadata = await Promise.all(metadataPromises);

        // Calculate common values
        const commons: Record<string, any> = {};

        // Get all editable fields from all plugins
        const allFields: string[] = [];
        plugins.forEach((plugin) => {
          if (plugin.metadataSchema) {
            Object.entries(plugin.metadataSchema).forEach(([key, field]) => {
              if (!field.readonly) {
                allFields.push(key);
              }
            });
          }
        });

        allFields.forEach((field) => {
          const values = metadata.map((m) => m[field as keyof FileMetadata]);

          // Filter out undefined and null values
          const definedValues = values.filter((v) => v !== undefined && v !== null);

          if (definedValues.length === 0) {
            return;
          }

          // Safely stringify values
          const uniqueValues = Array.from(
            new Set(
              definedValues.map((v) => {
                try {
                  return JSON.stringify(v);
                } catch {
                  return String(v);
                }
              })
            )
          );

          if (uniqueValues.length === 1) {
            try {
              commons[field] = JSON.parse(uniqueValues[0]);
            } catch {
              commons[field] = uniqueValues[0];
            }
          } else {
            commons[field] = '*';
          }
        });

        setCommonValues(commons);
      } catch (err: any) {
        setError(`Failed to load data: ${err.message}`);
      } finally {
        setIsLoading(false);
      }
    };

    loadData();
  }, [hashIds]);

  const handleFieldChange = (field: string, value: any) => {
    setEditedFields({
      ...editedFields,
      [field]: value,
    });
  };

  const handleClearField = (field: string) => {
    const newFields = { ...editedFields };
    delete newFields[field];
    setEditedFields(newFields);
  };

  const handleSave = async () => {
    if (Object.keys(editedFields).length === 0) {
      setError('No fields have been edited');
      return;
    }

    try {
      setIsSaving(true);
      setError(null);
      setSuccessMessage(null);

      // Build batch update payload
      const updates = hashIds.map((hashId) => ({
        hashId,
        metadata: editedFields,
      }));

      const result = await MetadataAPI.batchUpdate(updates);

      if (result.errors > 0) {
        setError(
          `Updated ${result.success} files successfully, but ${result.errors} failed. Check console for details.`
        );
        console.error('Batch update errors:', result.results.filter((r) => r.error));
      } else {
        setSuccessMessage(`Successfully updated ${result.success} files!`);
      }

      if (onSaved) {
        onSaved();
      }

      setTimeout(() => setSuccessMessage(null), 3000);
    } catch (err: any) {
      setError(err.message);
    } finally {
      setIsSaving(false);
    }
  };

  const hasChanges = Object.keys(editedFields).length > 0;

  // Filter to only editable plugins (those with non-readonly fields)
  const editablePlugins = plugins.filter((plugin) => {
    if (!plugin.metadataSchema) return false;
    return Object.values(plugin.metadataSchema).some((field) => !field.readonly);
  });

  if (isLoading) {
    return (
      <div className="bulk-metadata-editor">
        <div className="bulk-metadata-editor-loading">
          Loading metadata for {hashIds.length} files...
        </div>
      </div>
    );
  }

  // Build display metadata (edited values or common values)
  const displayMetadata: Record<string, any> = { ...commonValues, ...editedFields };

  return (
    <div className="bulk-metadata-editor">
      <div className="bulk-metadata-editor-header">
        <h2>Bulk Edit Metadata</h2>
        <p className="bulk-info">
          Editing {hashIds.length} file{hashIds.length !== 1 ? 's' : ''} - Only modified fields will
          be updated. Fields showing "*" have different values across files.
        </p>
        <div className="bulk-metadata-editor-actions">
          {onCancel && (
            <button onClick={onCancel} disabled={isSaving}>
              Cancel
            </button>
          )}
          <button onClick={handleSave} disabled={isSaving || !hasChanges} className="primary">
            {isSaving ? 'Saving...' : `Update ${hashIds.length} File${hashIds.length !== 1 ? 's' : ''}`}
          </button>
        </div>
      </div>

      {successMessage && (
        <div className="bulk-metadata-editor-message success">{successMessage}</div>
      )}
      {error && <div className="bulk-metadata-editor-message error">{error}</div>}

      <div className="bulk-metadata-editor-form">
        {editablePlugins.map((plugin) => {
          if (!plugin.metadataSchema) return null;

          // Get editable fields for this plugin
          const editableFields = Object.entries(plugin.metadataSchema).filter(
            ([, field]) => !field.readonly
          );

          if (editableFields.length === 0) return null;

          return (
            <section
              key={plugin.id}
              className="bulk-metadata-section"
              style={{ borderLeftColor: plugin.color || '#646cff' }}
            >
              <div className="bulk-metadata-section-header">
                <h3>{plugin.name}</h3>
                {plugin.description && (
                  <p className="section-hint">{plugin.description}</p>
                )}
                <p className="section-hint-bulk">
                  Common values shown. "*" indicates different values. Edit to update all files.
                </p>
              </div>

              <div className="plugin-fields">
                {editableFields.map(([key, field]) => (
                  <DynamicFieldRenderer
                    key={key}
                    fieldKey={key}
                    schema={field}
                    metadata={displayMetadata as FileMetadata}
                    onChange={handleFieldChange}
                    readonly={false}
                  />
                ))}
              </div>
            </section>
          );
        })}

        {/* Modified Fields Summary */}
        {hasChanges && (
          <section className="bulk-metadata-section modified-fields">
            <h3>Fields to be Updated ({Object.keys(editedFields).length})</h3>
            <div className="modified-fields-list">
              {Object.entries(editedFields).map(([key, value]) => (
                <div key={key} className="modified-field-item">
                  <span className="field-name">{key}:</span>
                  <span className="field-value">
                    {Array.isArray(value)
                      ? value.join(', ')
                      : typeof value === 'object'
                      ? JSON.stringify(value)
                      : String(value || '(empty)')}
                  </span>
                  <button
                    className="remove-field-btn"
                    onClick={() => handleClearField(key)}
                    title="Remove this field"
                  >
                    Remove
                  </button>
                </div>
              ))}
            </div>
          </section>
        )}
      </div>
    </div>
  );
}
