import { useEffect } from 'react';
import { ToastEntry } from './types';

interface Props {
  toasts: ToastEntry[];
  onDismiss: (id: number) => void;
}

export function ToastStack({ toasts, onDismiss }: Props) {
  return (
    <div className="kv-toast-stack">
      {toasts.map((t) => (
        <Toast key={t.id} entry={t} onDismiss={() => onDismiss(t.id)} />
      ))}
    </div>
  );
}

function Toast({ entry, onDismiss }: { entry: ToastEntry; onDismiss: () => void }) {
  useEffect(() => {
    if (entry.kind === 'error') return;
    const timer = setTimeout(onDismiss, 4500);
    return () => clearTimeout(timer);
  }, [entry.kind, onDismiss]);

  return (
    <div className={`kv-toast kv-toast-${entry.kind}`}>
      <div className="kv-toast-row">
        <span className="kv-toast-text">{entry.text}</span>
        <button className="kv-toast-close" onClick={onDismiss} aria-label="dismiss">×</button>
      </div>
      {entry.details && entry.details.length > 0 && (
        <ul className="kv-toast-details">
          {entry.details.slice(0, 8).map((d, i) => <li key={i}>{d}</li>)}
          {entry.details.length > 8 && <li>… {entry.details.length - 8} more</li>}
        </ul>
      )}
    </div>
  );
}

let nextId = 1;
export function makeToast(kind: ToastEntry['kind'], text: string, details?: string[]): ToastEntry {
  return { id: nextId++, kind, text, details };
}
