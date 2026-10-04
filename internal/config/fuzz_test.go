package config

import (
	"bytes"
	"testing"
)

// The config loader parses files an operator (or a compromised/rewritten file
// on disk) supplies. It must reject any malformed input cleanly and never
// panic. Both the JSON and YAML entry points are fuzzed.
func FuzzDecodeConfig(f *testing.F) {
	f.Add([]byte(`{"schema_version":1}`))
	f.Add([]byte("schema_version: 1\n"))
	f.Add([]byte("{"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = DecodeJSON(bytes.NewReader(b))
		_, _ = DecodeYAML(bytes.NewReader(b))
	})
}
