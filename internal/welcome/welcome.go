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
	"matterbox/internal/mmauth"
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

// Demo mode stretches the intro. The choreographed animation (sun rise + title
// fly-in) is shifted demoIntroDelaySecs later — its keyframes are pushed back in
// New, while the terrain keeps drifting from t=0 underneath it — and the wizard
// waits until demoWizardDelaySecs past introSecs before replacing the scene, so
// the show plays longer before the form interrupts it.
const (
	demoIntroDelaySecs  = 3.0
	demoWizardDelaySecs = 8.0
)

// Demo mountains pulse with the soundtrack: the playback level (musicLevel)
// drives the terrain peak height. The range is skewed downward — quiet passages
// collapse the peaks toward flat (demoMountainBase), while the loudest beats only
// bring them back to about normal (demoMountainMax), so the motion reads as the
// mountains dropping out rather than towering up. demoMountainGain sets how hard
// the level lifts them; a frame-rate envelope rises fast and falls slowly so they
// snap up on the beat and sink back down.
const (
	demoMountainBase = 0.2
	demoMountainGain = 8
	demoMountainMax  = 1.2
	demoPulseAttack  = 0.45
	demoPulseRelease = 0.12
)

type phase int

const (
	phaseIntro phase = iota
	phaseWizard
	phaseDone
	// phaseHidden: demo only. The closing panel is dismissed (settings already
	// saved) but the program keeps running so the animated scene + soundtrack
	// play on — reached by pressing space on the done screen, exited with ctrl+c.
	phaseHidden
)

// wizard steps, in order.
const (
	stepServer = iota
	stepAuth
	stepAdvanced
)

// Focusable controls on the auth step, in tab order. Username/password come
// first — they're the primary path, so the username field is focused when the
// step opens — with the GitLab SSO button and the token/mmauth:// paste field
// as alternatives below. The two-factor field is last and stays unreachable
// until the server asks for a code (authControls skips it), so it doesn't
// clutter the tab order in the common case.
const (
	authFocusUser = iota
	authFocusPassword
	authFocusSSO
	authFocusToken
	authFocusMFA
	authFieldCount
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

	demo  bool    // `--demo`: animate the title (per-letter bob + flips) and hold full fps
	pulse float64 // demo: smoothed soundtrack level (0..1) driving the mountain height

	phase phase
	step  int

	authFocus int // focused control on the auth step (authFocus* consts)

	server   textField
	user     textField
	password textField
	token    textField
	mfa      textField
	adv      advanced

	// themeNames is the sorted list of chroma style names the code-theme cycler
	// steps through (adv.codeThemeIdx indexes into it). Captured once in New so
	// the every-keystroke View doesn't re-enumerate the registry.
	themeNames []string

	serverMsg    string // validation hint under the server field
	authMsg      string // status/error under the auth controls
	authErr      bool   // authMsg is an error (vs neutral status)
	authOK       bool   // a sign-in succeeded this session
	authUser     string // username from the successful sign-in
	validating   bool   // an auth check is in flight
	pendingToken string // token awaiting validation, saved on success
	mfaRequired  bool   // the server demanded a two-factor code; reveal the MFA field

	// Auto-capture of the mmauth:// SSO redirect (Linux). Started when the user
	// opens the browser; cap.URL delivers the captured link so the token fills
	// and validates itself, no copy-paste. capturing guards against starting it
	// twice; closeCapture tears it down on success/quit.
	cap       mmauth.Capture
	capturing bool

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
	focus        int
	markRead     int  // mark_read_delay_seconds
	sqlTab       bool // sql_tab
	mouse        bool // mouse
	thumbnails   bool // image_thumbnails: auto (vs off)
	animations   bool // animations.* (custom_emoji + image_preview + inline_images)
	ctrlArrow    bool // keybindings.nav_modifier == "ctrl" (vs "none")
	codeThemeIdx int  // index into Model.themeNames → code_theme
}

const advFieldCount = 7

