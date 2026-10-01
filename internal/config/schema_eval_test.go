package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"regexp"
	"testing"
	"unicode/utf8"
)

// schemaErrors evaluates the JSON Schema subset used by configs/schema-v1.json.
// An unimplemented keyword is reported as an error, so a schema edit cannot
// silently escape these tests.
func schemaErrors(s map[string]any, v any, path string) []string {
	var errs []string
	fail := func(format string, a ...any) { errs = append(errs, path+": "+fmt.Sprintf(format, a...)) }
	obj, isObj := v.(map[string]any)
	arr, isArr := v.([]any)
	str, isStr := v.(string)
	num, isNum := v.(float64)
	sub := func(k string) map[string]any { m, _ := s[k].(map[string]any); return m }
	list := func(k string) []any { l, _ := s[k].([]any); return l }

	for key, raw := range s {
		switch key {
		case "$schema", "$id", "title", "then":
		case "type":
			ok := map[string]bool{
				"object": isObj, "array": isArr, "string": isStr, "number": isNum,
				"integer": isNum && num == math.Trunc(num), "boolean": reflect.TypeOf(v) == reflect.TypeOf(true),
			}[raw.(string)]
			if !ok { fail("is not of type %v", raw) }
		case "const":
			if !reflect.DeepEqual(raw, v) { fail("must be %v", raw) }
		case "enum":
			found := false
			for _, e := range list(key) { found = found || reflect.DeepEqual(e, v) }
			if !found { fail("is not one of %v", raw) }
		case "required":
			for _, r := range list(key) {
				if _, ok := obj[r.(string)]; isObj && !ok { fail("missing %v", r) }
			}
		case "properties":
			for name, ps := range sub(key) {
				if pv, ok := obj[name]; ok { errs = append(errs, schemaErrors(ps.(map[string]any), pv, path+"."+name)...) }
			}
		case "additionalProperties":
			props := sub("properties")
			for name, pv := range obj {
				if _, known := props[name]; known { continue }
				if as, ok := raw.(map[string]any); ok {
					errs = append(errs, schemaErrors(as, pv, path+"."+name)...)
				} else if raw == false {
					fail("unknown property %s", name)
				}
			}
		case "minProperties":
			if isObj && float64(len(obj)) < raw.(float64) { fail("too few properties") }
		case "items":
			for i, e := range arr { errs = append(errs, schemaErrors(sub(key), e, fmt.Sprintf("%s[%d]", path, i))...) }
		case "minItems":
			if isArr && float64(len(arr)) < raw.(float64) { fail("too few items") }
		case "maxItems":
			if isArr && float64(len(arr)) > raw.(float64) { fail("too many items") }
		case "uniqueItems":
			seen := map[any]bool{}
			for _, e := range arr {
				if seen[e] { fail("duplicate item %v", e) }
				seen[e] = true
			}
		case "minLength":
			if isStr && float64(utf8.RuneCountInString(str)) < raw.(float64) { fail("too short") }
		case "maxLength":
			if isStr && float64(utf8.RuneCountInString(str)) > raw.(float64) { fail("too long") }
		case "pattern":
			if isStr && !regexp.MustCompile(raw.(string)).MatchString(str) { fail("does not match %v", raw) }
		case "minimum":
			if isNum && num < raw.(float64) { fail("below minimum %v", raw) }
		case "maximum":
			if isNum && num > raw.(float64) { fail("above maximum %v", raw) }
		case "allOf":
			for _, a := range list(key) { errs = append(errs, schemaErrors(a.(map[string]any), v, path)...) }
		case "oneOf":
			matches := 0
			for _, a := range list(key) {
				if len(schemaErrors(a.(map[string]any), v, path)) == 0 { matches++ }
			}
			if matches != 1 { fail("matches %d of the oneOf branches", matches) }
		case "not":
			if len(schemaErrors(sub(key), v, path)) == 0 { fail("matches a forbidden shape") }
		case "if":
			if len(schemaErrors(sub(key), v, path)) == 0 { errs = append(errs, schemaErrors(sub("then"), v, path)...) }
		default:
			fail("schema keyword %q is not implemented by this test", key)
		}
	}
	return errs
}

func loadExampleDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil { t.Fatal(err) }
	j, err := yamlDocumentJSON(b)
	if err != nil { t.Fatal(err) }
	var doc map[string]any
	if err := json.Unmarshal(j, &doc); err != nil { t.Fatal(err) }
	return doc
}

