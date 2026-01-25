import { PluginSchema, FileMetadata, SchemaField } from '../types';
import { DynamicFieldRenderer } from './DynamicFieldRenderer';
import './DynamicPluginSection.css';

interface DynamicPluginSectionProps {
  plugin: PluginSchema;
  metadata: FileMetadata;
  onChange: (key: string, value: any) => void;
}

/**
 * Renders a plugin section with all its fields based on schema
 */
export function DynamicPluginSection({
  plugin,
  metadata,
  onChange,
}: DynamicPluginSectionProps) {
  const schema = plugin.metadataSchema;

  if (!schema || Object.keys(schema).length === 0) {
    return null;
  }

  // Check if plugin has any data in metadata
  const hasData = checkPluginHasData(schema, metadata);
  if (!hasData) {
    return null;
  }

  // Sort fields: non-readonly first, then by key
  const sortedFields = Object.entries(schema).sort(([keyA, a], [keyB, b]) => {
    // Non-readonly fields first
    if (a.readonly !== b.readonly) {
      return a.readonly ? 1 : -1;
    }
    // Then alphabetically
    return keyA.localeCompare(keyB);
  });

  return (
    <section
      className="plugin-section"
      style={{ borderLeftColor: plugin.color || '#646cff' }}
    >
      <div className="plugin-section-header">
        <h3>{plugin.name}</h3>
        {plugin.description && (
          <p className="plugin-description">{plugin.description}</p>
        )}
      </div>

      <div className="plugin-fields">
        {sortedFields.map(([fieldKey, fieldSchema]) => (
          <DynamicFieldRenderer
            key={fieldKey}
            fieldKey={fieldKey}
            schema={fieldSchema}
            metadata={metadata}
            onChange={onChange}
          />
        ))}
      </div>
    </section>
  );
}

/**
 * Check if a plugin has any data in the metadata
 */
function checkPluginHasData(
  schema: Record<string, SchemaField>,
  metadata: FileMetadata
): boolean {
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

/**
 * Get nested value from object using path-style key
 */
function getNestedValue(obj: any, path: string): any {
  const parts = path.split('/');
  let value = obj;
  for (const part of parts) {
    if (value === undefined || value === null) return undefined;
    value = value[part];
  }
  return value;
}

/**
 * Check if object has any key starting with prefix
 */
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
