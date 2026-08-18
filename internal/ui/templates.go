package ui

import (
	"encoding/json"
	"os"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"matterbox/internal/config"
)

// templatesFile is the on-disk shape of ~/.config/matterbox/templates.json:
// composer snippets by (lower-cased) name, inserted with /tmpl <name> or from
// the Templates picker.
type templatesFile struct {
	Templates map[string]string `json:"templates"`
}

func templatesPath() (string, error) {
	return config.File("templates.json")
}

// loadTemplates reads the saved templates. A missing or unparsable file
// degrades to an empty map — templates are a nicety, not load-bearing.
func loadTemplates() map[string]string {
	p, err := templatesPath()
	if err != nil {
		return map[string]string{}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return map[string]string{}
	}
	var f templatesFile
	if err := json.Unmarshal(b, &f); err != nil || f.Templates == nil {
		return map[string]string{}
	}
	return f.Templates
}

// writeTemplates persists the map atomically, like the stats files.
func writeTemplates(ts map[string]string) error {
	p, err := templatesPath()
	if err != nil {
		return err
	}
	return writeJSONAtomic(p, templatesFile{Templates: ts})
}

func normalizeTemplateName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ensureTemplates loads the file on first use; the map lives on the Model
// afterwards (shared by reference across the value copies).
func (m *Model) ensureTemplates() {
	if m.templates == nil {
		m.templates = loadTemplates()
	}
}

func (m *Model) persistTemplates() tea.Cmd {
	snapshot := make(map[string]string, len(m.templates))
	for k, v := range m.templates {
		snapshot[k] = v
	}
	return func() tea.Msg {
		_ = writeTemplates(snapshot)
		return nil
	}
}

func (m *Model) templateNames() []string {
	m.ensureTemplates()
	out := make([]string, 0, len(m.templates))
	for name := range m.templates {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// templatePickerState is the Templates sheet: enter inserts the selected
// template into the composer, d deletes it.
type templatePickerState struct {
	active bool
	names  []string
	idx    int
}

// templateCommands returns the Templates sheet and "save composer as…"
// commands, plus whether they apply: only while a channel tab shows the
// composer they insert into / read from. On the Feed / Search / SQL tabs
// there is no composer on screen, and inserting into the hidden one would
// silently edit the open channel's draft.
func (m *Model) templateCommands() ([]switcherCommand, bool) {
	if !m.composerShown() {
		return nil, false
	}
	return []switcherCommand{
		{
			name: "Templates",
			desc: "your composer templates (enter inserts, d deletes); also /tmpl",
			run:  runListTemplates,
		},
		{
			name:           "Templates: save composer as…",
			desc:           "keep the current composer text as a template under this name",
			argPrompt:      "template: ",
			argPlaceholder: "standup",
			run:            runSaveTemplate,
		},
	}, true
}

func runListTemplates(m *Model, _ string) tea.Cmd {
	m.openTemplatePicker()
	return nil
}

func runSaveTemplate(m *Model, arg string) tea.Cmd {
	return m.saveTemplate(arg)
}

func (m *Model) openTemplatePicker() {
	m.templatePicker = templatePickerState{active: true, names: m.templateNames()}
}

func (m *Model) closeTemplatePicker() {
	m.templatePicker = templatePickerState{}
}

// handleTemplatePickerKey owns every keystroke while the picker is open:
// esc/q close, ↑/↓ move, enter inserts, d deletes the selected template.
func (m Model) handleTemplatePickerKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.closeTemplatePicker()
		return m, nil
	}
	if key.Matches(msg, m.keys.SheetRemove) {
		if m.templatePicker.idx < 0 || m.templatePicker.idx >= len(m.templatePicker.names) {
			return m, nil
		}
		cmd := m.deleteTemplate(m.templatePicker.names[m.templatePicker.idx])
		m.templatePicker.names = m.templateNames()
		if m.templatePicker.idx >= len(m.templatePicker.names) {
			m.templatePicker.idx = len(m.templatePicker.names) - 1
		}
		if m.templatePicker.idx < 0 {
			m.templatePicker.idx = 0
		}
		return m, cmd
	}
	if m.listNav(msg, &m.templatePicker.idx, len(m.templatePicker.names)) {
		return m, nil
	}
	if key.Matches(msg, m.keys.OpenChannel) && m.templatePicker.idx < len(m.templatePicker.names) {
		name := m.templatePicker.names[m.templatePicker.idx]
		m.closeTemplatePicker()
		return m, m.insertTemplate(name)
	}
	return m, nil
}

func (m *Model) renderTemplatePicker() string {
	if !m.templatePicker.active {
		return ""
	}
	names := m.templatePicker.names
	row := func(i int) string {
		preview := strings.Join(strings.Fields(m.templates[names[i]]), " ")
		if preview == "" {
			preview = "(empty)"
		}
		return names[i] + "  " + preview
	}
	empty := "No templates saved yet.\n\nType a message, then run \"> Templates: save composer as…\" to keep it as a template."
	return m.renderListModal("Templates", helpKey(m.keys.OpenChannel)+" inserts · "+helpKey(m.keys.SheetRemove)+" deletes · esc closes", empty, len(names), m.templatePicker.idx, row)
}

// saveTemplate stores the composer's current text under name (lower-cased),
// replacing an existing template of that name.
func (m *Model) saveTemplate(name string) tea.Cmd {
	name = normalizeTemplateName(name)
	if name == "" {
		m.status = "template: enter a name"
		return nil
	}
	body := m.input.Value()
	if strings.TrimSpace(body) == "" {
		m.status = "template: composer is empty"
		return nil
	}
	m.ensureTemplates()
	m.templates[name] = body
	m.status = "saved template " + name
	return m.persistTemplates()
}

func (m *Model) deleteTemplate(name string) tea.Cmd {
	name = normalizeTemplateName(name)
	m.ensureTemplates()
	if _, ok := m.templates[name]; !ok {
		m.status = "template not found: " + name
		return nil
	}
	delete(m.templates, name)
	m.status = "deleted template " + name
	return m.persistTemplates()
}

// insertTemplate puts the named template into the composer at the cursor
// (an undo checkpoint first). An empty name opens the picker instead — that's
// what a bare /tmpl does.
func (m *Model) insertTemplate(name string) tea.Cmd {
	name = normalizeTemplateName(name)
	if name == "" {
		m.openTemplatePicker()
		return nil
	}
	m.ensureTemplates()
	body, ok := m.templates[name]
	if !ok {
		m.status = "template not found: " + name
		return nil
	}
	m.history.checkpoint(m.composerContextKey(), m.input.Value())
	m.input.InsertString(body)
	m.syncInputHeight()
	m.focusComposerIfShown()
	m.status = "inserted template " + name
	return m.scheduleGrammarCheck()
}

// composerShown reports whether a channel tab — and so the composer — is on
// screen; the Feed / Search / SQL tabs own the body without one.
func (m *Model) composerShown() bool {
	return !m.onSearchTab() && !m.onFeedTab() && !m.onSQLTab()
}

// focusComposerIfShown moves focus to the composer after a pick landed text
// in it, so the next keystroke continues the message. The Update wrapper's
// syncComposerFocus / syncSelBarFocus do the rest.
func (m *Model) focusComposerIfShown() {
	if m.composerShown() {
		m.focus = focusInput
	}
}
