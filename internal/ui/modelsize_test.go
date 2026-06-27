package ui

import (
	"reflect"
	"sort"
	"testing"
	"unsafe"
)

// ptrTo returns a pointer to a copy of v. Test helper for building Models with
// the cold sub-state fields that are pointers (see the boxing in model.go).
func ptrTo[T any](v T) *T { return &v }

// modelSizeCeiling guards against re-bloating ui.Model. The struct is value-
// copied on every Update (per keystroke) and View (per render), so its size is
// a direct per-event memcpy cost. Cold modal sub-state (switcher, reaction
// picker, history + cheatsheet viewports) is held behind pointers to keep this
// down; fields touched in the unconditional layout/render passes stay inline
// (boxing them would nil-panic literal-built test Models). If you add a fat
// field, box it if it's safe or raise this ceiling deliberately.
const modelSizeCeiling = 108000

func TestModelSizeCeiling(t *testing.T) {
	got := unsafe.Sizeof(Model{})
	if got > modelSizeCeiling {
		// Dump the biggest fields so the regression is easy to attribute.
		ty := reflect.TypeOf(Model{})
		type fs struct {
			name, typ string
			size      uintptr
		}
		fields := make([]fs, 0, ty.NumField())
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			fields = append(fields, fs{f.Name, f.Type.String(), f.Type.Size()})
		}
		sort.SliceStable(fields, func(i, j int) bool { return fields[i].size > fields[j].size })
		for i := 0; i < 12 && i < len(fields); i++ {
			t.Logf("%7d  %-20s %s", fields[i].size, fields[i].name, fields[i].typ)
		}
		t.Fatalf("unsafe.Sizeof(Model{}) = %d, want <= %d", got, modelSizeCeiling)
	}
}
