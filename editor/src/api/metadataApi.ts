import {
  FileMetadata,
  SearchResponse,
  UpdateResult,
  BatchUpdateResponse,
} from '../types';

const API_BASE = '/api/metadata';

export class MetadataAPI {
  /**
   * Search for files by query, hashId, or property
   */
  static async search(params: {
    query?: string;
    hashId?: string;
    property?: string;
    propertyValue?: string;
    limit?: number;
  }): Promise<SearchResponse> {
    const response = await fetch(`${API_BASE}/search`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(params),
    });

    if (!response.ok) {
      const error = await response.json();
      throw new Error(error.error || 'Search failed');
    }

    return response.json();
  }

  /**
   * Get metadata for a specific file by hash ID
   */
  static async getMetadata(hashId: string): Promise<FileMetadata> {
    const response = await fetch(`${API_BASE}/${hashId}`);

    if (!response.ok) {
      const error = await response.json();
      throw new Error(error.error || 'Failed to get metadata');
    }

    return response.json();
  }

  /**
   * Update complete metadata for a file
   */
  static async updateMetadata(
    hashId: string,
    metadata: FileMetadata
  ): Promise<UpdateResult> {
    const response = await fetch(`${API_BASE}/${hashId}`, {
      method: 'PUT',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(metadata),
    });

    if (!response.ok) {
      const error = await response.json();
      throw new Error(error.error || 'Failed to update metadata');
    }

    return response.json();
  }

  /**
   * Update a specific property for a file
   */
  static async updateProperty(
    hashId: string,
    property: string,
    value: string
  ): Promise<UpdateResult> {
    const response = await fetch(`${API_BASE}/${hashId}/property`, {
      method: 'PUT',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ property, value }),
    });

    if (!response.ok) {
      const error = await response.json();
      throw new Error(error.error || 'Failed to update property');
    }

    return response.json();
  }

  /**
   * Batch update multiple files
   */
  static async batchUpdate(
    updates: Array<{
      hashId: string;
      metadata?: FileMetadata;
      properties?: Record<string, string>;
    }>
  ): Promise<BatchUpdateResponse> {
    const response = await fetch(`${API_BASE}/batch`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ updates }),
    });

    if (!response.ok) {
      const error = await response.json();
      throw new Error(error.error || 'Batch update failed');
    }

    return response.json();
  }

  /**
   * Delete metadata for a file
   */
  static async deleteMetadata(hashId: string): Promise<void> {
    const response = await fetch(`${API_BASE}/${hashId}`, {
      method: 'DELETE',
    });

    if (!response.ok) {
      const error = await response.json();
      throw new Error(error.error || 'Failed to delete metadata');
    }
  }

  /**
   * Get all hash IDs
   */
  static async getAllHashIds(): Promise<{ hashIds: string[]; count: number }> {
    const response = await fetch(`${API_BASE}/hash-ids`);

    if (!response.ok) {
      const error = await response.json();
      throw new Error(error.error || 'Failed to get hash IDs');
    }

    return response.json();
  }
}
