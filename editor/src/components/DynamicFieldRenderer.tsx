import { SchemaField, FileMetadata } from '../types';
import { LanguageCombobox, formatLanguageOption } from './LanguageCombobox';
import './DynamicFieldRenderer.css';

interface DynamicFieldRendererProps {
  fieldKey: string;
  schema: SchemaField;
  metadata: FileMetadata;
  onChange: (key: string, value: any) => void;
  readonly?: boolean;
}

/**
 * Renders a single metadata field based on its schema definition
 */
export function DynamicFieldRenderer({
  fieldKey,
  schema,
  metadata,
  onChange,
  readonly = false,
}: DynamicFieldRendererProps) {
  const isReadonly = readonly || schema.readonly;
  const fieldType = schema.type || 'string';

  // Get value from metadata using path-style key
  const getValue = (): any => {
    // Handle path-style keys like "stream/duration" or "nfo/title"
    const parts = fieldKey.split('/');
    let value: any = metadata;
    for (const part of parts) {
      if (value === undefined || value === null) return undefined;
      value = value[part];
    }
    return value;
  };

  // Set value in metadata using path-style key
  const setValue = (newValue: any) => {
    onChange(fieldKey, newValue);
  };

  const value = getValue();

  // Render link if configured
  const renderValueWithLink = (displayValue: string) => {
    if (schema.link && displayValue) {
      const url = schema.link.replace('{value}', encodeURIComponent(displayValue));
      return (
        <a href={url} target="_blank" rel="noopener noreferrer" className="field-link">
          {displayValue}
        </a>
      );
    }
    return displayValue;
  };

  // Render based on field type
  switch (fieldType) {
    case 'boolean':
      return (
        <div className="dynamic-field">
          <label className="checkbox-label">
            <input
              type="checkbox"
              checked={Boolean(value)}
              onChange={(e) => setValue(e.target.checked)}
              disabled={isReadonly}
            />
            <span>{schema.label}</span>
          </label>
          {schema.hint && <small className="field-hint">{schema.hint}</small>}
        </div>
      );

    case 'number':
      return (
        <div className="dynamic-field">
          <label>{schema.label}</label>
          <input
            type="number"
            value={value ?? ''}
            onChange={(e) => setValue(e.target.value ? Number(e.target.value) : undefined)}
            disabled={isReadonly}
            min={schema.min}
            max={schema.max}
            placeholder={schema.placeholder}
          />
          {schema.hint && <small className="field-hint">{schema.hint}</small>}
        </div>
      );

    case 'select':
      return (
        <div className="dynamic-field">
          <label>{schema.label}</label>
          <select
            value={value ?? ''}
            onChange={(e) => setValue(e.target.value || undefined)}
            disabled={isReadonly}
          >
            <option value="">Select...</option>
            {schema.options?.map((opt) => (
              <option key={opt.value} value={opt.value}>
                {opt.label}
              </option>
            ))}
          </select>
          {schema.hint && <small className="field-hint">{schema.hint}</small>}
        </div>
      );

    case 'text':
      return (
        <div className="dynamic-field">
          <label>{schema.label}</label>
          <textarea
            value={value ?? ''}
            onChange={(e) => setValue(e.target.value || undefined)}
            disabled={isReadonly}
            placeholder={schema.placeholder}
            rows={4}
          />
          {schema.hint && <small className="field-hint">{schema.hint}</small>}
        </div>
      );

    case 'array':
      // Convert RecordSet to comma-separated string for display
      const arrayValue = (() => {
        if (!value) return '';
        if (typeof value === 'string') return value;
        if (typeof value === 'object') {
          return Object.values(value).join(', ');
        }
        return '';
      })();

      // Convert comma-separated string back to RecordSet
      const handleArrayChange = (str: string) => {
        const items = str.split(',').map(s => s.trim()).filter(Boolean);
        if (items.length === 0) {
          setValue(undefined);
          return;
        }
        const recordSet: Record<string, string> = {};
        items.forEach((item, idx) => {
          recordSet[idx.toString()] = item;
        });
        setValue(recordSet);
      };

      return (
        <div className="dynamic-field">
          <label>{schema.label}</label>
          <input
            type="text"
            value={arrayValue}
            onChange={(e) => handleArrayChange(e.target.value)}
            disabled={isReadonly}
            placeholder={schema.placeholder || 'Comma-separated values'}
          />
          {schema.hint && <small className="field-hint">{schema.hint}</small>}
        </div>
      );

    case 'json':
      // Aggregate matching keys based on pattern
      const jsonData = (() => {
        if (!schema.pattern) {
          return value;
        }

        // Pattern like "stream/video/{n}/*" - aggregate all matching keys
        const patternPrefix = schema.pattern.replace(/\{n\}.*$/, '');
        const result: Record<string, any> = {};

        // Recursively find all matching keys
        const findMatching = (obj: any, currentPath: string = '') => {
          if (!obj || typeof obj !== 'object') return;

          for (const [key, val] of Object.entries(obj)) {
            const newPath = currentPath ? `${currentPath}/${key}` : key;
            if (newPath.startsWith(patternPrefix.replace(/\/$/, ''))) {
              // Store in result with relative path
              const relativePath = newPath.substring(patternPrefix.length - 1);
              if (relativePath) {
                setNestedValue(result, relativePath, val);
              }
            }
            if (typeof val === 'object' && val !== null) {
              findMatching(val, newPath);
            }
          }
        };

        findMatching(metadata);
        return Object.keys(result).length > 0 ? result : undefined;
      })();

      if (!jsonData) {
        return null;
      }

      return (
        <div className="dynamic-field json-field">
          <label>{schema.label}</label>
          <pre className="json-viewer">
            {JSON.stringify(jsonData, null, 2)}
          </pre>
          {schema.hint && <small className="field-hint">{schema.hint}</small>}
        </div>
      );

    case 'cid':
      // CID field type - displays CID with "Open in Editor" button
      if (!value) {
        return null;
      }

      const cidValue = String(value);
      const editorUrl = `/editor/file/${cidValue}`;

      return (
        <div className="dynamic-field cid-field">
          <label>{schema.label}</label>
          <div className="cid-value-container">
            <code
              className="cid-value"
              onClick={() => navigator.clipboard.writeText(cidValue)}
              title="Click to copy"
            >
              {cidValue}
            </code>
            <a
              href={editorUrl}
              className="cid-open-button"
              title="Open in Editor"
              target="_blank"
              rel="noopener noreferrer"
            >
              ↗ Open
            </a>
          </div>
          {schema.hint && <small className="field-hint">{schema.hint}</small>}
        </div>
      );

    case 'languageString':
      // Language-keyed string object: { "eng": "English Title", "jpn": "日本語タイトル" }
      const langEntries: Array<[string, string]> = value && typeof value === 'object'
        ? Object.entries(value).map(([k, v]) => [k, String(v)])
        : [];

      const handleLangCodeChange = (oldLang: string, newLang: string) => {
        if (oldLang === newLang) return;
        const newValue = { ...value };
        const text = newValue[oldLang];
        delete newValue[oldLang];
        newValue[newLang] = text;
        setValue(Object.keys(newValue).length > 0 ? newValue : undefined);
      };

      const handleLangTextChange = (lang: string, text: string) => {
        const newValue = { ...value, [lang]: text };
        setValue(newValue);
      };

      const handleRemoveLang = (lang: string) => {
        const newValue = { ...value };
        delete newValue[lang];
        setValue(Object.keys(newValue).length > 0 ? newValue : undefined);
      };

      const handleAddLang = () => {
        // Find a language code not already used, default to 'eng'
        const usedLangs = new Set(langEntries.map(([k]) => k));
        const commonDefaults = ['eng', 'jpn', 'zho', 'kor', 'fra', 'deu', 'spa'];
        const defaultLang = commonDefaults.find(l => !usedLangs.has(l)) || 'eng';
        setValue({ ...value, [defaultLang]: '' });
      };

      if (isReadonly && langEntries.length === 0) {
        return null;
      }

      return (
        <div className="dynamic-field language-string-field">
          <label>{schema.label}</label>
          <div className="language-entries">
            {langEntries.map(([lang, text]) => (
              <div key={lang} className="language-entry">
                {isReadonly ? (
                  <>
                    <span className="lang-code" title={formatLanguageOption(lang)}>{lang}</span>
                    <span className="lang-text">{text}</span>
                  </>
                ) : (
                  <>
                    <LanguageCombobox
                      value={lang}
                      onChange={(newLang) => handleLangCodeChange(lang, newLang)}
                    />
                    <input
                      type="text"
                      className="lang-input"
                      value={text}
                      onChange={(e) => handleLangTextChange(lang, e.target.value)}
                      placeholder={schema.placeholder || 'Text...'}
                    />
                    <button
                      type="button"
                      className="lang-remove-btn"
                      onClick={() => handleRemoveLang(lang)}
                      title="Remove"
                    >
                      ×
                    </button>
                  </>
                )}
              </div>
            ))}
            {!isReadonly && (
              <button
                type="button"
                className="lang-add-btn"
                onClick={handleAddLang}
              >
                + Add Language
              </button>
            )}
          </div>
          {schema.hint && <small className="field-hint">{schema.hint}</small>}
        </div>
      );

    case 'string':
    default:
      // Show nothing if no value and readonly
      if (isReadonly && !value) {
        return null;
      }

      return (
        <div className="dynamic-field">
          <label>{schema.label}</label>
          {isReadonly ? (
            <div className="readonly-value">
              {renderValueWithLink(String(value ?? ''))}
            </div>
          ) : (
            <input
              type="text"
              value={value ?? ''}
              onChange={(e) => setValue(e.target.value || undefined)}
              placeholder={schema.placeholder}
            />
          )}
          {schema.hint && <small className="field-hint">{schema.hint}</small>}
        </div>
      );
  }
}

// Helper to set nested value in object
function setNestedValue(obj: any, path: string, value: any) {
  const parts = path.split('/').filter(Boolean);
  let current = obj;
  for (let i = 0; i < parts.length - 1; i++) {
    if (!current[parts[i]]) {
      current[parts[i]] = {};
    }
    current = current[parts[i]];
  }
  if (parts.length > 0) {
    current[parts[parts.length - 1]] = value;
  }
}