// The schema cannot express every rule in Validate, but for the cases below
// both must give the same verdict; a disagreement means one of them drifted.
func TestSchemaAndValidateAgree(t *testing.T) {
	b, err := os.ReadFile("../../configs/schema-v1.json")
	if err != nil { t.Fatal(err) }
	var schema map[string]any
	if err := json.Unmarshal(b, &schema); err != nil { t.Fatal(err) }

	key := func(seed byte) string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{seed}, 32)) }
	const ir2 = "urn:baft:node:ir-02"
	type doc = map[string]any
	cases := []struct {
		name   string
		base   string
		mutate func(doc)
		valid  bool
	}{
		{"example EX", "ex", func(doc) {}, true},
		{"example IR", "ir", func(doc) {}, true},
		{"recovery enabled", "ex", func(d doc) { d["recovery"] = doc{"enabled": true, "retention_seconds": 30.0, "mode": "same_process"} }, true},
		{"recovery enabled without retention", "ex", func(d doc) { d["recovery"] = doc{"enabled": true, "retention_seconds": 0.0} }, false},
		{"recovery durable", "ex", func(d doc) { d["recovery"] = doc{"enabled": true, "retention_seconds": 30.0, "durable": true} }, false},
		{"recovery unknown mode", "ex", func(d doc) { d["recovery"] = doc{"enabled": true, "retention_seconds": 30.0, "mode": "durable"} }, false},
		{"telemetry disabled", "ir", func(d doc) { d["telemetry"] = doc{"enabled": false} }, true},
		{"telemetry enabled", "ir", func(d doc) { d["telemetry"] = doc{"enabled": true, "bcc_url": "https://bcc.example"} }, true},
		{"telemetry enabled without bcc_url", "ir", func(d doc) { d["telemetry"] = doc{"enabled": true} }, false},
		{"telemetry interval out of range", "ir", func(d doc) { d["telemetry"] = doc{"enabled": true, "bcc_url": "https://bcc.example", "interval_seconds": 7200.0} }, false},
		{"telemetry unknown key", "ir", func(d doc) { d["telemetry"] = doc{"enabled": false, "token": "x"} }, false},
		{"revocation on listener", "ex", func(d doc) { d["revocation"] = doc{"file": "/etc/baft/revoked.yaml"} }, true},
		{"revocation relative path", "ex", func(d doc) { d["revocation"] = doc{"file": "revoked.yaml"} }, false},
		{"revocation on dialer", "ir", func(d doc) { d["revocation"] = doc{"file": "/etc/baft/revoked.yaml"} }, false},
		{"noise pinned listener", "ex", func(d doc) { d["noise"] = doc{"key_file": "/etc/baft/noise-key.json", "peer_public_key": key(1)} }, true},
		{"noise pinned listener with two identities", "ex", func(d doc) {
			d["noise"] = doc{"key_file": "/etc/baft/noise-key.json", "peer_public_key": key(1)}
			d["server"].(doc)["allowed_peer_identities"] = []any{"urn:baft:node:ir-01", ir2}
		}, false},
		{"noise multi-peer listener", "ex", func(d doc) {
			d["noise"] = doc{"key_file": "/etc/baft/noise-key.json", "allowed_peer_public_keys": doc{"urn:baft:node:ir-01": key(1), ir2: key(2)}}
			d["server"].(doc)["allowed_peer_identities"] = []any{"urn:baft:node:ir-01", ir2}
		}, true},
		{"noise listener with both key forms", "ex", func(d doc) {
			d["noise"] = doc{"key_file": "/etc/baft/noise-key.json", "peer_public_key": key(1), "allowed_peer_public_keys": doc{"urn:baft:node:ir-01": key(1)}}
		}, false},
		{"noise dialer", "ir", func(d doc) { d["noise"] = doc{"key_file": "/etc/baft/noise-key.json", "peer_public_key": key(1)} }, true},
		{"noise dialer without client certificate", "ir", func(d doc) {
			d["noise"] = doc{"key_file": "/etc/baft/noise-key.json", "peer_public_key": key(1)}
			delete(d["tls"].(doc), "cert_file")
			delete(d["tls"].(doc), "key_file")
		}, true},
		{"noise dialer with cert but no key", "ir", func(d doc) {
			d["noise"] = doc{"key_file": "/etc/baft/noise-key.json", "peer_public_key": key(1)}
			delete(d["tls"].(doc), "key_file")
		}, false},
		{"mTLS dialer without client certificate", "ir", func(d doc) {
			delete(d["tls"].(doc), "cert_file")
			delete(d["tls"].(doc), "key_file")
		}, false},
		{"noise listener without server certificate", "ex", func(d doc) {
			d["noise"] = doc{"key_file": "/etc/baft/noise-key.json", "peer_public_key": key(1)}
			delete(d["tls"].(doc), "cert_file")
			delete(d["tls"].(doc), "key_file")
		}, false},
		{"noise dialer with allowlist", "ir", func(d doc) {
			d["noise"] = doc{"key_file": "/etc/baft/noise-key.json", "allowed_peer_public_keys": doc{"urn:baft:node:ex-01": key(1)}}
		}, false},
	}
	for _, tc := range cases {
		d := loadExampleDocument(t, "../../configs/example-"+tc.base+".yaml")
		tc.mutate(d)
		j, err := json.Marshal(d)
		if err != nil { t.Fatal(err) }
		var generic any
		if err := json.Unmarshal(j, &generic); err != nil { t.Fatal(err) }

		schemaErrs := schemaErrors(schema, generic, "config")
		_, goErr := DecodeJSON(bytes.NewReader(j))
		if (len(schemaErrs) == 0) != tc.valid { t.Errorf("%s: schema valid=%v, want %v (%v)", tc.name, len(schemaErrs) == 0, tc.valid, schemaErrs) }
		if (goErr == nil) != tc.valid { t.Errorf("%s: Validate valid=%v, want %v (%v)", tc.name, goErr == nil, tc.valid, goErr) }
	}
}
