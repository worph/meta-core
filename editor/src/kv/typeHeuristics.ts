import { FieldType } from './types';

// Path-pattern table. The leftmost segment of the path that matches wins.
// Order matters: more specific patterns should appear first.
const PATH_PATTERNS: Array<[RegExp, FieldType]> = [
  [/(^|\/)(language|lang|audioLang|subLang)$/i, 'lang'],
  [/(^|\/)(cid_|hash$|sha\d|midhash|md5|crc32)/i, 'hash'],
  [/(^|\/)(duration|bitrate|fps|width|height|count|size|sizeByte|increment|episode|season|year|runtime|trackId|index)$/i, 'number'],
  [/(^|\/)(enabled|active|disabled|isHidden|hidden|favorite|watched)$/i, 'bool'],
  [/(^|\/)(url|uri|href|link|posterUrl|backdropUrl|imageUrl)$/i, 'url'],
  [/(^|\/)(.+_at|.+At|.+Date|.+Time|timestamp|createdAt|updatedAt)$/i, 'datetime'],
  [/(^|\/)(description|overview|notes?|comment|summary|nfo)$/i, 'text'],
];

function detectFromPath(path: string): FieldType | null {
  for (const [re, t] of PATH_PATTERNS) {
    if (re.test(path)) return t;
  }
  return null;
}

function detectFromValue(v: string): FieldType {
  if (v === '') return 'string';
  if (v === 'true' || v === 'false') return 'bool';
  if (/^-?\d+$/.test(v)) return 'number';
  if (/^-?\d*\.\d+$/.test(v)) return 'number';
  if (/^https?:\/\//i.test(v)) return 'url';
  const trimmed = v.trim();
  if ((trimmed.startsWith('{') && trimmed.endsWith('}')) || (trimmed.startsWith('[') && trimmed.endsWith(']'))) {
    try { JSON.parse(trimmed); return 'json'; } catch {}
  }
  if (v.includes('\n') || v.length > 200) return 'text';
  return 'string';
}

// Combine path and value heuristics. Path wins when it matches; otherwise
// fall back to value shape.
export function detectType(path: string, value: string): FieldType {
  return detectFromPath(path) ?? detectFromValue(value);
}

export const ALL_TYPES: FieldType[] = [
  'auto', 'string', 'text', 'number', 'bool', 'json', 'url', 'lang', 'datetime', 'hash',
];

export function describeType(t: FieldType): string {
  switch (t) {
    case 'auto': return 'auto';
    case 'string': return 'string';
    case 'text': return 'text (multiline)';
    case 'number': return 'number';
    case 'bool': return 'bool';
    case 'json': return 'json';
    case 'url': return 'url';
    case 'lang': return 'language code';
    case 'datetime': return 'datetime';
    case 'hash': return 'hash / cid';
  }
}
