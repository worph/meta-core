import { SearchResult } from '../types';
import { getDisplayName, truncateHash, formatBytes } from '../utils/formatting';
import './FileList.css';

interface FileListProps {
  results: SearchResult[];
  selectedHashId?: string;
  selectedHashIds: Set<string>;
  bulkEditMode: boolean;
  onSelectFile: (hashId: string) => void;
  onToggleSelection: (hashId: string) => void;
  onSelectAll: () => void;
  onClearSelection: () => void;
  onBulkEditMode: (enabled: boolean) => void;
}

export function FileList({
  results,
  selectedHashId,
  selectedHashIds,
  bulkEditMode,
  onSelectFile,
  onToggleSelection,
  onSelectAll,
  onClearSelection,
  onBulkEditMode,
}: FileListProps) {
  if (results.length === 0) {
    return (
      <div className="file-list-empty">
        <p>No files found. Try searching for a title, filename, or hash ID.</p>
      </div>
    );
  }

  const allSelected = results.length > 0 && results.every(r => selectedHashIds.has(r.hashId));

  // Sort results by file path
  const sortedResults = [...results].sort((a, b) => {
    const pathA = a.metadata.filePath || a.metadata.privateFilePath || '';
    const pathB = b.metadata.filePath || b.metadata.privateFilePath || '';
    return pathA.localeCompare(pathB);
  });

  return (
    <div className="file-list">
      <div className="file-list-header">
        <span className="file-count">{results.length} files found</span>
        <button
          className={`bulk-edit-toggle ${bulkEditMode ? 'active' : ''}`}
          onClick={() => onBulkEditMode(!bulkEditMode)}
          title={bulkEditMode ? 'Exit bulk edit mode' : 'Enter bulk edit mode'}
        >
          {bulkEditMode ? '✓ Bulk Edit' : 'Bulk Edit'}
        </button>
      </div>

      {bulkEditMode && (
        <div className="file-list-bulk-actions">
          <button onClick={allSelected ? onClearSelection : onSelectAll} className="select-all-btn">
            {allSelected ? 'Deselect All' : 'Select All'}
          </button>
          {selectedHashIds.size > 0 && (
            <span className="selection-count">
              {selectedHashIds.size} selected
            </span>
          )}
        </div>
      )}

      <div className="file-list-items">
        {sortedResults.map((result) => {
          const isSelected = bulkEditMode
            ? selectedHashIds.has(result.hashId)
            : selectedHashId === result.hashId;

          return (
            <div
              key={result.hashId}
              className={`file-list-item ${isSelected ? 'selected' : ''}`}
              onClick={() => {
                if (bulkEditMode) {
                  onToggleSelection(result.hashId);
                } else {
                  onSelectFile(result.hashId);
                }
              }}
            >
              {bulkEditMode && (
                <div className="file-list-item-checkbox">
                  <input
                    type="checkbox"
                    checked={selectedHashIds.has(result.hashId)}
                    onChange={(e) => {
                      e.stopPropagation();
                      onToggleSelection(result.hashId);
                    }}
                  />
                </div>
              )}
              <div className="file-list-item-content">
                <div className="file-list-item-title">
                  {getDisplayName(result.metadata)}
                </div>
                <div className="file-list-item-details">
                  <span className="file-list-item-type">
                    {result.metadata.videoType || 'Unknown'}
                  </span>
                  <span className="file-list-item-size">
                    {formatBytes(result.metadata.byteSize)}
                  </span>
                </div>
                <div className="file-list-item-hash">
                  Hash: {truncateHash(result.hashId)}
                </div>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
