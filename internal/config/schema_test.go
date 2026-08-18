package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestSchemaValidJSON keeps the embedded schema parseable and shaped like a
// JSON Schema, so a syntax slip is caught here rather than by every user's
// editor.
func TestSchemaValidJSON(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(SchemaJSON, &doc); err != nil {
		t.Fatalf("config.schema.json is not valid JSON: %v", err)
	}
	if _, ok := doc["properties"]; !ok {
		t.Fatal("config.schema.json has no top-level properties")
	}
	if _, ok := doc["definitions"]; !ok {
		t.Fatal("config.schema.json has no definitions")
	}
}

// TestSchemaMatchesConfig fails when the JSON Schema drifts from the Config
// structs: every yaml field must appear as a schema property and every schema
// property must map to a yaml field. This makes autocomplete metadata a
// non-optional part of adding or renaming a setting.
func TestSchemaMatchesConfig(t *testing.T) {
	structFields := map[string]bool{}
	collectYAMLFields(reflect.TypeFor[Config](), structFields, map[reflect.Type]bool{})

	var doc any
	if err := json.Unmarshal(SchemaJSON, &doc); err != nil {
		t.Fatalf("config.schema.json: %v", err)
	}
	schemaProps := map[string]bool{}
	collectSchemaProps(doc, schemaProps)

	for name := range structFields {
		if !schemaProps[name] {
			t.Errorf("config field %q is missing from config.schema.json", name)
		}
	}
	for name := range schemaProps {
		if !structFields[name] {
			t.Errorf("schema property %q has no matching yaml field in Config", name)
		}
	}
}

// TestSchemaModelineFormat pins the modeline the yaml-language-server reads, so a
// rename of SchemaFileName can't silently break the sibling-file wiring.
func TestSchemaModelineFormat(t *testing.T) {
	want := "# yaml-language-server: $schema=" + SchemaFileName + "\n"
	if schemaModeline != want {
		t.Errorf("schemaModeline = %q; want %q", schemaModeline, want)
	}
	if !hasSchemaModeline([]byte(schemaModeline)) {
		t.Error("hasSchemaModeline should detect the modeline it emits")
	}
	if hasSchemaModeline([]byte("# just a normal comment\n")) {
		t.Error("hasSchemaModeline should not fire on an unrelated comment")
	}
}

// TestLoadDropsSchemaBesideConfig exercises the real Load path: writing a fresh
// default config must also drop the sibling schema file and stamp the header
// with the modeline, so autocomplete works out of the box.
func TestLoadDropsSchemaBesideConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DirEnv, dir)

	if _, err := Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	cfgPath := filepath.Join(dir, "config.yaml")
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.HasPrefix(cfg, []byte(schemaModeline)) {
		t.Errorf("config header does not start with the schema modeline; got:\n%s", firstLine(cfg))
	}

	schemaPath := filepath.Join(dir, SchemaFileName)
	got, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema beside config: %v", err)
	}
	if !bytes.Equal(got, SchemaJSON) {
		t.Errorf("schema written beside config differs from the embedded SchemaJSON")
	}
}

// TestEnsureAutocompletePrepends covers the existing-config path: a config that
// predates the modeline gains one (non-destructively) and the schema file, and a
// second pass is idempotent — no duplicate modeline.
func TestEnsureAutocompletePrepends(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	const original = "server_url: https://mm.example.com\nreactions: [rocket]\n"
	if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	ensureAutocomplete(p, []byte(original))

	updated, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.HasPrefix(updated, []byte(schemaModeline)) {
		t.Errorf("modeline not prepended; got:\n%s", updated)
	}
	if !bytes.HasSuffix(updated, []byte(original)) {
		t.Errorf("original config content not preserved; got:\n%s", updated)
	}
	if _, err := os.Stat(filepath.Join(dir, SchemaFileName)); err != nil {
		t.Errorf("schema file not written beside config: %v", err)
	}

	// Idempotent: a second pass on the now-stamped file must not add another line.
	ensureAutocomplete(p, updated)
	again, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("re-read config: %v", err)
	}
	if got := bytes.Count(again, []byte("yaml-language-server")); got != 1 {
		t.Errorf("modeline count = %d; want 1 (idempotent)", got)
	}
}

// firstLine returns the first line of b (without the trailing newline), for
// readable test failure messages.
func firstLine(b []byte) []byte {
	line, _, _ := bytes.Cut(b, []byte("\n"))
	return line
}

// collectYAMLFields walks a struct (through pointers, slices, arrays and nested
// structs) collecting the name of every yaml-tagged field, matching how
// gopkg.in/yaml.v3 reads the config. The seen set breaks the RuleMatchConfig.not
// self-reference.
func collectYAMLFields(t reflect.Type, out map[string]bool, seen map[reflect.Type]bool) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || seen[t] {
		return
	}
	seen[t] = true
	for f := range t.Fields() {
		name := f.Tag.Get("yaml")
		if idx := strings.IndexByte(name, ','); idx >= 0 {
			name = name[:idx]
		}
		if name == "" || name == "-" {
			continue
		}
		out[name] = true
		collectYAMLFields(f.Type, out, seen)
	}
}

// collectSchemaProps walks a decoded JSON Schema collecting every key defined
// under a "properties" object, anywhere in the document (top level and each
// definition).
func collectSchemaProps(node any, out map[string]bool) {
	switch v := node.(type) {
	case map[string]any:
		for k, child := range v {
			if k == "properties" {
				if props, ok := child.(map[string]any); ok {
					for name, sub := range props {
						out[name] = true
						collectSchemaProps(sub, out)
					}
					continue
				}
			}
			collectSchemaProps(child, out)
		}
	case []any:
		for _, child := range v {
			collectSchemaProps(child, out)
		}
	}
}
