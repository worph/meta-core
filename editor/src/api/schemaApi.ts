import { useEffect, useState } from 'react';

export type SchemaPrimitive =
  | 'string'
  | 'int'
  | 'float'
  | 'bool'
  | 'json-object'
  | 'json-array'
  | 'undefined'
  | 'mixed';

export type SchemaHint = 'cid' | 'timestamp';
export type SchemaKeyHint = 'language-code';

export interface SchemaFieldInfo {
  type: SchemaPrimitive;
  hint?: SchemaHint;
  key_hint?: SchemaKeyHint;
  breakdown: Record<string, number>;
}

export interface SchemaResponse {
  fields: Record<string, SchemaFieldInfo>;
  generated_at: string;
  source: 'live' | 'rescan';
}

let cached: Promise<SchemaResponse> | null = null;

export async function fetchSchema(): Promise<SchemaResponse> {
  if (!cached) {
    cached = fetch('/api/schema').then((r) => {
      if (!r.ok) {
        cached = null;
        throw new Error(`schema fetch failed: ${r.status}`);
      }
      return r.json();
    });
  }
  return cached;
}

/**
 * Returns the live schema entry for a field, or undefined if not yet observed.
 * Loads /api/schema lazily on first mount and caches the result process-wide.
 */
export function useSchema(): {
  schema: SchemaResponse | null;
  getFieldHint: (key: string) => SchemaFieldInfo | undefined;
} {
  const [schema, setSchema] = useState<SchemaResponse | null>(null);

  useEffect(() => {
    let alive = true;
    fetchSchema()
      .then((s) => {
        if (alive) setSchema(s);
      })
      .catch(() => {
        // Schema endpoint missing or errored — editor falls back to plugin schemas.
      });
    return () => {
      alive = false;
    };
  }, []);

  const getFieldHint = (key: string): SchemaFieldInfo | undefined => {
    if (!schema) return undefined;
    return schema.fields[key];
  };

  return { schema, getFieldHint };
}
