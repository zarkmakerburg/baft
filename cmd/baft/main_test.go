package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestVersionJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	var v versionInfo
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Name != "baft" || v.Version != version {
		t.Fatalf("unexpected version: %#v", v)
	}
}
