package welcome

// textField is a minimal single-line rune buffer with a cursor. The wizard
// manages input by hand (rather than reusing bubbles/textinput) because the
// composited frame is a cell grid, not an ANSI string — owning the buffer lets
// the field be drawn directly into cells with a block cursor, no escape parsing.
type textField struct {
	runes  []rune
	cursor int
}

func (f *textField) value() string { return string(f.runes) }

func (f *textField) setValue(s string) {
	f.runes = []rune(s)
	f.cursor = len(f.runes)
}

// insert adds text at the cursor (used for both typed runes and pastes).
func (f *textField) insert(s string) {
	r := []rune(s)
	if len(r) == 0 {
		return
	}
	out := make([]rune, 0, len(f.runes)+len(r))
	out = append(out, f.runes[:f.cursor]...)
	out = append(out, r...)
	out = append(out, f.runes[f.cursor:]...)
	f.runes = out
	f.cursor += len(r)
}

func (f *textField) backspace() {
	if f.cursor == 0 {
		return
	}
	f.runes = append(f.runes[:f.cursor-1], f.runes[f.cursor:]...)
	f.cursor--
}

func (f *textField) deleteForward() {
	if f.cursor >= len(f.runes) {
		return
	}
	f.runes = append(f.runes[:f.cursor], f.runes[f.cursor+1:]...)
}

func (f *textField) left() {
	if f.cursor > 0 {
		f.cursor--
	}
}

func (f *textField) right() {
	if f.cursor < len(f.runes) {
		f.cursor++
	}
}

func (f *textField) home() { f.cursor = 0 }
func (f *textField) end()  { f.cursor = len(f.runes) }
