package operatorauth

import (
	"strings"
	"testing"
	"time"
)

func testSecret() []byte {
	return []byte("this-is-a-fixed-32-byte-test-key")
}

func TestIssueAndVerifySession_RealRoundTrip(t *testing.T) {
	secret := testSecret()
	now := time.Now()
	token, expiresAt, err := IssueSession(secret, "operator-123", now)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	if !expiresAt.After(now) {
		t.Fatalf("expected expiresAt %v to be after now %v", expiresAt, now)
	}

	identity, err := VerifySession(secret, token, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("VerifySession: %v", err)
	}
	if identity.OperatorID != "operator-123" {
		t.Fatalf("expected operator-123, got %q", identity.OperatorID)
	}
}

func TestVerifySession_ExpiredTokenRejected(t *testing.T) {
	secret := testSecret()
	now := time.Now()
	token, _, err := IssueSession(secret, "operator-123", now)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	_, err = VerifySession(secret, token, now.Add(SessionTTL+time.Second))
	if err != ErrInvalidSession {
		t.Fatalf("expected ErrInvalidSession for an expired token, got %v", err)
	}
}

func TestVerifySession_TamperedPayloadRejected(t *testing.T) {
	secret := testSecret()
	now := time.Now()
	token, _, err := IssueSession(secret, "operator-123", now)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	// Forge a token for a different operator by re-issuing with the
	// legitimate secret, then splicing the forged payload onto the
	// original signature — proves the signature actually binds to this
	// exact payload, not just "some payload this secret once signed".
	forged, _, err := IssueSession(secret, "operator-attacker", now)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	forgedPayload := strings.SplitN(forged, ".", 2)[0]
	originalSig := strings.SplitN(token, ".", 2)[1]
	spliced := forgedPayload + "." + originalSig

	if _, err := VerifySession(secret, spliced, now); err != ErrInvalidSession {
		t.Fatalf("expected ErrInvalidSession for a spliced token, got %v", err)
	}
}

func TestVerifySession_WrongSecretRejected(t *testing.T) {
	now := time.Now()
	token, _, err := IssueSession(testSecret(), "operator-123", now)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	otherSecret := []byte("a-completely-different-32-byte-k")
	if _, err := VerifySession(otherSecret, token, now); err != ErrInvalidSession {
		t.Fatalf("expected ErrInvalidSession under a different signing secret, got %v", err)
	}
}

func TestVerifySession_MalformedTokenRejected(t *testing.T) {
	secret := testSecret()
	now := time.Now()
	cases := []string{"", "not-a-token", "onlyonepart", "a.b.c", "!!!.???"}
	for _, tok := range cases {
		if _, err := VerifySession(secret, tok, now); err != ErrInvalidSession {
			t.Fatalf("token %q: expected ErrInvalidSession, got %v", tok, err)
		}
	}
}

func TestSigningSecretFromEnv_RequiresMinimumLength(t *testing.T) {
	t.Setenv("SESSION_SIGNING_SECRET", "dG9vc2hvcnQ=") // base64("tooshort"), 8 bytes
	if _, err := SigningSecretFromEnv(); err == nil {
		t.Fatal("expected an error for a too-short signing secret")
	}
}

func TestSigningSecretFromEnv_RequiresPresence(t *testing.T) {
	t.Setenv("SESSION_SIGNING_SECRET", "")
	if _, err := SigningSecretFromEnv(); err == nil {
		t.Fatal("expected an error when SESSION_SIGNING_SECRET is unset")
	}
}
