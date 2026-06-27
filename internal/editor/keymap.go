package editor

import "charm.land/bubbles/v2/key"

// KeyMap binds editing actions to keys. Field names match the subset of
// textarea.KeyMap that matterbox reconfigured (InsertNewline, WordBackward,
// WordForward), so existing call-site overrides port across unchanged.
type KeyMap struct {
	CharacterBackward       key.Binding
	CharacterForward        key.Binding
	WordBackward            key.Binding
	WordForward             key.Binding
	LineNext                key.Binding
	LinePrevious            key.Binding
	LineStart               key.Binding
	LineEnd                 key.Binding
	InputBegin              key.Binding
	InputEnd                key.Binding
	DeleteCharacterBackward key.Binding
	DeleteCharacterForward  key.Binding
	DeleteWordBackward      key.Binding
	DeleteWordForward       key.Binding
	DeleteAfterCursor       key.Binding
	DeleteBeforeCursor      key.Binding
	InsertNewline           key.Binding
}

// DefaultKeyMap mirrors textarea's default bindings for the actions matterbox
// relies on. (Transpose/case-change ops are intentionally omitted — they were
// never surfaced in the composer.)
func DefaultKeyMap() KeyMap {
	return KeyMap{
		CharacterForward:        key.NewBinding(key.WithKeys("right", "ctrl+f")),
		CharacterBackward:       key.NewBinding(key.WithKeys("left", "ctrl+b")),
		WordForward:             key.NewBinding(key.WithKeys("alt+right", "alt+f")),
		WordBackward:            key.NewBinding(key.WithKeys("alt+left", "alt+b")),
		LineNext:                key.NewBinding(key.WithKeys("down", "ctrl+n")),
		LinePrevious:            key.NewBinding(key.WithKeys("up", "ctrl+p")),
		LineStart:               key.NewBinding(key.WithKeys("home", "ctrl+a")),
		LineEnd:                 key.NewBinding(key.WithKeys("end", "ctrl+e")),
		InputBegin:              key.NewBinding(key.WithKeys("alt+<", "ctrl+home")),
		InputEnd:                key.NewBinding(key.WithKeys("alt+>", "ctrl+end")),
		DeleteCharacterBackward: key.NewBinding(key.WithKeys("backspace", "ctrl+h")),
		DeleteCharacterForward:  key.NewBinding(key.WithKeys("delete", "ctrl+d")),
		DeleteWordBackward:      key.NewBinding(key.WithKeys("alt+backspace", "ctrl+w")),
		DeleteWordForward:       key.NewBinding(key.WithKeys("alt+delete", "alt+d")),
		DeleteAfterCursor:       key.NewBinding(key.WithKeys("ctrl+k")),
		DeleteBeforeCursor:      key.NewBinding(key.WithKeys("ctrl+u")),
		InsertNewline:           key.NewBinding(key.WithKeys("enter", "ctrl+m")),
	}
}
