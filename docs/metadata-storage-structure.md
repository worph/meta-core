# Redis Metadata Storage Architecture

This document describes how MetaMesh stores file metadata in Redis, the key structure, plugin metadata contributions, and special cases like FFmpeg stream handling.

## Overview

MetaMesh uses Redis as its primary metadata storage, storing file metadata as **flat string keys**. Each property is stored as a separate Redis key rather than using Redis Hashes. This design enables:

- **Redis keyspace notifications** for field-level change detection
- **Pattern-based subscriptions** (e.g., `file:*/tmdb/*` for TMDB updates)
- **meta:events stream** integration for real-time updates across services
- Individual field updates without affecting other properties
- Fast VFS lookups using content-based hash IDs

## Key Structure

### Flat Key Storage

Each file property is stored as a separate Redis STRING key:

```
Key Pattern:  file:{hashId}/{property/path}
Key Type:     STRING
Value:        String value
```

**Example Redis commands:**
```bash
# Store file metadata (each property is a separate key)
SET "file:bafkr4ih5kapbjzq.../cid_midhash256" "bafkr4ih5kapbjzq..."
SET "file:bafkr4ih5kapbjzq.../filePath" "/files/watch/movie.mkv"
SET "file:bafkr4ih5kapbjzq.../fileinfo/duration" "7200.5"
SET "file:bafkr4ih5kapbjzq.../fileinfo/formatName" "matroska,webm"

# Retrieve single field
GET "file:bafkr4ih5kapbjzq.../fileinfo/duration"

# Retrieve all metadata for a file (scan by prefix)
SCAN 0 MATCH "file:bafkr4ih5kapbjzq.../*" COUNT 1000
```

### Why Flat Keys?

The flat key structure (vs. Redis Hashes) enables:

1. **Keyspace Notifications**: Redis publishes events when individual keys change
2. **Pattern Subscriptions**: Services can subscribe to `__keyspace@0__:file:*/tmdb/*`
3. **Selective Updates**: Update one field without touching others
4. **Stream Integration**: The `meta:events` stream captures field-level changes

### File Index

A Redis Set tracks all known file hash IDs:

```
Key:   file:__index__
Type:  Set
Value: {hashId1, hashId2, hashId3, ...}
```

This enables fast bulk queries like "get all files" without scanning keys.

```bash
# Get all hash IDs
SMEMBERS "file:__index__"

# Count total files
SCARD "file:__index__"

# Check if a file exists
SISMEMBER "file:__index__" "bafkr4ih5kapbjzq..."
```

### Keyspace Notifications

Redis keyspace notifications are enabled to detect metadata changes. This requires the `notify-keyspace-events` configuration:

```bash
# Required Redis config (set by meta-core)
CONFIG SET notify-keyspace-events "K$"

# K = Keyspace events (published on __keyspace@<db>__:<key>)
# $ = String commands (SET, etc.)
```

Services can subscribe to keyspace events for real-time updates:

```bash
# Subscribe to all file key changes
PSUBSCRIBE '__keyspace@0__:file:*'

# Subscribe to specific field pattern (e.g., TMDB updates)
PSUBSCRIBE '__keyspace@0__:file:*/tmdb/*'
```

### meta:events Stream

The `meta:events` stream provides reliable event delivery for file system events from meta-core:

```
Stream Key: meta:events
Entry Fields:
  - type: add | change | delete | rename | reset
  - path: File path relative to /files
  - midhash256: Content hash (for add/change events)
  - oldPath: Previous path (for rename events)
  - timestamp: Event timestamp
```

Consumer groups enable reliable processing with acknowledgment:

```bash
# Create consumer group
XGROUP CREATE meta:events mygroup 0 MKSTREAM

# Read new events (blocking)
XREADGROUP GROUP mygroup myconsumer BLOCK 5000 STREAMS meta:events >

# Acknowledge processed events
XACK meta:events mygroup <entry-id>
```

