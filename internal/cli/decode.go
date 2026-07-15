package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"matterbox/internal/effects"
	"matterbox/internal/game"
	"matterbox/internal/game/kurve"
	"matterbox/internal/hidden"
)

// decode is the window into the invisible half of a matterbox post. Several
// features smuggle a payload through a post body — the Gorillas game (MBG1),
// Achtung die Kurve (MBK1) and text effects (MBF1) so far — carried as a run of
// Unicode variation selectors that render as nothing (see internal/hidden). The
// whole point of that
// transport is that the state is unreadable in any client, so when one of these
// features misbehaves there is otherwise no way to look at what a post actually
// carries.
//
// The command takes the post body the way a human can get hold of one: copied
// out of a Mattermost client and pasted in. A copy that silently drops the
// invisible runes is itself a finding, so a body with no payload is reported as
// such rather than treated as empty.

func newDecodeCmd() *cobra.Command {
	var (
		postID     string
		cols, rows int
		raw        bool
	)

	cmd := &cobra.Command{
		Use:     "decode [body]",
		Aliases: []string{"game-debug"},
		Short:   "Decode the hidden payload smuggled in a post body",
		Long: "Decode the invisible payload matterbox smuggles through a post body.\n\n" +
			"More than one feature rides the same transport — the Gorillas game, Achtung\n" +
			"die Kurve, and text effects, so far — so this reports whichever channel a body\n" +
			"actually carries and decodes it: a game world, a controller input, or a set of\n" +
			"effect spans.\n\n" +
			"Paste the post body on stdin (end with ctrl-D), pass it as an argument, or\n" +
			"use --post <id> to fetch it from the server — which is the reliable route if\n" +
			"the client you copied from ate the invisible runes.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := decodeBody(cmd.Context(), cmd.InOrStdin(), postID, args)
			if err != nil {
				return err
			}
			return inspectPost(cmd.OutOrStdout(), body, cols, rows, raw)
		},
	}
	cmd.Flags().StringVar(&postID, "post", "", "fetch the body from this post id instead of stdin")
	cmd.Flags().IntVar(&cols, "cols", 64, "board width, in columns for a game payload (0 to skip the board)")
	cmd.Flags().IntVar(&rows, "rows", 18, "board height, in rows for a game payload")
	cmd.Flags().BoolVar(&raw, "raw", true, "show the raw body with the hidden runes made visible")
	return cmd
}

