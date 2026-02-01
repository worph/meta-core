# Redis Metadata Storage Architecture

This document describes how MetaMesh stores file metadata in Redis, the key structure, plugin metadata contributions, and special cases like FFmpeg stream handling.

## Overview

MetaMesh uses Redis as its primary metadata storage, storing file metadata as **Redis Hashes** with flattened key-value pairs. This design enables:

- Efficient nested object storage without JSON serialization overhead
- Individual field updates without full document rewrites
- Cross-service access via Redis pub/sub notifications
- Fast VFS lookups using content-based hash IDs

## Key Structure

### Hash-Based Storage

Each file's metadata is stored as a Redis Hash:

```
Hash Key:    file:{hashId}
Hash Fields: property/path (e.g., "fileinfo/duration", "titles/eng")
Hash Values: String values
```

**Example Redis commands:**
```bash
# Store file metadata
HMSET file:bafkr4ih5kapbjzq... \
  "cid_midhash256" "bafkr4ih5kapbjzq..." \
  "filePath" "/files/watch/movie.mkv" \
  "fileinfo/duration" "7200.5" \
  "fileinfo/formatName" "matroska,webm"

# Retrieve all metadata
HGETALL file:bafkr4ih5kapbjzq...

# Get single field
HGET file:bafkr4ih5kapbjzq... "fileinfo/duration"
```

### File Index

A Redis Set tracks all known file hash IDs:

```
Key:   file:__index__
Type:  Set
Value: {hashId1, hashId2, hashId3, ...}
```

This enables fast bulk queries like "get all files" without scanning keys.

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
    └─ Merge metadata into Redis via HMSET
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

## Redis Pub/Sub Notifications

MetaMesh uses Redis pub/sub to notify other services of metadata changes.

### Channels

```
meta-sort:file:batch    # Batch file updates (every 5 seconds)
meta-sort:scan:reset    # Full rescan notification
```

### Batch Update Message Format

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

This enables meta-fuse to update its VFS in near real-time without polling.

## Complete Example

Here's a complete metadata example for a movie file:

```bash
# Query all metadata
redis-cli HGETALL file:bafkr4ih5kapbjzq...

# Result (reconstructed as nested object)
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

## Debugging

```bash
# List all file keys
docker exec meta-sort-dev redis-cli KEYS 'file:*' | head -20

# Get all metadata for a file
docker exec meta-sort-dev redis-cli HGETALL 'file:bafkr4ih5...'

# Get specific field
docker exec meta-sort-dev redis-cli HGET 'file:bafkr4ih5...' 'fileinfo/duration'

# Count total files
docker exec meta-sort-dev redis-cli SCARD 'file:__index__'

# Monitor metadata changes in real-time
docker exec meta-sort-dev redis-cli SUBSCRIBE 'meta-sort:file:batch'
```
