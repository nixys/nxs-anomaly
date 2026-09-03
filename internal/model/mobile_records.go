package model

import "github.com/nixys/nxs-anomaly/internal/store"

// MobileDevice is the typed Record wrapper for the mobile_devices collection.
// TypedColumns: user_id, platform, active.
type MobileDevice struct{ mapBacked }

var _ store.Record = MobileDevice{}

func WrapMobileDevice(m map[string]any) MobileDevice {
	return MobileDevice{mapBacked{m}}
}

func (d MobileDevice) TypedValues() []any {
	return []any{
		tvStr(d.raw, "user_id"),
		tvStr(d.raw, "platform"),
		tvBool(d.raw, "active"),
	}
}

// MobileSession is the typed Record wrapper for the mobile_sessions collection.
// TypedColumns: token, user_id, device_id, revoked_at.
type MobileSession struct{ mapBacked }

var _ store.Record = MobileSession{}

func WrapMobileSession(m map[string]any) MobileSession {
	return MobileSession{mapBacked{m}}
}

func (s MobileSession) TypedValues() []any {
	return []any{
		tvStr(s.raw, "token"),
		tvStr(s.raw, "user_id"),
		tvStr(s.raw, "device_id"),
		tvStr(s.raw, "revoked_at"),
	}
}

func init() {
	registerMapBacked("mobile_devices", func(m map[string]any) store.Record { return WrapMobileDevice(m) })
	registerMapBacked("mobile_sessions", func(m map[string]any) store.Record { return WrapMobileSession(m) })
}
