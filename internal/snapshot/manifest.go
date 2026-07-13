// Package snapshot implements export/import/wipe of MetaMesh metadata.
//
// The on-disk format is a ZIP archive containing one nested-JSON file per
// indexed root. Schema is defined by ManifestSchemaVersion; readers must
// reject archives whose schemaVersion exceeds what they know.
//
// v2 (current): root identifiers are UUIDv7 (Crockford Base32). Entries
// under metadata/<root>.json carry the flat string properties; the cid
// reverse-index and the duplicates SET are NOT serialized — they are rebuilt
// transparently on import because the cids/<cid> key-set members ARE flat
// properties, and SetMetadataFlat / MergeMetadataFlat auto-register a
// reverse-index alias for every one they see (see
// internal/storage/cid_resolution.go: cidFromKeysetField). The legacy flat
// cid_*/midhash256 named fields are not recognised.
//
// v1 (legacy): roots were content-hash-typed tokens like "midhash256:abc".
// v1 archives can no longer be imported — they would resurrect the
// privileged-hashID schema this layer was built to retire.
package snapshot

import "time"

// ManifestSchemaVersion is the on-disk format version. Bump when the layout
// of metadata/{root}.json or the manifest changes in a non-additive way.
const ManifestSchemaVersion = 2

// MetadataDir is the path inside the ZIP that holds per-CID JSON files.
const MetadataDir = "metadata/"

// ManifestFile is the path inside the ZIP that holds the manifest JSON.
const ManifestFile = "manifest.json"

// IndexFile is the path inside the ZIP that holds the file index (CID list).
const IndexFile = "index.json"

// Manifest is the metadata header stored at the root of every snapshot ZIP.
type Manifest struct {
	SchemaVersion int       `json:"schemaVersion"`
	GeneratedAt   time.Time `json:"generatedAt"`
	SourceHost    string    `json:"sourceHost,omitempty"`
	MetaCoreVer   string    `json:"metaCoreVersion,omitempty"`
	FileCount     int       `json:"fileCount"`
	Includes      Includes  `json:"includes"`
}

// Includes records which optional sections are present in a snapshot.
type Includes struct {
	Metadata     bool `json:"metadata"`
	PluginOutput bool `json:"pluginOutput,omitempty"`
}

// IndexFileBody is the structure of index.json inside the ZIP.
type IndexFileBody struct {
	HashIDs []string `json:"hashIds"`
}
