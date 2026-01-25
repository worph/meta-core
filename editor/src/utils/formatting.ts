/**
 * Format bytes to human readable format
 */
export function formatBytes(bytes: number | undefined): string {
  if (!bytes) return 'N/A';

  const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
  if (bytes === 0) return '0 B';

  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  return Math.round((bytes / Math.pow(1024, i)) * 100) / 100 + ' ' + sizes[i];
}

/**
 * Format duration in seconds to HH:MM:SS
 */
export function formatDuration(seconds: number | undefined): string {
  if (!seconds) return 'N/A';

  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const secs = Math.floor(seconds % 60);

  if (hours > 0) {
    return `${hours}:${minutes.toString().padStart(2, '0')}:${secs.toString().padStart(2, '0')}`;
  }
  return `${minutes}:${secs.toString().padStart(2, '0')}`;
}

/**
 * Truncate hash to first and last 6 characters
 */
export function truncateHash(hash: string | undefined): string {
  if (!hash) return 'N/A';
  if (hash.length <= 16) return hash;
  return `${hash.slice(0, 6)}...${hash.slice(-6)}`;
}

/**
 * Get display name for a file from metadata
 */
export function getDisplayName(metadata: any): string {
  if (metadata.title) {
    if (metadata.season && metadata.episode) {
      return `${metadata.title} S${String(metadata.season).padStart(2, '0')}E${String(metadata.episode).padStart(2, '0')}`;
    }
    if (metadata.movieYear) {
      return `${metadata.title} (${metadata.movieYear})`;
    }
    return metadata.title;
  }
  return metadata.fileName || 'Unknown';
}
