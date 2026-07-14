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

	"matterbox/internal/game"
)

// game-debug is the window into the invisible half of a Gorillas post. The whole
// point of the transport is that the state is unreadable — variation selectors
// render as nothing — so when a match misbehaves there is otherwise no way to
// look at what a post actually says.
//
// It takes the post body the way a human can get hold of one: copied out of a
// Mattermost client and pasted in. A copy that silently drops the invisible runes
// is itself a finding, so a body with no payload is reported as such rather than
// treated as an empty game.

func newGameDebugCmd() *cobra.Command {
	var (
		postID     string
		cols, rows int
		raw        bool
	)

	cmd := &cobra.Command{
		Use:   "game-debug [body]",
		Short: "Decode the hidden game state in a Gorillas post",
		Long: "Decode the invisible state blob carried by a Gorillas post.\n\n" +
			"Paste the post body on stdin (end with ctrl-D), pass it as an argument, or\n" +
			"use --post <id> to fetch it from the server — which is the reliable route if\n" +
			"the client you copied from ate the invisible runes.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := gameDebugBody(cmd.Context(), cmd.InOrStdin(), postID, args)
			if err != nil {
				return err
			}
			return inspectGamePost(cmd.OutOrStdout(), body, cols, rows, raw)
		},
	}
	cmd.Flags().StringVar(&postID, "post", "", "fetch the body from this post id instead of stdin")
	cmd.Flags().IntVar(&cols, "cols", 64, "board width, in columns (0 to skip the board)")
	cmd.Flags().IntVar(&rows, "rows", 18, "board height, in rows")
	cmd.Flags().BoolVar(&raw, "raw", true, "show the raw body with the hidden runes made visible")
	return cmd
}

// gameDebugBody resolves the post body from whichever source the user gave.
func gameDebugBody(ctx context.Context, stdin io.Reader, postID string, args []string) (string, error) {
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

func inspectGamePost(out io.Writer, body string, cols, rows int, raw bool) error {
	body = strings.TrimRight(body, "\n")
	visible := game.Strip(body)
	// Strip removes exactly the payload runes, so the rune counts either side of it
	// tell us how many invisible runes the body carried — including any that never
	// made it past a lossy copy/paste.
	hidden := len([]rune(body)) - len([]rune(visible))

	fmt.Fprintf(out, "body     %d bytes, %d runes (%d invisible)\n", len(body), len([]rune(body)), hidden)

	runs := hiddenRuns(body)
	if len(runs) > 0 {
		fmt.Fprintln(out, "\nhidden runs — where the bytes are smuggled")
		for _, r := range runs {
			magic := "no magic, not ours"
			if r.magic {
				magic = fmt.Sprintf("magic %s + %d payload bytes", game.Magic, len(r.bytes)-len(game.Magic))
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

	payload, ok := game.Decode(body)
	if !ok {
		fmt.Fprintln(out)
		if hidden == 0 {
			return errors.New("no payload: the body carries no invisible runes at all — whatever copied it stripped them (try --post <id>)")
		}
		return fmt.Errorf("no payload: %d invisible runes present, but no run of them starts with the %s magic — the blob is truncated or mangled (try --post <id>)", hidden, game.Magic)
	}

	fmt.Fprintf(out, "\npayload  %d bytes, magic stripped\n", len(payload))
	fmt.Fprint(out, hex.Dump(payload))

	// A state payload is 35 bytes before its craters; an input is exactly 4. The
	// two are never confusable by length, so the joiner's controller post decodes
	// as what it is rather than as a mangled world.
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
	printState(out, st, cols, rows)
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

// hiddenRun is one maximal stretch of invisible payload runes, located in the
// body. Decode reads exactly these runs, so listing them is listing everywhere in
// the post that could be carrying data — the magic flag says which one actually
// is, and an emoji's lone U+FE0F shows up here as the near-miss it is.
type hiddenRun struct {
	byteStart, byteEnd int
	runeStart          int
	bytes              []byte // the bytes the run's runes carry, magic included
	magic              bool
}

func hiddenRuns(body string) []hiddenRun {
	var runs []hiddenRun
	var cur *hiddenRun
	ri := 0
	for bi, r := range body {
		b, ok := game.PayloadByte(r)
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
		cur.magic = len(cur.bytes) > len(game.Magic) && string(cur.bytes[:len(game.Magic)]) == game.Magic
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
			if b, ok := game.PayloadByte(r); ok {
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
