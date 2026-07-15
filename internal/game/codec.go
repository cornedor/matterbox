// Package game implements the Gorillas artillery game and the in-post
// multiplayer transport it rides on. The transport itself — an invisible payload
// smuggled through a Mattermost post body, one byte per Unicode variation
// selector — is generic and lives in internal/hidden. This file pins the game's
// channel magic and re-exports the codec under a stable, game-scoped API so game
// code and `matterbox decode` don't reach across packages.
package game

import "matterbox/internal/hidden"

// Magic prefixes every game payload. "MBG1" = MatterBox Game, v1. See
// hidden.Encode for why a magic prefix is needed at all.
const Magic = "MBG1"

// Encode renders payload as an invisible run of variation selectors, game magic
// included. The result is safe to append to any post body.
func Encode(payload []byte) string { return hidden.Encode(Magic, payload) }

// Decode extracts the game payload from a post body, or ok=false if the body
// carries none for this channel.
func Decode(msg string) ([]byte, bool) { return hidden.Decode(Magic, msg) }

// Strip removes every hidden payload rune from msg (any channel), leaving the
// human-readable text. Used by the message pane, the SQLite cache, and search.
func Strip(msg string) string { return hidden.Strip(msg) }

// PayloadByte reports the byte r carries, or ok=false if r is not a payload
// rune. Used by `matterbox decode` to walk a post body rune by rune.
func PayloadByte(r rune) (byte, bool) { return hidden.PayloadByte(r) }