// New builds the wizard over an already-loaded config, seeding the fields from
// whatever is already configured so re-running `welcome` edits rather than
// resets. The scene parameters reproduce the reference vaporascii invocation
// (glyph renderer, slow drive, low warm-coloured mountains, the "Matterbox"
// title flying in via intro.json). When demo is set, each letter of the title
// bobs up and down on a sine wave (the `--demo` flag).
func New(cfg *config.Config, demo bool) *Model {
	stops, _ := vapor.ParseHexStops("#ffd21e,#ff9b2f,#ff3d7f,#ec1e63")
	anim, _ := vapor.LoadAnimationJSON(introJSON)
	if demo && anim != nil {
		// Hold the choreography back so the scene drifts solo first (see the
		// demoIntroDelaySecs comment). The terrain isn't affected — only the sun
		// rise and title fly-in start later.
		anim.DelayBy(demoIntroDelaySecs)
	}
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
			Scale: 1.5, Depth: 1, RotX: 25, Demo: demo,
		},
		Anim: anim,
	})

	m := &Model{rend: rend, cfg: cfg, phase: phaseIntro, step: stepServer, demo: demo}
	m.themeNames = allThemeNames()
	if cfg.ServerURL != "" && cfg.ServerURL != config.PlaceholderServerURL {
		m.server.setValue(cfg.ServerURL)
	}
	m.adv = advanced{
		markRead:     derefInt(cfg.MarkReadDelaySeconds, 5),
		sqlTab:       derefBool(cfg.SQLTab, false),
		mouse:        derefBool(cfg.Mouse, true),
		thumbnails:   cfg.ImageThumbnails == "auto",
		animations:   derefBool(cfg.Animations.CustomEmoji, true),
		ctrlArrow:    navModEnabled(cfg.Keybindings.NavModifier),
		codeThemeIdx: themeIndex(m.themeNames, cfg.CodeTheme),
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
// intro fly-in, low once the wizard/done panel is up over a slowly drifting
// scene. Demo mode keeps the full rate throughout — its per-letter bob and flips
// run during the wizard too, so they'd stutter at the low rate.
func (m *Model) frameRate() int {
	if m.phase == phaseIntro || m.demo {
		return introFrameRate
	}
	return wizardFrameRate
}

// introEnd is the animation time at which the intro has fully settled (sun risen,
// title parked). Demo pushes it back by demoIntroDelaySecs to match the keyframes
// shifted in New, so skipping the intro still snaps to the settled pose.
func (m *Model) introEnd() float64 {
	if m.demo {
		return introSecs + demoIntroDelaySecs
	}
	return introSecs
}

// wizardAt is the animation time at which the wizard replaces the intro. Demo
// holds the settled scene longer, appearing demoWizardDelaySecs past introSecs.
func (m *Model) wizardAt() float64 {
	if m.demo {
		return introSecs + demoWizardDelaySecs
	}
	return introSecs
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

// passwordResultMsg reports the outcome of a username/password sign-in. Exactly
// one of success (token+user), mfaRequired, or err carries the result.
type passwordResultMsg struct {
	token       string
	user        string
	mfaRequired bool
	err         error
}

// passwordLoginCmd signs in with a username/password (and an optional two-factor
// code) in the background. A first attempt the server rejects asking for MFA
// comes back as mfaRequired so the wizard can reveal the code field.
func passwordLoginCmd(server, loginID, password, mfaToken string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		token, u, err := mm.New(server, "").LoginWithPassword(ctx, loginID, password, mfaToken)
		if err != nil {
			return passwordResultMsg{mfaRequired: mfaToken == "" && mm.MFARequired(err), err: err}
		}
		return passwordResultMsg{token: token, user: u.Username}
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
		if m.demo {
			// Envelope-follow the playback level: rise fast toward a louder beat,
			// fall slowly. Advanced per frame so the time constant is stable
			// regardless of how often PulseAudio pulls audio.
			lvl := musicLevel()
			coef := demoPulseRelease
			if lvl > m.pulse {
				coef = demoPulseAttack
			}
			m.pulse += (lvl - m.pulse) * coef
		}
		if m.phase == phaseIntro && m.t >= m.wizardAt() {
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

	case mmauthURLMsg:
		return m.handleCapturedURL(msg)

	case authResultMsg:
		return m.handleAuthResult(msg)

	case passwordResultMsg:
		return m.handlePasswordResult(msg)
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
		if m.demo {
			m.rend.SetHeightScale(m.mountainScale())
		}
		m.scene = copyGrid(m.scene, m.rend.Render(m.t))
		m.sceneT = m.t
		m.sceneValid = true
	}
	m.frame = copyGrid(m.frame, m.scene)
	return m.frame
}

// mountainScale maps the smoothed soundtrack level to a peak-height multiplier,
// clamped so loud passages can't shoot the mountains off-screen.
func (m *Model) mountainScale() float64 {
	s := demoMountainBase + demoMountainGain*m.pulse
	if s > demoMountainMax {
		s = demoMountainMax
	}
	return s
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

// activeField returns the text field accepting input for the current step (and
// focused control, on the auth step), or nil when there's no text entry under
// the cursor (the SSO button, or the advanced screen).
func (m *Model) activeField() *textField {
	if m.phase != phaseWizard {
		return nil
	}
	switch m.step {
	case stepServer:
		return &m.server
	case stepAuth:
		switch m.authFocus {
		case authFocusUser:
			return &m.user
		case authFocusPassword:
			return &m.password
		case authFocusToken:
			return &m.token
		case authFocusMFA:
			return &m.mfa
		}
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
