// Package snapshot implements export/import/wipe of MetaMesh metadata.
//
// The on-disk format is a ZIP archive containing one nested-JSON file per
// known CID (cid_midhash256). Schema is defined by ManifestSchemaVersion;
// readers must reject archives whose schemaVersion exceeds what they know.
package snapshot

import "time"

// ManifestSchemaVersion is the on-disk format version. Bump when the layout
// of metadata/{cid}.json or the manifest changes in a non-additive way.
const ManifestSchemaVersion = 1

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
