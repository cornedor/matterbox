// Package welcome is the first-run setup wizard: a vaporwave intro animation
// (the internal/vapor renderer) that, after the ~6s title fly-in, hands over to
// a small form — server URL, authentication, and a screen of advanced settings —
// drawn on top of the still-running background. It is launched by the
// `matterbox welcome` subcommand and writes config.yaml + the saved token.
package welcome

import (
	"context"
	_ "embed"
	"time"

	tea "charm.land/bubbletea/v2"

	"matterbox/internal/config"
	"matterbox/internal/mm"
	"matterbox/internal/vapor"
)

//go:embed intro.json
var introJSON []byte

// Background animation rates. The intro fly-in wants to be smooth, but once the
// form is up the scene only drifts slowly (terrain at speed 0.2, twinkling
// stars), so the tick drops to a low rate to spare the CPU — typing stays
// instant regardless, since bubbletea re-renders the View on every keypress, not
// just on the animation tick.
const (
	introFrameRate  = 30
	wizardFrameRate = 12
)

// introSecs matches the embedded intro.json duration: the title fly-in and the
// sunrise both settle at t=6s, after which the wizard appears over the scene
// (which keeps drifting, since the animation holds its final keyframes).
const introSecs = 6.0

type phase int

const (
	phaseIntro phase = iota
	phaseWizard
	phaseDone
)

// wizard steps, in order.
const (
	stepServer = iota
	stepAuth
	stepAdvanced
)

// Model is the welcome wizard. Pointer receivers throughout: the model is small
// (two pointers plus a handful of fields) and mutating in place keeps the step
// logic readable.
type Model struct {
	rend *vapor.Renderer
	cfg  *config.Config

	start time.Time // animation origin; set on the first frame
	now   time.Time // most recent frame time
	t     float64   // animation seconds (now - start)

	width, height int

	phase phase
	step  int

	server textField
	token  textField
	adv    advanced

	serverMsg    string // validation hint under the server field
	authMsg      string // status/error under the token field
	authErr      bool   // authMsg is an error (vs neutral status)
	authOK       bool   // a token validated this session
	authUser     string // username from the validated token
	validating   bool   // an auth check is in flight
	pendingToken string // token awaiting validation, saved on success

	// Background frame cache. scene holds the pristine rendered scene for sceneT;
	// frame is a per-View copy the overlay draws onto. The scene is re-rendered
	// only when the animation time advances (see sceneFrame), so keystrokes — which
	// also re-run View but don't move m.t — skip the expensive scene render.
	scene      [][]cell
	frame      [][]cell
	sceneT     float64
	sceneValid bool
}

// advanced holds the one-screen advanced settings and the focused row.
type advanced struct {
	focus      int
	markRead   int  // mark_read_delay_seconds
	sqlTab     bool // sql_tab
	mouse      bool // mouse
	animations bool // animations.* (custom_emoji + image_preview)
	ctrlArrow  bool // keybindings.nav_modifier == "ctrl" (vs "none")
}

const advFieldCount = 5

// New builds the wizard over an already-loaded config, seeding the fields from
// whatever is already configured so re-running `welcome` edits rather than
// resets. The scene parameters reproduce the reference vaporascii invocation
// (glyph renderer, slow drive, low warm-coloured mountains, the "Matterbox"
// title flying in via intro.json).
func New(cfg *config.Config) *Model {
	stops, _ := vapor.ParseHexStops("#ffd21e,#ff9b2f,#ff3d7f,#ec1e63")
	anim, _ := vapor.LoadAnimationJSON(introJSON)
	rend := vapor.New(vapor.Options{
		Mode:         "glyph",
		Coverage:     "octant",
		Speed:        0.5,
		Height:       0.7,
		Valley:       1,
		ValleyHeight: 0.3,
		SunY:         1,
		SunStops:     stops,
		Text: &vapor.TextOpts{
			Text: "Matterbox", X: 0, Y: 4, Z: 22,
			Scale: 1.5, Depth: 1, RotX: 25,
		},
		Anim: anim,
	})

	m := &Model{rend: rend, cfg: cfg, phase: phaseIntro, step: stepServer}
	if cfg.ServerURL != "" && cfg.ServerURL != config.PlaceholderServerURL {
		m.server.setValue(cfg.ServerURL)
	}
	m.adv = advanced{
		markRead:   derefInt(cfg.MarkReadDelaySeconds, 5),
		sqlTab:     derefBool(cfg.SQLTab, false),
		mouse:      derefBool(cfg.Mouse, true),
		animations: derefBool(cfg.Animations.CustomEmoji, true),
		ctrlArrow:  navModEnabled(cfg.Keybindings.NavModifier),
	}
	return m
}

