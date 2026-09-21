package tests

import (
	"context"
	"testing"
	"time"

	"github.com/nixys/nxs-anomaly/internal/authz"
	"github.com/nixys/nxs-anomaly/internal/utils"
)

// TestMobilePairingAgainstPostgres covers the SQL the in-memory store only
// imitates: a code is consumed by the statement that reads it, an expired one
// yields nothing, and a session is found by hash and only while it is live.
func TestMobilePairingAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	st, eng := newIntegrationEngine(t, ctx)
	defer st.Close()
	clearStore(t, ctx, st)

	user, err := eng.CreateUser(ctx, map[string]any{"name": "Pair User", "username": "pair-user", "role": "responder"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	userID := utils.StrVal(user, "id")

	live := authz.HashSessionToken("LIVE0-CODE0")
	if err := st.CreateMobilePairingCode(ctx, live, userID, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("create code: %v", err)
	}
	if got, err := st.RedeemMobilePairingCode(ctx, live); err != nil || got != userID {
		t.Fatalf("redeem: user=%q err=%v, want %s", got, err, userID)
	}
	if got, err := st.RedeemMobilePairingCode(ctx, live); err != nil || got != "" {
		t.Fatalf("second redeem: user=%q err=%v, want nothing", got, err)
	}
	expired := authz.HashSessionToken("DEAD0-CODE0")
	if err := st.CreateMobilePairingCode(ctx, expired, userID, time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("create expired code: %v", err)
	}
	if got, err := st.RedeemMobilePairingCode(ctx, expired); err != nil || got != "" {
		t.Fatalf("expired code redeemed: user=%q err=%v", got, err)
	}

	// Through the engine, as the phone does.
	ctx = authz.NewContext(ctx, authz.Actor{ID: userID, Kind: authz.KindUser, Role: authz.RoleResponder})
	pairing, err := eng.CreateMobilePairing(ctx)
	if err != nil {
		t.Fatalf("pairing: %v", err)
	}
	issued, err := eng.RedeemMobilePairing(ctx, map[string]any{"code": pairing["code"], "platform": "android"})
	if err != nil {
		t.Fatalf("redeem pairing: %v", err)
	}
	token := utils.StrVal(issued, "token")
	if _, u, err := eng.AuthenticateMobileSession(ctx, token); err != nil || utils.StrVal(u, "id") != userID {
		t.Fatalf("authenticate: user=%v err=%v", u, err)
	}
	// The typed column carries the hash, not the token.
	sess, err := st.GetItem(ctx, "mobile_sessions", utils.StrVal(issued, "session_id"))
	if err != nil || sess["token"] != authz.HashSessionToken(token) {
		t.Fatalf("stored session = %v (err %v), want the token hash", sess, err)
	}
	if err := eng.RevokeMobileSession(ctx, token); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, u, err := eng.AuthenticateMobileSession(ctx, token); err != nil || u != nil {
		t.Fatalf("revoked session authenticates: user=%v err=%v", u, err)
	}
}
