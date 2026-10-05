package config

import (
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// mergeConfigYAML returns the existing config.yaml with cfg's values written
// into it, so a save keeps the user's comments, key order, and keys this
// version doesn't know. ok is false when existing isn't a YAML mapping; the
// caller then writes the file fresh.
func mergeConfigYAML(existing []byte, cfg *Config) (out []byte, ok bool, err error) {
	var doc yaml.Node
	if yaml.Unmarshal(existing, &doc) != nil || doc.Kind != yaml.DocumentNode ||
		len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, nil
	}
	var src yaml.Node
	if err := src.Encode(cfg); err != nil {
		return nil, false, err
	}
	mergeMapping(doc.Content[0], &src, reflect.TypeOf(*cfg))
	out, err = yaml.Marshal(&doc)
	return out, err == nil, err
}

// mergeMapping writes src's entries into dst, where t is the Go type both
// decode to. A struct field or map entry that dst has and src lacks is
// dropped (zeroed, or a migrated legacy key); a key t doesn't know is kept.
func mergeMapping(dst, src *yaml.Node, t reflect.Type) {
	inSrc := map[string]bool{}
	for i := 0; i+1 < len(src.Content); i += 2 {
		k, v := src.Content[i], src.Content[i+1]
		inSrc[k.Value] = true
		j := mappingIndex(dst, k.Value)
		if j < 0 {
			dst.Content = append(dst.Content, k, v)
			continue
		}
		ct, _ := childType(t, k.Value)
		mergeValue(dst.Content[j+1], v, ct)
	}
	kept := dst.Content[:0]
	for i := 0; i+1 < len(dst.Content); i += 2 {
		k := dst.Content[i]
		if _, known := childType(t, k.Value); known && !inSrc[k.Value] {
			continue
		}
		kept = append(kept, k, dst.Content[i+1])
	}
	dst.Content = kept
}

// mergeValue writes src into dst, where t (nil when unknown) is the Go type
// both decode to. Mappings merge key by key and same-length lists of
// mappings item by item, so comments inside survive; anything else is
// replaced unless unchanged, keeping dst's own comments.
func mergeValue(dst, src *yaml.Node, t reflect.Type) {
	switch {
	case t != nil && dst.Kind == yaml.MappingNode && src.Kind == yaml.MappingNode &&
		(t.Kind() == reflect.Struct || t.Kind() == reflect.Map):
		mergeMapping(dst, src, t)
		return
	case t != nil && t.Kind() == reflect.Slice && dst.Kind == yaml.SequenceNode && src.Kind == yaml.SequenceNode &&
		len(dst.Content) == len(src.Content):
		for i := range dst.Content {
			mergeValue(dst.Content[i], src.Content[i], deref(t.Elem()))
		}
		return
	}
	if sameValue(dst, src) {
		return
	}
	head, line, foot := dst.HeadComment, dst.LineComment, dst.FootComment
	*dst = *src
	dst.HeadComment, dst.LineComment, dst.FootComment = head, line, foot
}

func mappingIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// childType is the Go type of key inside t, and whether t knows the key at
// all: every key of a map, the yaml-tagged fields of a struct.
func childType(t reflect.Type, key string) (reflect.Type, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Map:
		return deref(t.Elem()), true
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, opts, _ := strings.Cut(f.Tag.Get("yaml"), ",")
			if strings.Contains(opts, "inline") {
				if ct, ok := childType(f.Type, key); ok {
					return ct, true
				}
				continue
			}
			if name == "" {
				name = strings.ToLower(f.Name)
			}
			if name == key {
				return deref(f.Type), true
			}
		}
	}
	return nil, false
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// sameValue reports whether two nodes hold the same data, ignoring style and
// comments, so an unchanged list or block keeps the user's formatting.
func sameValue(a, b *yaml.Node) bool {
	for a.Kind == yaml.AliasNode && a.Alias != nil {
		a = a.Alias
	}
	for b.Kind == yaml.AliasNode && b.Alias != nil {
		b = b.Alias
	}
	if a.Kind != b.Kind || a.Value != b.Value || len(a.Content) != len(b.Content) {
		return false
	}
	for i := range a.Content {
		if !sameValue(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}