// frameMsg carries the wall-clock time of an animation tick.
type frameMsg time.Time

func tickAt(fps int) tea.Cmd {
	return tea.Tick(time.Second/time.Duration(fps), func(t time.Time) tea.Msg {
		return frameMsg(t)
	})
}

// frameRate is the animation tick rate for the current phase: smooth during the
// intro fly-in, low once the wizard/done panel is up over a slowly drifting scene.
func (m *Model) frameRate() int {
	if m.phase == phaseIntro {
		return introFrameRate
	}
	return wizardFrameRate
}

// authResultMsg reports the outcome of validating a token against the server.
type authResultMsg struct {
	user string
	err  error
}

// validateCmd checks a token against the server in the background, returning the
// authenticated username on success. Mirrors `matterbox login`'s saveAndVerify:
// a token is only saved once it authenticates.
func validateCmd(server, token string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		u, err := mm.New(server, token).Me(ctx)
		if err != nil {
			return authResultMsg{err: err}
		}
		return authResultMsg{user: u.Username}
	}
}

func (m *Model) Init() tea.Cmd { return tickAt(m.frameRate()) }

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case frameMsg:
		now := time.Time(msg)
		if m.start.IsZero() {
			m.start = now
		}
		m.now = now
		m.t = now.Sub(m.start).Seconds()
		if m.phase == phaseIntro && m.t >= introSecs {
			m.phase = phaseWizard
		}
		// frameRate() reflects the just-updated phase, so the tick rate drops as
		// soon as the wizard opens.
		return m, tickAt(m.frameRate())

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.width > 0 && m.height > 0 {
			m.rend.Resize(m.width, m.height)
			m.sceneValid = false // re-render the scene at the new size
		}
		return m, nil

	case tea.PasteMsg:
		if f := m.activeField(); f != nil {
			f.insert(msg.Content)
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case authResultMsg:
		return m.handleAuthResult(msg)
	}
	return m, nil
}

func (m *Model) View() tea.View {
	if m.width == 0 || m.height == 0 || m.rend.Cols() == 0 {
		v := tea.NewView("")
		v.AltScreen = true
		return v
	}
	grid := m.sceneFrame()
	switch m.phase {
	case phaseIntro:
		m.drawIntroHint(grid)
	case phaseWizard:
		m.drawWizard(grid)
	case phaseDone:
		m.drawDone(grid)
	}
	v := tea.NewView(vapor.Serialize(grid))
	v.AltScreen = true
	return v
}

// sceneFrame returns a writable copy of the animated background for the current
// frame time. The scene is rendered only when the animation has advanced since
// the last frame: bubbletea re-runs View on every keystroke, but m.t moves only
// on a frameMsg, so without this guard each keypress would recompute the whole
// vaporwave scene — its single most expensive operation. Typing now just
// re-composites the wizard overlay onto the cached frame.
func (m *Model) sceneFrame() [][]cell {
	if !m.sceneValid || m.sceneT != m.t {
		m.scene = copyGrid(m.scene, m.rend.Render(m.t))
		m.sceneT = m.t
		m.sceneValid = true
	}
	m.frame = copyGrid(m.frame, m.scene)
	return m.frame
}

// copyGrid copies src into dst, reshaping dst to match src's dimensions (so it
// adapts across a resize), and returns the populated dst.
func copyGrid(dst, src [][]cell) [][]cell {
	if len(dst) != len(src) {
		dst = make([][]cell, len(src))
	}
	for y := range src {
		if len(dst[y]) != len(src[y]) {
			dst[y] = make([]cell, len(src[y]))
		}
		copy(dst[y], src[y])
	}
	return dst
}

// activeField returns the text field accepting input for the current step, or
// nil when the step has no text entry (e.g. the advanced screen).
func (m *Model) activeField() *textField {
	if m.phase != phaseWizard {
		return nil
	}
	switch m.step {
	case stepServer:
		return &m.server
	case stepAuth:
		return &m.token
	}
	return nil
}

func derefInt(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func derefBool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}
