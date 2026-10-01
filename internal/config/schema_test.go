package config

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// configs/schema-v1.json is maintained by hand next to the Go structs. This
// guards the two against drifting apart: the schema once lacked telemetry and
// the recovery fields entirely.
func TestSchemaPropertiesMatchConfigFields(t *testing.T) {
	b, err := os.ReadFile("../../configs/schema-v1.json")
	if err != nil { t.Fatal(err) }
	var schema map[string]any
	if err := json.Unmarshal(b, &schema); err != nil { t.Fatal(err) }
	compareSchemaToType(t, "config", schema, reflect.TypeOf(Config{}))
}

func compareSchemaToType(t *testing.T, path string, schema map[string]any, typ reflect.Type) {
	t.Helper()
	for typ.Kind() == reflect.Pointer { typ = typ.Elem() }
	if typ.Kind() == reflect.Slice {
		items, _ := schema["items"].(map[string]any)
		if items == nil { t.Errorf("%s: schema array has no items", path); return }
		compareSchemaToType(t, path+"[]", items, typ.Elem())
		return
	}
	if typ.Kind() != reflect.Struct { return }

	props, _ := schema["properties"].(map[string]any)
	fields := map[string]reflect.Type{}
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" { fields[name] = typ.Field(i).Type }
	}
	for name, ft := range fields {
		sub, ok := props[name].(map[string]any)
		if !ok { t.Errorf("%s.%s: config field is missing from the schema", path, name); continue }
		compareSchemaToType(t, path+"."+name, sub, ft)
	}
	for name := range props {
		if _, ok := fields[name]; !ok { t.Errorf("%s.%s: schema property has no config field", path, name) }
	}
}