// decodeBody resolves the post body from whichever source the user gave.
func decodeBody(ctx context.Context, stdin io.Reader, postID string, args []string) (string, error) {
	if postID != "" {
		if len(args) > 0 {
			return "", errors.New("pass a body or --post, not both")
		}
		_, client, err := dial()
		if err != nil {
			return "", err
		}
		p, err := client.Post(ctx, postID)
		if err != nil {
			return "", err
		}
		return p.Message, nil
	}
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	if f, ok := stdin.(*os.File); ok {
		if st, err := f.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprintln(os.Stderr, "paste the post body, then ctrl-D:")
		}
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeOpts carries the presentation knobs a channel decoder may want. Only the
// game channel reads cols/rows (to draw its board); other channels ignore them.
type decodeOpts struct {
	cols, rows int
}

// inspectPost is the channel-agnostic driver: it reports the body's shape (bytes,
// runes, where the hidden runs sit), echoes the visible text, then hands each
// recognised run to its channel's decoder. Everything up to that hand-off is
// generic — the invisible transport is one thing, whatever it carries is another.
func inspectPost(out io.Writer, body string, cols, rows int, raw bool) error {
	body = strings.TrimRight(body, "\n")
	visible := hidden.Strip(body)
	// Strip removes exactly the payload runes, so the rune counts either side of it
	// tell us how many invisible runes the body carried — including any that never
	// made it past a lossy copy/paste.
	hiddenCount := len([]rune(body)) - len([]rune(visible))

	fmt.Fprintf(out, "body     %d bytes, %d runes (%d invisible)\n", len(body), len([]rune(body)), hiddenCount)

	runs := hiddenRuns(body)
	if len(runs) > 0 {
		fmt.Fprintln(out, "\nhidden runs — where the bytes are smuggled")
		for _, r := range runs {
			magic := "no magic, not ours"
			if r.magic != "" {
				magic = fmt.Sprintf("magic %s (%s) + %d payload bytes",
					r.magic, r.channel, len(r.bytes)-len(r.magic))
			}
			fmt.Fprintf(out, "  bytes %d–%d · runes %d–%d · %d runes · %s\n",
				r.byteStart, r.byteEnd, r.runeStart, r.runeStart+len(r.bytes), len(r.bytes), magic)
		}
	}

	if raw {
		fmt.Fprintln(out, "\nraw body — each ‹xx› is one invisible variation selector carrying byte xx")
		printRawBody(out, body)
	}

	fmt.Fprintln(out, "\nvisible text — what everyone else sees")
	for _, line := range strings.Split(visible, "\n") {
		fmt.Fprintln(out, "  "+line)
	}

	// Decode every run that opens with a channel we recognise. A post normally
	// carries exactly one, but the tool exists to show whatever is actually there —
	// so if a body somehow carried two channels, both would be reported.
	opts := decodeOpts{cols: cols, rows: rows}
	decoded := 0
	for _, r := range runs {
		ch := channelFor(r.magic)
		if ch == nil {
			continue
		}
		payload := r.bytes[len(ch.magic):]
		fmt.Fprintf(out, "\n%s payload — %d bytes, magic stripped\n", ch.name, len(payload))
		fmt.Fprint(out, hex.Dump(payload))
		if err := ch.decode(out, payload, visible, opts); err != nil {
			return fmt.Errorf("%s: %w", ch.name, err)
		}
		decoded++
	}
	if decoded > 0 {
		return nil
	}

	// Nothing decoded — say why, as specifically as the body allows, rather than
	// leave the reader hunting for a payload that was never here.
	fmt.Fprintln(out)
	return diagnoseNoPayload(hiddenCount)
}

// diagnoseNoPayload explains why a body carried nothing we could decode. The
// distinction that matters is whether the invisible runes are gone (a lossy
// copy) or merely unrecognised (a truncated blob, a newer channel, or an emoji's
// lone presentation selector) — the two send a reader down very different paths.
func diagnoseNoPayload(hiddenCount int) error {
	if hiddenCount == 0 {
		return errors.New("no payload: the body carries no invisible runes at all — whatever copied it stripped them (try --post <id>)")
	}
	var magics []string
	for _, c := range channels {
		magics = append(magics, c.magic)
	}
	return fmt.Errorf("no recognised payload: %d invisible runes present, but none opens with a channel magic (%s) — a truncated blob, a channel a newer matterbox added, or just an emoji's presentation selector (try --post <id>)",
		hiddenCount, strings.Join(magics, ", "))
}

// A channel is one feature that smuggles a payload through the invisible
// transport. It knows its magic prefix, a human name, and how to explain the
// bytes that follow the magic. Adding a new channel here teaches the decoder to
// recognise and read it — nothing else in this file is channel-specific.
type channel struct {
	magic string
	name  string
	// decode explains payload (the bytes after the magic). visible is the body's
	// human-readable text, which some channels index into (effect spans are rune
	// offsets into it); opts carries presentation knobs.
	decode func(out io.Writer, payload []byte, visible string, opts decodeOpts) error
}

// channels is the registry of payload magics matterbox smuggles through a post
// body (see internal/hidden). A run is reported and decoded as the channel it
// actually belongs to — a text-effects blob is a fact about that post, not an
// unrecognised smear of bytes.
var channels = []channel{
	{game.Magic, "gorillas", decodeGame},
	{kurve.Magic, "achtung die kurve", decodeKurve},
	{effects.MagicEffects, "text effects", decodeEffects},
}

// channelFor returns the channel with this magic, or nil if none is registered
// (an empty magic — a run that opened with nothing we know — always returns nil).
func channelFor(magic string) *channel {
	if magic == "" {
		return nil
	}
	for i := range channels {
		if channels[i].magic == magic {
			return &channels[i]
		}
	}
	return nil
}

// channelOf names the channel a run's bytes open with, or ok=false when they
// match none — an emoji's lone U+FE0F, or a truncated blob.
func channelOf(b []byte) (magic, name string, ok bool) {
	for _, c := range channels {
		if len(b) > len(c.magic) && string(b[:len(c.magic)]) == c.magic {
			return c.magic, c.name, true
		}
	}
	return "", "", false
}

// hiddenRun is one maximal stretch of invisible payload runes, located in the
// body. Decode reads exactly these runs, so listing them is listing everywhere in
// the post that could be carrying data — the magic flag says which one actually
// is, and an emoji's lone U+FE0F shows up here as the near-miss it is.
type hiddenRun struct {
	byteStart, byteEnd int
	runeStart          int
	bytes              []byte // the bytes the run's runes carry, magic included
	magic              string // the channel magic the run opens with, "" if none
	channel            string // what writes that channel, for humans
}

func hiddenRuns(body string) []hiddenRun {
	var runs []hiddenRun
	var cur *hiddenRun
	ri := 0
	for bi, r := range body {
		b, ok := hidden.PayloadByte(r)
		if !ok {
			cur = nil
			ri++
			continue
		}
		if cur == nil {
			runs = append(runs, hiddenRun{byteStart: bi, runeStart: ri})
			cur = &runs[len(runs)-1]
		}
		cur.bytes = append(cur.bytes, b)
		cur.byteEnd = bi + len(string(r))
		cur.magic, cur.channel, _ = channelOf(cur.bytes)
		ri++
	}
	return runs
}

// printRawBody prints the body with every invisible rune spelled out as the byte
// it carries, in place. Visible text stays verbatim, so the output shows the blob
// exactly where it sits: after the board, at the end of the post.
func printRawBody(out io.Writer, body string) {
	// The blob runs to hundreds of runes on a well-cratered world, so it is chunked
	// rather than emitted as one unreadable line.
	const perLine = 16

	for _, line := range strings.Split(body, "\n") {
		var vis strings.Builder
		var hid []byte
		wrote := false

		flushVis := func() {
			if vis.Len() > 0 {
				fmt.Fprintln(out, "  "+vis.String())
				vis.Reset()
				wrote = true
			}
		}
		flushHid := func() {
			for i := 0; i < len(hid); i += perLine {
				var b strings.Builder
				for _, c := range hid[i:min(i+perLine, len(hid))] {
					fmt.Fprintf(&b, "‹%02x›", c)
				}
				fmt.Fprintln(out, "  "+b.String())
				wrote = true
			}
			hid = hid[:0]
		}

		for _, r := range line {
			if b, ok := hidden.PayloadByte(r); ok {
				flushVis()
				hid = append(hid, b)
				continue
			}
			flushHid()
			vis.WriteRune(r)
		}
		flushVis()
		flushHid()
		if !wrote {
			fmt.Fprintln(out)
		}
	}
}

// decodeEffects explains an MBF1 text-effects payload: the spans it carries, the
// run of visible text each one paints, and the composer markup they add up to.
// A span whose offsets fall outside the current text is flagged rather than
// silently dropped — that is exactly the symptom of a post edited from another
// client after the effects were written (the offsets no longer line up, so the
// effect stops applying), and it is worth naming.
func decodeEffects(out io.Writer, payload []byte, visible string, _ decodeOpts) error {
	spans, ok := effects.UnmarshalPayload(payload)
	if !ok {
		return errors.New("payload is truncated or in a format this build doesn't know")
	}

	fmt.Fprintln(out, "\nkind     text effects")
	if len(payload) > 0 {
		fmt.Fprintf(out, "version  %d\n", payload[0])
	}
	fmt.Fprintf(out, "spans    %d\n", len(spans))

	rs := []rune(visible)
	for i, s := range spans {
		name := effects.Name(s.ID)
		if name == "" { // unmarshalled spans are always known, but don't assume it
			name = fmt.Sprintf("effect#%d", s.ID)
		}
		end := s.Start + s.Len
		if s.Start < 0 || s.Len < 0 || end > len(rs) {
			fmt.Fprintf(out, "  %2d  %-8s runes %d–%d  (out of range — the visible text is only %d runes; edited from another client?)\n",
				i, name, s.Start, end, len(rs))
			continue
		}
		fmt.Fprintf(out, "  %2d  %-8s runes %d–%d  %q\n", i, name, s.Start, end, string(rs[s.Start:end]))
	}

	// Reconstruct is Parse's inverse: it rebuilds the markup a user would type to
	// get these spans, which is the clearest picture of what the effects do — the
	// braces show the nesting the flat span list can't.
	fmt.Fprintf(out, "\nmarkup   %s\n", effects.Reconstruct(visible, spans))
	return nil
}

// decodeGame explains an MBG1 payload: either a controller input (the joiner's
// post) or a full game world (the host's). A state payload is 35 bytes before its
// craters; an input is exactly 4. The two are never confusable by length, so the
// joiner's controller post decodes as what it is rather than as a mangled world.
func decodeGame(out io.Writer, payload []byte, _ string, opts decodeOpts) error {
	if len(payload) == len(game.MarshalInput(&game.Input{})) {
		in, err := game.UnmarshalInput(payload)
		if err != nil {
			return fmt.Errorf("input: %w", err)
		}
		fmt.Fprintln(out, "\nkind     input (the joiner's controller post)")
		fmt.Fprintf(out, "angle    %d°\n", in.Angle)
		fmt.Fprintf(out, "power    %d\n", in.Power)
		fmt.Fprintf(out, "seq      %d\n", in.Seq)
		return nil
	}

	st, err := game.UnmarshalState(payload)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	printState(out, st, opts.cols, opts.rows)
	return nil
}

func printState(out io.Writer, st *game.State, cols, rows int) {
	w := st.World()

	fmt.Fprintln(out, "\nkind     state (the host's post)")
	fmt.Fprintf(out, "seed     %d\n", st.Seed)
	fmt.Fprintf(out, "wind     %d %s\n", st.Wind, windDir(st.Wind))
	fmt.Fprintf(out, "phase    %s\n", phaseName(st.Phase))
	fmt.Fprintf(out, "turn     %d (%s)\n", st.Turn, playerName(int(st.Turn)))
	fmt.Fprintf(out, "scores   host %d – joiner %d (first to %d)\n", st.Scores[0], st.Scores[1], game.WinScore)
	if st.Winner >= 0 {
		fmt.Fprintf(out, "winner   %d (%s)\n", st.Winner, playerName(int(st.Winner)))
	} else {
		fmt.Fprintln(out, "winner   none yet")
	}
	if st.Joiner == "" {
		fmt.Fprintln(out, "joiner   — (still in the lobby)")
	} else {
		fmt.Fprintf(out, "joiner   %s\n", st.Joiner)
	}

	g := w.Gorillas
	fmt.Fprintf(out, "gorillas host (%d,%d) · joiner (%d,%d)\n", g[0].X, g[0].Y, g[1].X, g[1].Y)

	shot := st.LiveShot(w)
	if shot == nil {
		fmt.Fprintln(out, "shot     none in the air")
	} else {
		x, y := shot.Pos()
		fmt.Fprintf(out, "shot     angle %d° · power %d · t %.2fs · at (%.0f,%.0f)\n",
			st.Shot.Angle, st.Shot.Power, float64(st.Shot.T)/100, x, y)
	}

	if st.Boom == nil {
		fmt.Fprintln(out, "boom     nothing burning")
	} else {
		fmt.Fprintf(out, "boom     %s at (%d,%d) · frame %d\n",
			boomName(st.Boom.Kind), st.Boom.X, st.Boom.Y, st.Boom.Frame)
	}
	if st.Dance == nil {
		fmt.Fprintln(out, "dance    nobody is celebrating")
	} else {
		fmt.Fprintf(out, "dance    %s · frame %d\n", playerName(int(st.Dance.Player)), st.Dance.Frame)
	}
	fmt.Fprintf(out, "sun      %s\n", sunState(st.SunHit))

	// Craters are ellipses: QBasic's CIRCLE is one, so every hole the game punches
	// is wider than it is tall (a banana) or a great deal taller than it is wide (a
	// gorilla).
	fmt.Fprintf(out, "craters  %d\n", len(st.Craters))
	for i, c := range st.Craters {
		fmt.Fprintf(out, "  %2d  (%d,%d) rx=%d ry=%d\n", i, c.X, c.Y, c.RX, c.RY)
	}

	if cols > 0 && rows > 0 {
		fmt.Fprintln(out, "\nboard")
		for _, line := range game.RenderASCII(w, shot, cols, rows) {
			fmt.Fprintln(out, "  "+line)
		}
	}
}

// decodeKurve explains an MBK1 payload: either a controller input (the joiner's
// post) or a full game state (the host's). An input is 3 bytes and a state is
// dozens, so the two are never confusable by length.
func decodeKurve(out io.Writer, payload []byte, _ string, opts decodeOpts) error {
	if len(payload) == len(kurve.MarshalInput(&kurve.Input{})) {
		in, err := kurve.UnmarshalInput(payload)
		if err != nil {
			return fmt.Errorf("input: %w", err)
		}
		fmt.Fprintln(out, "\nkind     input (the joiner's controller post)")
		fmt.Fprintf(out, "steer    %s\n", kurveDir(in.Dir))
		fmt.Fprintf(out, "seq      %d\n", in.Seq)
		return nil
	}

	st, err := kurve.UnmarshalState(payload)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	printKurveState(out, st, opts.cols, opts.rows)
	return nil
}

func printKurveState(out io.Writer, st *kurve.State, cols, rows int) {
	fmt.Fprintln(out, "\nkind     state (the host's post)")
	fmt.Fprintf(out, "seed     %d\n", st.Seed)
	fmt.Fprintf(out, "phase    %s\n", kurvePhaseName(st.Phase))
	fmt.Fprintf(out, "tick     %d\n", st.Tick)
	fmt.Fprintf(out, "players  %d\n", len(st.Scores))

	var scores strings.Builder
	for i, s := range st.Scores {
		if i > 0 {
			scores.WriteString(" · ")
		}
		fmt.Fprintf(&scores, "P%d %d", i, s)
	}
	fmt.Fprintf(out, "scores   %s (first to %d)\n", scores.String(), kurve.WinScore)

	if st.Winner >= 0 {
		fmt.Fprintf(out, "winner   P%d\n", st.Winner)
	} else {
		fmt.Fprintln(out, "winner   none yet")
	}
	if len(st.Joiners) == 0 {
		fmt.Fprintln(out, "joiners  — (host alone in the lobby)")
	} else {
		for i, j := range st.Joiners {
			fmt.Fprintf(out, "joiner   P%d %s\n", i+1, j)
		}
	}
	for i, d := range st.Deaths {
		if d == 0xFFFF {
			fmt.Fprintf(out, "curve %d  alive · %d steering changes\n", i, len(st.Turns[i]))
		} else {
			fmt.Fprintf(out, "curve %d  crashed at tick %d · %d steering changes\n", i, d, len(st.Turns[i]))
		}
	}

	if cols > 0 && rows > 0 {
		fmt.Fprintln(out, "\nboard")
		for _, line := range kurve.RenderASCII(kurve.FromState(st).Sim, cols, rows) {
			fmt.Fprintln(out, "  "+line)
		}
	}
}

func kurvePhaseName(p kurve.Phase) string {
	switch p {
	case kurve.PhaseLobby:
		return "lobby (waiting for players)"
	case kurve.PhaseCountdown:
		return "countdown (get ready)"
	case kurve.PhaseRun:
		return "run (the curves are moving)"
	case kurve.PhaseRoundOver:
		return "round over (holding before the next arena)"
	case kurve.PhaseOver:
		return "over"
	}
	return fmt.Sprintf("unknown (%d)", p)
}

func kurveDir(d kurve.Dir) string {
	switch d {
	case kurve.Left:
		return "left"
	case kurve.Right:
		return "right"
	default:
		return "straight"
	}
}

func phaseName(p game.Phase) string {
	switch p {
	case game.PhaseLobby:
		return "lobby (waiting for a second player)"
	case game.PhaseAiming:
		return "aiming"
	case game.PhaseFlight:
		return "flight (a banana is in the air)"
	case game.PhaseBoom:
		return "boom (a fireball is collapsing; the crater is not cut yet)"
	case game.PhaseDance:
		return "dance (the winner is celebrating; the next city is not drawn yet)"
	case game.PhaseOver:
		return "over"
	}
	return fmt.Sprintf("unknown (%d)", p)
}

func boomName(kind uint8) string {
	if game.ExplosionKind(kind) == game.BoomGorilla {
		return "gorilla (a direct hit)"
	}
	return "banana (masonry)"
}

func sunState(hit bool) string {
	if hit {
		return "shocked (a banana went through it)"
	}
	return "happy"
}

func playerName(p int) string {
	if p == 0 {
		return "host"
	}
	return "joiner"
}

func windDir(wind int8) string {
	switch {
	case wind == 0:
		return "(calm)"
	case wind < 0:
		return "(blowing left)"
	default:
		return "(blowing right)"
	}
}