## Primary Hash ID

The primary identifier for each file is `cid_midhash256` - an IPFS-compatible CID generated from:

1. SHA-256 hash of the middle 1MB of the file
2. File size in bytes
3. IPFS CID v1 encoding (multicodec 0x1000)

**Example:** `bafkr4ih5kapbjzqvylwj7kx7zmkpxn6qj5xw...`

This provides:
- Content-based addressing (same file = same ID)
- Fast computation (only reads 1MB)
- IPFS compatibility for future distributed storage

### Collision Handling

If two different files produce the same midhash (rare), the system falls back to full SHA-256:

```typescript
// Normal case
hashId = midHash256;  // "bafkr4ih5kapbjzq..."

// Collision detected (different filePath with same midhash)
hashId = metadata['cid_sha2-256'];  // Full hash fallback
```

## Metadata Flattening

Nested objects are flattened into path-based keys for efficient Redis storage.

### Flattening Rules

```typescript
// Input (nested object)
{
  title: "Inception",
  video: {
    codec: "h265",
    width: "1920"
  },
  streams: [
    { language: "eng" },
    { language: "jpn" }
  ]
}

// Output (flattened for Redis)
title                  = "Inception"
video/codec            = "h265"
video/width            = "1920"
streams/0/language     = "eng"
streams/1/language     = "jpn"
```

**Key rules:**
- Nested objects use `/` as path separator
- Arrays use numeric indices (0, 1, 2...)
- All values stored as strings
- `null`/`undefined` stored as empty strings
- Numbers and booleans converted via `String()`

### Reconstruction

When reading, the flattened structure is reconstructed into nested objects:

```typescript
const metadata = await kvClient.getMetadataFlat(hashId);
// Returns: { title: "Inception", video: { codec: "h265", width: "1920" }, ... }
```

## Plugin Metadata Structure

Each plugin writes metadata using a prefixed key pattern:

```
{plugin-id}/{field}                   # Simple field
{plugin-id}/{group}/{field}           # Grouped field
{plugin-id}/{group}/{index}/{field}   # Indexed field (for arrays)
```

### Plugin Contribution Flow

```
File Discovered
    ↓
[FileProcessorPiscina]
    ├─ Compute midhash256
    ├─ Store preliminary metadata
    └─ Dispatch plugin tasks
         ↓
[Plugin Processing]
    ├─ file-info    → fileType, mimeType, sizeByte
    ├─ ffmpeg       → fileinfo/duration, fileinfo/streamdetails/...
    ├─ filename-parser → titles/eng, season, episode, videoType
    ├─ anime-detector  → anime, titles/jpn, titles/jpl
    ├─ tmdb        → tmdbid, plot/eng, genres
    └─ full-hash   → cid_sha2-256, cid_sha1
         ↓
[Plugin Callbacks]
    └─ Store metadata via flat keys (SET per property)
```

## Core Metadata Types

### HashMeta - Content Identification

```
cid_crc32         = "bafkr..."    # CRC32 (multicodec 0x0132)
cid_md5           = "bafkr..."    # MD5 (multicodec 0xd5)
cid_sha1          = "bafkr..."    # SHA-1 (multicodec 0x11)
cid_sha2-256      = "bafkr..."    # SHA-256 (multicodec 0x12)
cid_midhash256    = "bafkr..."    # Middle-hash (PRIMARY ID, multicodec 0x1000)
```

### FileStatMeta - File Properties

```
fileType          = "video"              # video, audio, image, text
mimeType          = "video/x-matroska"   # MIME type
sizeByte          = "4294967296"         # File size in bytes
```

### VideoMeta - Classification

```
videoType         = "movie"              # movie, episode, unknown
originalTitle     = "Inception"
titles/eng        = "Inception"          # Localized titles
titles/jpn        = "インセプション"
season            = "1"
episode           = "5"
movieYear         = "2010"
```

