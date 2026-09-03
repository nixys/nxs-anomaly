package model

import "github.com/nixys/nxs-anomaly/internal/store"

// ChatopsChannel is the typed Record wrapper for the chatops_channels collection.
// TypedColumns: team_id, user_id, notifications_enabled.
type ChatopsChannel struct{ mapBacked }

var _ store.Record = ChatopsChannel{}

func WrapChatopsChannel(m map[string]any) ChatopsChannel {
	return ChatopsChannel{mapBacked{m}}
}

func (c ChatopsChannel) TypedValues() []any {
	return []any{
		tvStr(c.raw, "team_id"),
		tvStr(c.raw, "user_id"),
		tvBool(c.raw, "notifications_enabled"),
	}
}

// ChatopsMessage is the typed Record wrapper for the chatops_messages collection.
// No typed columns — TTL archival uses an expression index on
// ((data->>'created_at')) added in migration 0015.
type ChatopsMessage struct{ mapBacked }

var _ store.Record = ChatopsMessage{}

func WrapChatopsMessage(m map[string]any) ChatopsMessage {
	return ChatopsMessage{mapBacked{m}}
}

func (ChatopsMessage) TypedValues() []any { return nil }

func init() {
	registerMapBacked("chatops_channels", func(m map[string]any) store.Record { return WrapChatopsChannel(m) })
	registerMapBacked("chatops_messages", func(m map[string]any) store.Record { return WrapChatopsMessage(m) })
}
