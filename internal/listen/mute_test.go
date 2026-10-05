package listen

import (
	"encoding/json"
	"io"
	"log"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
)

// TestMuteFollowsMemberUpdated: muting or unmuting on another client mid-
// connection must reach the notify gate, not wait for the next reconnect.
func TestMuteFollowsMemberUpdated(t *testing.T) {
	e := &Engine{me: &model.User{Id: "u-me"}, log: log.New(io.Discard, "", 0)}
	send := func(userID, markUnread string) {
		mb := model.ChannelMember{ChannelId: "c1", UserId: userID, NotifyProps: model.StringMap{model.MarkUnreadNotifyProp: markUnread}}
		raw, _ := json.Marshal(mb)
		ev := model.NewWebSocketEvent(model.WebsocketEventChannelMemberUpdated, "", "c1", userID, nil, "")
		ev = ev.SetData(map[string]any{"channelMember": string(raw)})
		e.handle(t.Context(), ev)
	}

	send("u-me", model.ChannelMarkUnreadMention)
	if !e.isMuted("c1") {
		t.Fatal("mute not applied")
	}
	send("u-other", model.ChannelMarkUnreadAll) // someone else's row: ignored
	if !e.isMuted("c1") {
		t.Fatal("another user's member update unmuted our channel")
	}
	send("u-me", model.ChannelMarkUnreadAll)
	if e.isMuted("c1") {
		t.Fatal("unmute not applied")
	}
}