### AnimeMeta - Anime Detection

```
anime             = "true"               # "true" or "false"
titles/jpn        = "進撃の巨人"         # Japanese title
titles/jpl        = "Shingeki no Kyojin" # Romaji
```

### SubtitleMeta - Subtitle References

```
subtitles/eng/0   = "bafkr..."          # Reference to subtitle file CID
subtitles/eng/1   = "bafks..."
subtitles/jpn/0   = "bafkt..."
```

### JellyfinMeta - Comprehensive Metadata

```
imdbid            = "tt1375666"
tmdbid            = "27205"
anidbid           = "12345"
plot/eng          = "A movie about..."
genre/0           = "Action"
genre/1           = "Sci-Fi"
rating            = "8.8"
mpaa              = "PG-13"
releasedate       = "2010-07-16"
art/poster        = "bafkr..."          # Reference to poster image CID
art/fanart        = "bafks..."          # Reference to backdrop image CID
```

## FFmpeg Plugin - Special Case

The FFmpeg plugin extracts detailed stream information with a specific nested structure.

### Stream Details Structure

```
fileinfo/duration                              = "7200.5"
fileinfo/formatName                            = "matroska,webm"
fileinfo/streamdetails/video/0/codec           = "h264"
fileinfo/streamdetails/video/0/width           = "1920"
fileinfo/streamdetails/video/0/height          = "1080"
fileinfo/streamdetails/video/0/aspect          = "16:9"
fileinfo/streamdetails/video/0/bitrate         = "8000000"
fileinfo/streamdetails/audio/0/codec           = "aac"
fileinfo/streamdetails/audio/0/language        = "eng"
fileinfo/streamdetails/audio/0/channels        = "6"
fileinfo/streamdetails/audio/1/codec           = "dts"
fileinfo/streamdetails/audio/1/language        = "fra"
fileinfo/streamdetails/audio/1/channels        = "6"
fileinfo/streamdetails/subtitle/0/language     = "eng"
fileinfo/streamdetails/subtitle/0/format       = "subrip"
fileinfo/streamdetails/subtitle/1/language     = "jpn"
fileinfo/streamdetails/subtitle/1/format       = "ass"
fileinfo/streamdetails/embeddedimage/0/type    = "cover"
```

### Stream Type Keys

| Stream Type | Key Pattern | Description |
|------------|-------------|-------------|
| Video | `fileinfo/streamdetails/video/{n}/...` | Video streams (0, 1, 2...) |
| Audio | `fileinfo/streamdetails/audio/{n}/...` | Audio tracks |
| Subtitle | `fileinfo/streamdetails/subtitle/{n}/...` | Embedded subtitles |
| Embedded Image | `fileinfo/streamdetails/embeddedimage/{n}/...` | Cover art, thumbnails |

### Common Stream Fields

**Video streams:**
- `codec` - h264, hevc, av1, vp9, etc.
- `width`, `height` - Resolution
- `aspect` - Aspect ratio
- `bitrate` - Video bitrate
- `profile` - Codec profile (main, high, etc.)
- `level` - Codec level

**Audio streams:**
- `codec` - aac, dts, ac3, flac, etc.
- `language` - ISO 639-2 code (eng, jpn, fra)
- `channels` - Channel count (2, 6, 8)
- `bitrate` - Audio bitrate
- `samplerate` - Sample rate

**Subtitle streams:**
- `language` - ISO 639-2 code
- `format` - subrip, ass, pgs, vobsub

## Real-Time Notifications

MetaMesh provides multiple mechanisms for real-time metadata change notifications:

### 1. meta:events Stream (Recommended)

The `meta:events` Redis stream provides reliable, ordered event delivery with consumer groups:

```bash
# File system events from meta-core watcher
XREAD STREAMS meta:events 0

# Event types: add, change, delete, rename, reset
```

