package workgraph

import "encoding/json"

// jsonMarshal / jsonUnmarshal are the package-local indirections over
// encoding/json. Declaring them here keeps the import surface narrow and lets
// a future swap (e.g. to a streaming decoder, or to the canonical encoder in
// serialize.go) touch one place. They are NOT exported: callers go through
// the typed helpers (MarshalWorkGraph, etc.).
func jsonMarshal(v any) ([]byte, error)      { return json.Marshal(v) }
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
