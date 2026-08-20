package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// SchemaFileName is the JSON Schema written next to config.yaml. A YAML-aware
// editor (see schemaModeline) reads it to offer key/value autocompletion, hover
// documentation, and typo warnings for the config.
const SchemaFileName = "config.schema.json"

// schemaModeline is the first line of the config header. The yaml-language-server
// resolves a relative $schema against the config file's own directory, so
// pointing at the sibling SchemaFileName makes autocomplete work with no
// per-editor configuration.
const schemaModeline = "# yaml-language-server: $schema=" + SchemaFileName + "\n"

// SchemaJSON is the embedded JSON Schema for config.yaml. It is kept in sync
// with the Config structs by TestSchemaMatchesConfig — a new setting can't ship
// without matching schema metadata.
//
//go:embed config.schema.json
var SchemaJSON []byte

// writeSchemaBeside writes SchemaJSON as SchemaFileName in the same directory as
// configPath, but only when the on-disk copy is missing or out of date, so an
// editor always sees the schema that matches this build. Callers treat a failure
// as non-fatal: it only costs autocomplete, never the app.
func writeSchemaBeside(configPath string) error {
	p := filepath.Join(filepath.Dir(configPath), SchemaFileName)
	if existing, err := os.ReadFile(p); err == nil && bytes.Equal(existing, SchemaJSON) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, SchemaJSON, 0o644)
}

// hasSchemaModeline reports whether a config file already declares a schema for
// the yaml-language-server, so ensureAutocomplete doesn't add a second one.
func hasSchemaModeline(configBytes []byte) bool {
	return bytes.Contains(configBytes, []byte("yaml-language-server"))
}

// ensureAutocomplete makes editor autocomplete work for an existing config that
// this build didn't just rewrite: it refreshes the sibling schema file and, when
// the config carries no schema modeline yet, prepends one without otherwise
// touching the user's file (preserving their comments, ordering and formatting).
// Every step is best-effort — a failure only costs autocomplete.
func ensureAutocomplete(configPath string, current []byte) {
	if err := writeSchemaBeside(configPath); err != nil {
		fmt.Fprintf(os.Stderr, "matterbox: could not write config schema next to %s: %v\n", configPath, err)
	}
	if hasSchemaModeline(current) {
		return
	}
	updated := append([]byte(schemaModeline), current...)
	if err := os.WriteFile(configPath, updated, FileMode); err != nil {
		fmt.Fprintf(os.Stderr, "matterbox: could not add schema modeline to %s: %v\n", configPath, err)
	}
}
