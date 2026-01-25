import { useState, useEffect } from 'react';
import { SearchBar } from './components/SearchBar';
import { FileList } from './components/FileList';
import { MetadataEditor } from './components/MetadataEditor';
import { BulkMetadataEditor } from './components/BulkMetadataEditor';
import { KVTreeView } from './components/KVTreeView';
import { MetadataAPI } from './api/metadataApi';
import { SearchResult } from './types';
import './App.css';

function App() {
  const [searchResults, setSearchResults] = useState<SearchResult[]>([]);
  const [selectedHashId, setSelectedHashId] = useState<string | undefined>();
  const [selectedHashIds, setSelectedHashIds] = useState<Set<string>>(new Set());
  const [bulkEditMode, setBulkEditMode] = useState(false);
  const [isSearching, setIsSearching] = useState(false);
  const [isLoadingDirectHash, setIsLoadingDirectHash] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Extract hash from URL path (supports /editor/file/hash or /editor/?hash=...)
  const getHashFromUrl = (): string | null => {
    const path = window.location.pathname;
    const fileMatch = path.match(/\/file\/([a-z0-9]+)$/i);
    if (fileMatch) {
      return fileMatch[1];
    }
    // Fallback to query param for backwards compatibility
    const params = new URLSearchParams(window.location.search);
    return params.get('hash');
  };

  // Update URL when file is selected
  const updateUrlForFile = (hashId: string | undefined) => {
    const basePath = window.location.pathname.replace(/\/file\/[a-z0-9]+$/i, '').replace(/\/$/, '');
    if (hashId) {
      const newPath = `${basePath}/file/${hashId}`;
      window.history.pushState({ hashId }, '', newPath);
    } else {
      window.history.pushState({}, '', basePath || '/editor/');
    }
  };

  // Check for hash parameter in URL on component mount
  useEffect(() => {
    const loadHashFromUrl = async () => {
      const hashParam = getHashFromUrl();

      if (hashParam) {
        try {
          setIsLoadingDirectHash(true);
          setError(null);

          // Try to get metadata for this hash
          const metadata = await MetadataAPI.getMetadata(hashParam);

          // Create a search result from the metadata
          const searchResult: SearchResult = {
            hashId: hashParam,
            metadata: metadata,
            score: 1.0,
          };

          // Set the search results and select this file
          setSearchResults([searchResult]);
          setSelectedHashId(hashParam);
        } catch (err: any) {
          setError(`Failed to load file with hash ${hashParam}: ${err.message}`);
          setSearchResults([]);
          setSelectedHashId(undefined);
        } finally {
          setIsLoadingDirectHash(false);
        }
      }
    };

    loadHashFromUrl();

    // Handle browser back/forward
    const handlePopState = (event: PopStateEvent) => {
      const hashId = event.state?.hashId || getHashFromUrl();
      if (hashId) {
        setSelectedHashId(hashId);
      } else {
        setSelectedHashId(undefined);
      }
    };
    window.addEventListener('popstate', handlePopState);
    return () => window.removeEventListener('popstate', handlePopState);
  }, []); // Empty dependency array means this runs once on mount

  const handleSearch = async (query: string) => {
    try {
      setIsSearching(true);
      setError(null);

      // Unified search - searches across all fields
      const response = await MetadataAPI.search({ query });

      setSearchResults(response.results);

      // Auto-select first result if available
      if (response.results.length > 0) {
        setSelectedHashId(response.results[0].hashId);
      } else {
        setSelectedHashId(undefined);
      }
    } catch (err: any) {
      setError(err.message);
      setSearchResults([]);
      setSelectedHashId(undefined);
    } finally {
      setIsSearching(false);
    }
  };

  const handleSelectFile = (hashId: string) => {
    if (!bulkEditMode) {
      setSelectedHashId(hashId);
      setSelectedHashIds(new Set());
      updateUrlForFile(hashId);
    }
  };

  const handleToggleSelection = (hashId: string) => {
    const newSelection = new Set(selectedHashIds);
    if (newSelection.has(hashId)) {
      newSelection.delete(hashId);
    } else {
      newSelection.add(hashId);
    }
    setSelectedHashIds(newSelection);
  };

  const handleSelectAll = () => {
    const allHashIds = new Set(searchResults.map(r => r.hashId));
    setSelectedHashIds(allHashIds);
  };

  const handleClearSelection = () => {
    setSelectedHashIds(new Set());
    setBulkEditMode(false);
  };

  const handleBulkEditMode = (enabled: boolean) => {
    setBulkEditMode(enabled);
    if (enabled) {
      setSelectedHashId(undefined);
    } else {
      setSelectedHashIds(new Set());
    }
  };

  const handleMetadataSaved = () => {
    // Optionally refresh the search results to reflect changes
    console.log('Metadata saved successfully');
  };

  const handleBulkMetadataSaved = () => {
    console.log('Bulk metadata saved successfully');
    handleClearSelection();
  };

  return (
    <div className="app">
      <header className="app-header">
        <h1>MetaMesh Metadata Editor</h1>
        <p className="app-description">
          Search and edit metadata for your media files
        </p>
      </header>

      <main className="app-main">
        <SearchBar onSearch={handleSearch} isLoading={isSearching} />

        {error && <div className="app-error">{error}</div>}

        {isLoadingDirectHash && (
          <div className="app-loading">Loading file metadata...</div>
        )}

        {!isSearching && !isLoadingDirectHash && searchResults.length === 0 && !error && (
          <div className="app-welcome">
            <h2>Welcome to MetaMesh Metadata Editor</h2>
            <p>Search for files by title, filename, file path, or hash ID to get started.</p>
            <p className="app-welcome-features">
              Unified search across all fields | Single file editing | Bulk editing | Direct access via URL
            </p>
            <div className="app-kv-browser">
              <KVTreeView />
            </div>
          </div>
        )}

        {searchResults.length > 0 && (
          <div className="app-content">
            <div className="app-sidebar">
              <FileList
                results={searchResults}
                selectedHashId={selectedHashId}
                selectedHashIds={selectedHashIds}
                bulkEditMode={bulkEditMode}
                onSelectFile={handleSelectFile}
                onToggleSelection={handleToggleSelection}
                onSelectAll={handleSelectAll}
                onClearSelection={handleClearSelection}
                onBulkEditMode={handleBulkEditMode}
              />
            </div>
            <div className="app-editor">
              {bulkEditMode && selectedHashIds.size > 0 ? (
                <BulkMetadataEditor
                  hashIds={Array.from(selectedHashIds)}
                  onSaved={handleBulkMetadataSaved}
                  onCancel={handleClearSelection}
                />
              ) : selectedHashId ? (
                <MetadataEditor
                  key={selectedHashId}
                  hashId={selectedHashId}
                  onSaved={handleMetadataSaved}
                />
              ) : (
                <div className="app-no-selection">
                  {bulkEditMode
                    ? 'Select files from the list to bulk edit metadata'
                    : 'Select a file from the list to edit its metadata'}
                </div>
              )}
            </div>
          </div>
        )}
      </main>

      <footer className="app-footer">
        <p>
          MetaMesh Metadata Editor | Part of{' '}
          <a href="/" target="_blank">
            MetaMesh
          </a>
        </p>
      </footer>
    </div>
  );
}

export default App;
