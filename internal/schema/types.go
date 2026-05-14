package schema

import "time"

// Primitive is the coarse value type used by the schema indexer.
// PrimMixed appears only as an aggregate label on a field, never per value.
type Primitive string

const (
	PrimString    Primitive = "string"
	PrimInt       Primitive = "int"
	PrimFloat     Primitive = "float"
	PrimBool      Primitive = "bool"
	PrimJSONObj   Primitive = "json-object"
	PrimJSONArr   Primitive = "json-array"
	PrimUndefined Primitive = "undefined"
	PrimMixed     Primitive = "mixed"
)

// Hint is a semantic refinement on top of a primitive (e.g. cid, timestamp).
// A hint is promoted onto a field only when every non-undefined value matches it.
type Hint string

const (
	HintCID       Hint = "cid"
	HintTimestamp Hint = "timestamp"
)

// KeyHint describes the shape of the subkey dimension when a field is keyed
// by a structured discriminator (e.g. title/eng, title/fre → language-code).
type KeyHint string

const KeyHintLanguageCode KeyHint = "language-code"

// FieldSchema is the per-field entry returned by GET /api/schema.
type FieldSchema struct {
	Type      Primitive      `json:"type"`
	Hint      Hint           `json:"hint,omitempty"`
	KeyHint   KeyHint        `json:"key_hint,omitempty"`
	Breakdown map[string]int `json:"breakdown"`
}

// SchemaResponse is the top-level payload for GET /api/schema.
type SchemaResponse struct {
	Fields      map[string]*FieldSchema `json:"fields"`
	GeneratedAt time.Time               `json:"generated_at"`
	Source      string                  `json:"source"`
}