Benefits:
- **Reliable delivery** via consumer groups and acknowledgment
- **Replay capability** for catching up after restarts
- **Ordered processing** with stream IDs

### 2. Keyspace Notifications

For field-level change detection, subscribe to Redis keyspace notifications:

```bash
# All file metadata changes
PSUBSCRIBE '__keyspace@0__:file:*'

# Specific plugin updates (e.g., TMDB enrichment)
PSUBSCRIBE '__keyspace@0__:file:*/tmdb/*'
```

### Cache Invalidation

The WebDAV cache in meta-core integrates with keyspace notifications for automatic invalidation:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Cache Invalidation Flow                       │
│                                                                  │
│  Redis Keyspace                Cache                             │
│  ┌──────────────┐              Invalidator                       │
│  │ SET file:*/  │ ──────────► ┌──────────────┐                  │
│  │ DEL file:*/  │  subscribe  │ Listens to   │                  │
│  └──────────────┘              │ __keyspace@  │                  │
│                                │ 0__:file:*   │                  │
│                                └──────┬───────┘                  │
│                                       │                          │
│                                       ▼                          │
│                                ┌──────────────┐                  │
│                                │ Remove from  │                  │
│                                │ LRU cache    │                  │
│                                └──────────────┘                  │
└─────────────────────────────────────────────────────────────────┘
```

Flow:
1. Cache invalidator subscribes to `__keyspace@0__:file:*`
2. When metadata changes (SET/DEL), invalidator receives notification
3. Invalidator removes the corresponding file from the WebDAV cache
4. Next access fetches fresh copy from source filesystem

This ensures cached files stay synchronized with metadata changes.

### 3. Pub/Sub Channels (Legacy)

Batch notification channels are still supported:

```
meta-sort:file:batch    # Batch file updates (every 5 seconds)
meta-sort:scan:reset    # Full rescan notification
```

```json
{
  "timestamp": 1704067200000,
  "changes": [
    { "action": "add", "hashId": "bafkr4ih5..." },
    { "action": "update", "hashId": "bafkr4ih5..." },
    { "action": "remove", "hashId": "bafkr4ih5..." }
  ]
}
```

## Complete Example

Here's a complete metadata example for a movie file:

```bash
# Query all keys for a file
redis-cli --scan --pattern 'file:bafkr4ih5kapbjzq.../*'

# Example flat keys stored in Redis:
# file:bafkr4ih5kapbjzq.../cid_midhash256      = "bafkr4ih5kapbjzq..."
# file:bafkr4ih5kapbjzq.../filePath            = "/files/watch/movies/Inception.2010.1080p.BluRay.x264.mkv"
# file:bafkr4ih5kapbjzq.../fileType            = "video"
# file:bafkr4ih5kapbjzq.../fileinfo/duration   = "8878.5"
# file:bafkr4ih5kapbjzq.../titles/eng          = "Inception"
# ...

