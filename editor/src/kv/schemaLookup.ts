import { SchemaFieldInfo, SchemaResponse } from '../api/schemaApi';
import { FieldType } from './types';

export interface SchemaLookup {
  /** The matched schema entry, if any. */
  info?: SchemaFieldInfo;
  /** Suggested FieldType for the leaf editor, or null when schema can't decide. */
  fieldType: FieldType | null;
  /**
   * True when the leaf is one variant under a language-keyed group
   * (e.g. plot/eng). The schema entry returned is the group's, not the leaf's.
   */
  isLanguageVariant: boolean;
}

/**
 * Resolve the logical schema entry for a full Redis key.
 *
 * - "file:<hash>/releasedate"  → schema.fields["releasedate"]
 * - "file:<hash>/plot/eng"     → schema.fields["plot"] (language-keyed group)
 * - non-file keys              → undefined (schema is per-file today)
 */
export function lookupSchemaForKey(
  schema: SchemaResponse | null,
  key: string,
): SchemaLookup {
  if (!schema) {
    return { fieldType: null, isLanguageVariant: false };
  }

  const propertyPath = stripFilePrefix(key);
  if (!propertyPath) {
    return { fieldType: null, isLanguageVariant: false };
  }

  const direct = schema.fields[propertyPath];
  if (direct) {
    return {
      info: direct,
      fieldType: schemaToFieldType(direct),
      isLanguageVariant: false,
    };
  }

  // Language-keyed collapse: tail is a 2/3-letter lang code under a known group.
  const lastSlash = propertyPath.lastIndexOf('/');
  if (lastSlash > 0) {
    const tail = propertyPath.slice(lastSlash + 1);
    if (/^[a-z]{2,3}$/.test(tail)) {
      const group = schema.fields[propertyPath.slice(0, lastSlash)];
      if (group?.key_hint === 'language-code') {
        return {
          info: group,
          fieldType: schemaToFieldType(group),
          isLanguageVariant: true,
        };
      }
    }
  }

  return { fieldType: null, isLanguageVariant: false };
}

export function schemaToFieldType(info: SchemaFieldInfo | undefined): FieldType | null {
  if (!info) return null;
  if (info.hint === 'cid') return 'hash';
  if (info.hint === 'timestamp') return 'datetime';
  switch (info.type) {
    case 'bool':
      return 'bool';
    case 'int':
    case 'float':
      return 'number';
    case 'json-object':
    case 'json-array':
      return 'json';
    case 'string':
    case 'mixed':
    case 'undefined':
    default:
      return null;
  }
}

function stripFilePrefix(key: string): string {
  if (!key.startsWith('file:')) return '';
  const slash = key.indexOf('/');
  if (slash < 0) return '';
  return key.slice(slash + 1);
}
