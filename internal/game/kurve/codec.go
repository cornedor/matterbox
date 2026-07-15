// Package kurve implements "Achtung, die Kurve" — two continuously-moving
// curves that leave a solid trail, steered left or right, dying the instant a
// head touches a wall or any trail — and rides it through a Mattermost post the
// same way internal/game rides Gorillas.
//
// It is a deliberate re-use of the Gorillas transport (internal/hidden, an
// invisible run of variation selectors smuggled through a post body) applied to
// a game that stresses it in a way Gorillas never does. Gorillas is turn-based:
// one discrete shot, then the other player's discrete shot, which maps onto
// discrete post edits without a fight. Achtung is real-time and, worse, its world
// grows without bound — a trail is not a handful of craters, it is hundreds of
// cells a second — so streaming the trail itself would blow the post size in
// seconds.
//
// The answer is the same trick Gorillas already leans on, pushed one step
// further. A Gorillas world is not sent; it is *derived* from a two-byte seed
// plus a short list of craters, and the joiner rebuilds it. A Kurve world is
// likewise derived: from the seed (start positions, headings, gap schedule) plus
// each player's log of steering changes. Steering is the only input, it changes
// rarely, and between changes the curve is a pure function of time — so the whole
// match compresses to a seed and two short event logs, and the joiner rebuilds
// the trail by replaying them through the identical simulation. See wire.go
// (FromState) and match.go.
//
// Authority still lives in exactly one place. The host is the only simulator: it
// alone decides when a head hits something, and it authors the input log (its own
// steering plus the joiner's, read off the joiner's controller post). The joiner
// replays that host-authored log purely to draw — it can no more disagree about
// the trail than a Gorillas joiner can disagree about where the buildings are.
package kurve

import "matterbox/internal/hidden"

// Magic prefixes every Kurve payload. "MBK1" = MatterBox Kurve, v1. A distinct
// magic is what lets a Kurve post and a Gorillas post (MBG1) share a channel, a
// join reaction, and every decode path without ever being mistaken for one
// another — hidden.Decode only returns a run that opens with the magic asked for.
const Magic = "MBK1"

// Encode renders payload as an invisible run of variation selectors, Kurve magic
// included. The result is safe to append to any post body.
func Encode(payload []byte) string { return hidden.Encode(Magic, payload) }

// Decode extracts the Kurve payload from a post body, or ok=false if the body
// carries none for this channel.
func Decode(msg string) ([]byte, bool) { return hidden.Decode(Magic, msg) }