# When reconstructed by the API (nested object representation)
{
  # Content identification
  "cid_midhash256": "bafkr4ih5kapbjzq...",
  "cid_sha2-256": "bafks7ij2kbpck...",
  "filePath": "/files/watch/movies/Inception.2010.1080p.BluRay.x264.mkv",

  # File properties
  "fileType": "video",
  "mimeType": "video/x-matroska",
  "sizeByte": "4294967296",

  # Classification
  "videoType": "movie",
  "originalTitle": "Inception",
  "movieYear": "2010",

  # Localized titles
  "titles": {
    "eng": "Inception",
    "fra": "Inception",
    "jpn": "インセプション"
  },

  # FFmpeg stream details
  "fileinfo": {
    "duration": "8878.5",
    "formatName": "matroska,webm",
    "streamdetails": {
      "video": {
        "0": {
          "codec": "h264",
          "width": "1920",
          "height": "1080",
          "aspect": "16:9",
          "bitrate": "8000000"
        }
      },
      "audio": {
        "0": { "codec": "dts", "language": "eng", "channels": "6" },
        "1": { "codec": "ac3", "language": "fra", "channels": "6" }
      },
      "subtitle": {
        "0": { "language": "eng", "format": "subrip" },
        "1": { "language": "fra", "format": "subrip" }
      }
    }
  },

  # External metadata (TMDB)
  "tmdbid": "27205",
  "imdbid": "tt1375666",
  "plot": {
    "eng": "A thief who steals corporate secrets through dream-sharing technology..."
  },
  "genre": ["Action", "Science Fiction", "Adventure"],
  "rating": "8.4",
  "releasedate": "2010-07-16",

  # Media assets
  "art": {
    "poster": "bafkr4poster...",
    "fanart": "bafkr4fanart..."
  },

  # Detection flags
  "anime": "false"
}
```

## Key Implementation Files

| Component | File Path |
|-----------|-----------|
| Redis Client | `packages/meta-sort/packages/meta-sort-core/src/kv/RedisClient.ts` |
| Metadata Utils | `packages/meta-sort/packages/meta-sort-core/src/kv/MetadataUtils.ts` |
| KV Interface | `packages/meta-sort/packages/meta-sort-core/src/kv/IKVClient.ts` |
| File Processor | `packages/meta-sort/packages/meta-sort-core/src/logic/fileProcessor/FileProcessorPiscina.ts` |
| Pub/Sub Pipeline | `packages/meta-sort/packages/meta-sort-core/src/logic/pipeline/StreamingPipeline.ts` |
| Type Definitions | `packages/meta-interface/src/lib/metadata-type/*.ts` |
| meta-core Storage | `packages/meta-core/internal/storage/client.go` |
| meta-core Events | `packages/meta-core/internal/events/publisher.go` |
| meta-core Cache | `packages/meta-core/internal/cache/` |

## Debugging

```bash
# List all file hash IDs
docker exec meta-core-dev redis-cli SMEMBERS 'file:__index__'

# Count total files
docker exec meta-core-dev redis-cli SCARD 'file:__index__'

# Get a sample hash ID
docker exec meta-core-dev redis-cli SRANDMEMBER 'file:__index__'

# List all keys for a specific file (flat key scan)
docker exec meta-core-dev redis-cli --scan --pattern 'file:bafkr4ih5.../*'

# Get specific field (flat key)
docker exec meta-core-dev redis-cli GET 'file:bafkr4ih5.../fileinfo/duration'

# Get filePath for a file
docker exec meta-core-dev redis-cli GET 'file:bafkr4ih5.../filePath'

# Verify keys are STRING type (not HASH)
docker exec meta-core-dev redis-cli TYPE 'file:bafkr4ih5.../filePath'
# Should return: string

# Monitor metadata changes via stream
docker exec meta-core-dev redis-cli XREAD STREAMS 'meta:events' 0

# Monitor keyspace notifications (requires notify-keyspace-events enabled)
docker exec meta-core-dev redis-cli PSUBSCRIBE '__keyspace@0__:file:*'

# meta-core API debugging
# Cache status
curl http://localhost:9000/api/cache/status

# Clear cache
curl -X POST http://localhost:9000/api/cache/clear

# Watch events stream (SSE)
curl http://localhost:9000/api/events/stream

# KV browser - storage info
curl http://localhost:9000/api/kv/info

# Scan status
curl http://localhost:9000/api/scan/status
```

## meta:events Stream

The `meta:events` stream captures metadata change events for real-time notifications:

```bash
# View recent events
docker exec meta-core-dev redis-cli XRANGE 'meta:events' - + COUNT 10

# Read events from a consumer group
docker exec meta-core-dev redis-cli XREADGROUP GROUP mygroup myconsumer STREAMS 'meta:events' >
```

Stream events contain:
- `type`: Event type (add, change, delete, rename, reset)
- `path`: File path
- `midhash256`: Content hash (for add/change)
- `timestamp`: Event timestamp
