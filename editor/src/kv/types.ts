// Inferred type for a leaf value. "auto" defers to the heuristic detector.
export type FieldType =
  | 'auto'
  | 'string'
  | 'number'
  | 'bool'
  | 'json'
  | 'url'
  | 'text'
  | 'lang'
  | 'datetime'
  | 'hash';

export interface ToastEntry {
  id: number;
  kind: 'info' | 'success' | 'error';
  text: string;
  details?: string[];
}
