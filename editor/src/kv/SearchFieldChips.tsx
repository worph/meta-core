import { useState } from 'react';

interface Props {
  fields: string[];
  onChange: (fields: string[]) => void;
}

// Pill list of field names that the value-search uses. Each pill is removable;
// the inline input adds a new field on Enter or comma. Persisting to
// localStorage is the parent's responsibility.
export function SearchFieldChips({ fields, onChange }: Props) {
  const [input, setInput] = useState('');

  const add = (raw: string) => {
    const next = raw.trim().replace(/,+$/, '');
    if (!next) return;
    if (fields.includes(next)) return;
    onChange([...fields, next]);
    setInput('');
  };

  const remove = (f: string) => onChange(fields.filter((x) => x !== f));

  return (
    <div className="kv-chips">
      <span className="kv-chips-label">search in:</span>
      {fields.map((f) => (
        <span key={f} className="kv-chip">
          <code>{f}</code>
          <button
            className="kv-chip-x"
            onClick={() => remove(f)}
            title={`remove ${f}`}
            aria-label={`remove ${f}`}
          >×</button>
        </span>
      ))}
      <input
        className="kv-chip-input"
        type="text"
        value={input}
        placeholder="+ field"
        onChange={(e) => {
          const v = e.target.value;
          if (v.endsWith(',')) add(v);
          else setInput(v);
        }}
        onKeyDown={(e) => {
          if (e.key === 'Enter') { e.preventDefault(); add(input); }
          if (e.key === 'Backspace' && input === '' && fields.length > 0) {
            remove(fields[fields.length - 1]);
          }
        }}
      />
    </div>
  );
}
