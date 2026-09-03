package authz

import "testing"

// testIterations keeps these tests cheap. The production work factor is
// verified separately by TestHashPasswordUsesConfiguredWorkFactor.
const testIterations = 1000

func TestVerifyPasswordRoundTrip(t *testing.T) {
	hash, err := hashPassword("correct-horse-battery", testIterations)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !VerifyPassword("correct-horse-battery", hash) {
		t.Error("the right password did not verify")
	}
	if VerifyPassword("correct-horse-batterz", hash) {
		t.Error("a wrong password verified")
	}
	if VerifyPassword("", hash) {
		t.Error("an empty password verified")
	}
}

// TestHashPasswordIsSalted: two hashes of the same password must differ, or a
// leaked table would show at a glance who shares a password.
func TestHashPasswordIsSalted(t *testing.T) {
	a, _ := hashPassword("correct-horse-battery", testIterations)
	b, _ := hashPassword("correct-horse-battery", testIterations)
	if a == b {
		t.Fatal("identical passwords produced identical hashes; salt is not applied")
	}
	if !VerifyPassword("correct-horse-battery", a) || !VerifyPassword("correct-horse-battery", b) {
		t.Error("both hashes must still verify")
	}
}

// TestVerifyPasswordRejectsMalformed: a corrupted or foreign hash must read as
// "wrong password", never as a match and never as a crash.
func TestVerifyPasswordRejectsMalformed(t *testing.T) {
	valid, _ := hashPassword("correct-horse-battery", testIterations)
	cases := map[string]string{
		"empty":            "",
		"not a hash":       "correct-horse-battery",
		"unknown scheme":   "bcrypt$10$abc$def",
		"missing field":    "pbkdf2-sha256$1000$c2FsdA",
		"bad iterations":   "pbkdf2-sha256$zero$c2FsdA$a2V5",
		"zero iterations":  "pbkdf2-sha256$0$c2FsdA$a2V5",
		"bad base64 salt":  "pbkdf2-sha256$1000$!!!$a2V5",
		"bad base64 key":   "pbkdf2-sha256$1000$c2FsdA$!!!",
		"truncated digest": valid[:len(valid)-4],
	}
	for name, stored := range cases {
		if VerifyPassword("correct-horse-battery", stored) {
			t.Errorf("%s: verified, want rejection", name)
		}
	}
}

// TestHashPasswordEnforcesLength. Length is the only composition rule; it is
// enforced at the one place that creates hashes so no endpoint can skip it.
func TestHashPasswordEnforcesLength(t *testing.T) {
	if _, err := HashPassword("short"); err != ErrPasswordTooShort {
		t.Errorf("got %v, want ErrPasswordTooShort", err)
	}
	// Counted in runes, not bytes: a short non-ASCII password must not pass by
	// virtue of its encoding.
	if _, err := HashPassword("парольчик"); err != ErrPasswordTooShort {
		t.Errorf("9-rune password: got %v, want ErrPasswordTooShort", err)
	}
	if _, err := HashPassword("длинный-пароль-достаточно"); err != nil {
		t.Errorf("long password rejected: %v", err)
	}
}

// TestHashPasswordUsesProductionWorkFactor pins the default: the stored format
// records its own parameters, so a regression that silently lowered them —
// including a test suite leaking its own SetPasswordIterations call into
// production code — would be invisible otherwise. This package's tests
// deliberately never lower the global, so the assertion is meaningful.
func TestHashPasswordUsesProductionWorkFactor(t *testing.T) {
	if DefaultPasswordIterations != 600_000 {
		t.Fatalf("default work factor = %d, want 600000", DefaultPasswordIterations)
	}
	hash, err := HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	want := "pbkdf2-sha256$600000$"
	if len(hash) < len(want) || hash[:len(want)] != want {
		t.Fatalf("hash prefix = %q, want %q", hash[:min(len(hash), len(want))], want)
	}
}

// TestVerifyPasswordAcceptsOtherWorkFactors: raising the iteration count must
// not lock out everyone hashed under the old one.
func TestVerifyPasswordAcceptsOtherWorkFactors(t *testing.T) {
	old, _ := hashPassword("correct-horse-battery", 500)
	if !VerifyPassword("correct-horse-battery", old) {
		t.Error("a hash stored at a different work factor must still verify")
	}
}

func TestSessionTokensAreUniqueAndHashed(t *testing.T) {
	a, err := NewSessionToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	b, _ := NewSessionToken()
	if a == b {
		t.Fatal("two session tokens collided")
	}
	if len(a) < 40 {
		t.Errorf("token is only %d chars; 32 random bytes should be longer", len(a))
	}
	// Hash the same token twice through separate calls: the point is that a
	// session presented later resolves to the stored row, so the two results
	// must be computed independently and then compared.
	first, second := HashSessionToken(a), HashSessionToken(a)
	if first != second {
		t.Error("token hashing is not deterministic; sessions would not resolve")
	}
	if HashSessionToken(a) == a {
		t.Error("the stored value equals the token; a database dump would hand over sessions")
	}
	if HashSessionToken(a) == HashSessionToken(b) {
		t.Error("distinct tokens hashed to the same value")
	}
}
