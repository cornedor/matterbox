package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/spf13/cobra"
)

func newChannelsCmd() *cobra.Command {
	var asJSONFn func() (bool, error)
	cmd := &cobra.Command{
		Use:   "channels",
		Short: "List teams and channels, resolving ids and addresses",
		Long: "List every channel you're in — across all teams, plus DMs and group DMs —\n" +
			"with its id and the address you'd pass to read/send (team/channel or\n" +
			"@user). Use it to discover what's available or to map an id to a name and\n" +
			"back.\n\n" +
			"Text output groups channels under a `# team` header, one channel per line\n" +
			"as: id, type, address, display name (id first so it's awk-friendly). Group\n" +
			"DMs have no CLI address, so their address column is blank. --json emits one\n" +
			"object per channel (JSON Lines).\n\n" +
			"  matterbox channels\n" +
			"  matterbox channels --json | jq 'select(.type==\"private\")'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := asJSONFn()
			if err != nil {
				return err
			}
			return runChannels(cmd.Context(), asJSON, cmd.OutOrStdout())
		},
	}
	asJSONFn = addOutputFlags(cmd)
	return cmd
}

// jsonChannel is the wire shape of a channel in `channels` output. Address is
// what read/send accept (team/channel or @user); it's empty for group DMs,
// which the CLI can't address. Team is the URL slug.
type jsonChannel struct {
	ID          string `json:"id"`
	Address     string `json:"address"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	Team        string `json:"team,omitempty"`
	TeamID      string `json:"team_id,omitempty"`
	Partner     string `json:"partner,omitempty"`
}

func runChannels(ctx context.Context, asJSON bool, out io.Writer) error {
	_, client, err := dial()
	if err != nil {
		return err
	}
	me, err := client.Me(ctx)
	if err != nil {
		return err
	}
	channels, err := client.AllChannels(ctx, me.Id)
	if err != nil {
		return err
	}

	// Team metadata labels the groups and builds team/channel addresses;
	// best-effort, so a Teams hiccup degrades to raw ids rather than failing.
	teamSlug := map[string]string{}
	teamName := map[string]string{}
	if teams, terr := client.Teams(ctx, me.Id); terr == nil {
		for _, t := range teams {
			teamSlug[t.Id] = t.Name
			teamName[t.Id] = t.DisplayName
		}
	}
	names, _ := client.UsernamesByIDs(ctx, channelPartnerIDs(channels, me.Id))
	if names == nil {
		names = map[string]string{}
	}

	rows := make([]jsonChannel, 0, len(channels))
	for _, ch := range channels {
		rows = append(rows, channelRow(ch, teamSlug, names, me.Id))
	}
	ordered := orderChannels(rows, teamName)

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetEscapeHTML(false)
		for _, r := range ordered {
			if err := enc.Encode(r); err != nil {
				return err
			}
		}
		return nil
	}
	printChannels(out, ordered, teamName)
	return nil
}

// channelPartnerIDs collects the DM partners across all direct-message channels
// so their usernames resolve in one batched lookup.
func channelPartnerIDs(channels []*model.Channel, meID string) []string {
	seen := map[string]bool{}
	var ids []string
	for _, ch := range channels {
		if ch.Type != model.ChannelTypeDirect {
			continue
		}
		if id := dmPartnerID(ch, meID); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// channelRow projects a channel onto the wire shape, computing its CLI address
// (team/channel, @partner, or "" for group DMs) and friendly type.
func channelRow(ch *model.Channel, teamSlug, names map[string]string, meID string) jsonChannel {
	r := jsonChannel{
		ID:          ch.Id,
		Name:        ch.Name,
		DisplayName: ch.DisplayName,
		Type:        channelType(ch),
		TeamID:      ch.TeamId,
	}
	switch ch.Type {
	case model.ChannelTypeDirect:
		r.Partner = names[dmPartnerID(ch, meID)]
		if r.Partner != "" {
			r.Address = "@" + r.Partner
		}
	case model.ChannelTypeGroup:
		// Group DMs aren't addressable by the CLI (their Name is a generated
		// hash); leave Address empty and rely on the display name.
	default:
		if slug := teamSlug[ch.TeamId]; slug != "" {
			r.Team = slug
			r.Address = slug + "/" + ch.Name
		} else {
			r.Address = ch.Name
		}
	}
	return r
}

// channelType renders the one-letter Mattermost channel type as a word.
func channelType(ch *model.Channel) string {
	switch ch.Type {
	case model.ChannelTypeOpen:
		return "public"
	case model.ChannelTypePrivate:
		return "private"
	case model.ChannelTypeDirect:
		return "direct"
	case model.ChannelTypeGroup:
		return "group"
	default:
		return string(ch.Type)
	}
}

// orderChannels sorts rows for display: team channels first (teams alphabetical
// by label, channels by address), then DMs and group DMs together at the end.
// The same order is used for text and JSON so output is deterministic.
func orderChannels(rows []jsonChannel, teamName map[string]string) []jsonChannel {
	out := append([]jsonChannel(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := groupKey(out[i], teamName), groupKey(out[j], teamName)
		if gi != gj {
			return gi < gj
		}
		return rowKey(out[i]) < rowKey(out[j])
	})
	return out
}

// isDM reports whether a row is a direct or group DM (the "Direct messages"
// bucket, which sorts after every team).
func isDM(r jsonChannel) bool {
	return r.Type == "direct" || r.Type == "group"
}

// groupKey orders the groups: each team's channels under "0<team label>", all
// DMs under "1" so they come last.
func groupKey(r jsonChannel, teamName map[string]string) string {
	if isDM(r) {
		return "1"
	}
	return "0" + strings.ToLower(teamLabel(teamName, r.TeamID))
}

// groupHeader is the printed header a row belongs under.
func groupHeader(r jsonChannel, teamName map[string]string) string {
	if isDM(r) {
		return "Direct messages"
	}
	return teamLabel(teamName, r.TeamID)
}

// rowKey orders channels within a group: by address when there is one, else by
// display name (group DMs).
func rowKey(r jsonChannel) string {
	if r.Address != "" {
		return strings.ToLower(r.Address)
	}
	return strings.ToLower(r.DisplayName)
}

// teamLabel is a team's display name, falling back to its id (or a placeholder
// for the team-less case) when the Teams lookup didn't cover it.
func teamLabel(teamName map[string]string, teamID string) string {
	if n := teamName[teamID]; n != "" {
		return n
	}
	if teamID == "" {
		return "(no team)"
	}
	return teamID
}

// printChannels writes the grouped, aligned text listing: a "# header" line per
// group (blank line between groups), then one row per channel as
// "id  type  address  display name".
func printChannels(out io.Writer, ordered []jsonChannel, teamName map[string]string) {
	addrW := 8
	for _, r := range ordered {
		if l := len(r.Address); l > addrW {
			addrW = l
		}
	}
	if addrW > 32 {
		addrW = 32
	}
	curHeader := ""
	for _, r := range ordered {
		if h := groupHeader(r, teamName); h != curHeader {
			if curHeader != "" {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "# %s\n", h)
			curHeader = h
		}
		fmt.Fprintf(out, "%s  %-7s  %-*s  %s\n", r.ID, r.Type, addrW, r.Address, r.DisplayName)
	}
}
