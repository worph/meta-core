import { useState, useRef, useEffect, useMemo } from 'react';
import { iso6393, Language } from 'iso-639-3';
import './LanguageCombobox.css';

// Build language lookup maps
const languagesByCode = new Map<string, Language>();
for (const lang of iso6393) {
  languagesByCode.set(lang.iso6393, lang);
}

// Get display name for a language code
export function getLanguageName(code: string): string {
  const lang = languagesByCode.get(code);
  if (lang) {
    return lang.name;
  }
  return code.toUpperCase();
}

// Format for display: "English (eng)" or "eng" for unknown
export function formatLanguageOption(code: string): string {
  const lang = languagesByCode.get(code);
  if (lang) {
    return `${lang.name} (${code})`;
  }
  return code;
}

interface LanguageComboboxProps {
  value: string;
  onChange: (code: string) => void;
  disabled?: boolean;
  placeholder?: string;
}

export function LanguageCombobox({
  value,
  onChange,
  disabled = false,
  placeholder = 'Search language...',
}: LanguageComboboxProps) {
  const [isOpen, setIsOpen] = useState(false);
  const [search, setSearch] = useState('');
  const [highlightedIndex, setHighlightedIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  // Filter languages based on search
  const filteredLanguages = useMemo(() => {
    const searchLower = search.toLowerCase().trim();
    if (!searchLower) {
      // Show common languages first when no search
      const common = ['eng', 'jpn', 'zho', 'kor', 'fra', 'deu', 'spa', 'ita', 'por', 'rus'];
      const commonLangs = common
        .map(code => languagesByCode.get(code))
        .filter((l): l is typeof iso6393[0] => l !== undefined);
      return commonLangs;
    }

    return iso6393.filter((lang: Language) => {
      const nameMatch = lang.name.toLowerCase().includes(searchLower);
      const codeMatch = lang.iso6393.toLowerCase().includes(searchLower);
      const iso1Match = lang.iso6391 && lang.iso6391.toLowerCase().includes(searchLower);
      return nameMatch || codeMatch || iso1Match;
    }).slice(0, 50); // Limit results for performance
  }, [search]);

  // Check if search is a valid custom 3-letter code
  const isValidCustomCode = useMemo(() => {
    const trimmed = search.trim().toLowerCase();
    return trimmed.length === 3 && /^[a-z]{3}$/.test(trimmed) && !languagesByCode.has(trimmed);
  }, [search]);

  // Reset highlight when filtered list changes
  useEffect(() => {
    setHighlightedIndex(0);
  }, [filteredLanguages.length]);

  // Scroll highlighted item into view
  useEffect(() => {
    if (isOpen && listRef.current) {
      const highlighted = listRef.current.querySelector('.highlighted');
      if (highlighted) {
        highlighted.scrollIntoView({ block: 'nearest' });
      }
    }
  }, [highlightedIndex, isOpen]);

  const handleSelect = (code: string) => {
    onChange(code);
    setSearch('');
    setIsOpen(false);
    inputRef.current?.blur();
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (!isOpen) {
      if (e.key === 'ArrowDown' || e.key === 'Enter') {
        setIsOpen(true);
        e.preventDefault();
      }
      return;
    }

    const totalItems = filteredLanguages.length + (isValidCustomCode ? 1 : 0);

    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault();
        setHighlightedIndex(prev => (prev + 1) % totalItems);
        break;
      case 'ArrowUp':
        e.preventDefault();
        setHighlightedIndex(prev => (prev - 1 + totalItems) % totalItems);
        break;
      case 'Enter':
        e.preventDefault();
        if (isValidCustomCode && highlightedIndex === filteredLanguages.length) {
          handleSelect(search.trim().toLowerCase());
        } else if (filteredLanguages[highlightedIndex]) {
          handleSelect(filteredLanguages[highlightedIndex].iso6393);
        }
        break;
      case 'Escape':
        setIsOpen(false);
        setSearch('');
        inputRef.current?.blur();
        break;
    }
  };

  const displayValue = isOpen ? search : formatLanguageOption(value);

  return (
    <div className={`language-combobox ${isOpen ? 'open' : ''} ${disabled ? 'disabled' : ''}`}>
      <input
        ref={inputRef}
        type="text"
        className="language-combobox-input"
        value={displayValue}
        onChange={(e) => {
          setSearch(e.target.value);
          setIsOpen(true);
        }}
        onFocus={() => {
          setSearch('');
          setIsOpen(true);
        }}
        onBlur={() => {
          // Delay to allow click on option
          setTimeout(() => {
            setIsOpen(false);
            setSearch('');
          }, 200);
        }}
        onKeyDown={handleKeyDown}
        disabled={disabled}
        placeholder={placeholder}
      />
      <span className="language-combobox-arrow">▼</span>

      {isOpen && (
        <div className="language-combobox-dropdown" ref={listRef}>
          {filteredLanguages.map((lang: Language, index: number) => (
            <div
              key={lang.iso6393}
              className={`language-combobox-option ${index === highlightedIndex ? 'highlighted' : ''} ${lang.iso6393 === value ? 'selected' : ''}`}
              onMouseDown={() => handleSelect(lang.iso6393)}
              onMouseEnter={() => setHighlightedIndex(index)}
            >
              <span className="lang-option-name">{lang.name}</span>
              <span className="lang-option-code">{lang.iso6393}</span>
            </div>
          ))}

          {isValidCustomCode && (
            <div
              className={`language-combobox-option custom ${highlightedIndex === filteredLanguages.length ? 'highlighted' : ''}`}
              onMouseDown={() => handleSelect(search.trim().toLowerCase())}
              onMouseEnter={() => setHighlightedIndex(filteredLanguages.length)}
            >
              <span className="lang-option-name">Use custom code</span>
              <span className="lang-option-code">{search.trim().toLowerCase()}</span>
            </div>
          )}

          {filteredLanguages.length === 0 && !isValidCustomCode && (
            <div className="language-combobox-empty">
              No languages found. Type a 3-letter code to add custom.
            </div>
          )}
        </div>
      )}
    </div>
  );
}
