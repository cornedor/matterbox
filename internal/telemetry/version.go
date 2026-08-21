package telemetry

// Upgrade detection.
//
// "Do people update?" is not answerable from the events alone: app_started
// carries the version it is running, but a fleet that never upgrades and a
// fleet that upgraded yesterday look identical in a single launch. What
// distinguishes them is the *change*, and that needs one thing remembered
// across runs.
//
// The remembered value lives in the message cache's meta table rather than in
// config.yaml. config.yaml is the user's document — hand-edited, commented, and
// rewritten wholesale by any save — and silently reformatting it on the first
// launch after an upgrade is a poor trade for one string. The cache is our own
// state, already present in every surface that could report this, and losing it
// costs nothing but one missed upgrade event.

// versionMetaKey is where the last-seen build is remembered. Namespaced like
// the rules engine's own meta keys so the reserved prefixes stay legible.
const versionMetaKey = "telemetry:last_version"

// VersionStore is the pair of meta operations CheckVersion needs, so the
// telemetry package doesn't depend on the store package (and so a test can pass
// a map).
type VersionStore interface {
	GetMeta(key string) (value string, ok bool, err error)
	SetMeta(key, value string) error
}

// CheckVersion reports version_upgraded when this build differs from the last
// one seen, and remembers the new one either way. Safe to call with telemetry
// off (it does nothing at all), with a nil store, or with an empty version —
// none of which is worth a branch at the call site. A store whose *pointer* is
// nil (the message cache failed to open, which the TUI degrades to rather than
// refusing to start) simply remembers nothing, so no upgrade is ever reported
// for that session.
//
// A first run records the version without reporting an upgrade: arriving from
// nowhere is an install, and counting it as an upgrade would make every new
// user look like a successful rollout.
func CheckVersion(st VersionStore, version string) {
	if !active.Load() || st == nil || version == "" {
		return
	}
	prev, ok, err := st.GetMeta(versionMetaKey)
	if err != nil {
		return
	}
	if prev == version {
		return
	}
	// Write first: a failed report is better than a repeated one, and an
	// upgrade announced on every launch would overstate the rollout.
	if err := st.SetMeta(versionMetaKey, version); err != nil {
		return
	}
	if !ok || prev == "" {
		return // first run with a store — an install, not an upgrade
	}
	VersionUpgraded(prev, version)
}
