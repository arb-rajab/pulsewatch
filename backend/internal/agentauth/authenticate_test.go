package agentauth

import (
	"errors"
	"testing"
)

// TestAuthenticate_RealCredentialRoundTrip proves the real provisioning ->
// verification round trip against a real Postgres agents row: the exact
// token CreateAgent returned once authenticates successfully, and nothing
// else does.
func TestAuthenticate_RealCredentialRoundTrip(t *testing.T) {
	pool := testPool(t)
	agentID, token := insertTestAgent(t, pool, "test-agent-roundtrip", 60)

	identity, err := Authenticate(t.Context(), pool, token)
	if err != nil {
		t.Fatalf("expected successful authentication, got error: %v", err)
	}
	if identity.AgentID != agentID {
		t.Fatalf("expected identity.AgentID=%s, got %s", agentID, identity.AgentID)
	}
}

func TestAuthenticate_UnknownTokenRejected(t *testing.T) {
	pool := testPool(t)
	_, _ = insertTestAgent(t, pool, "test-agent-unknown-token", 60)

	_, err := Authenticate(t.Context(), pool, "this-token-was-never-issued")
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("expected ErrInvalidCredential for an unknown token, got %v", err)
	}
}

func TestAuthenticate_EmptyTokenRejected(t *testing.T) {
	pool := testPool(t)

	_, err := Authenticate(t.Context(), pool, "")
	if !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("expected ErrInvalidCredential for an empty token, got %v", err)
	}
}

// TestAuthenticate_DistinctAgentsDistinctCredentials proves one agent's
// token never authenticates as another agent — the structural property
// 05-api-contracts.md leans on for "no {agent_id} in /agent/assignments":
// identity comes only from the token itself.
func TestAuthenticate_DistinctAgentsDistinctCredentials(t *testing.T) {
	pool := testPool(t)
	agentA, tokenA := insertTestAgent(t, pool, "test-agent-a", 60)
	agentB, tokenB := insertTestAgent(t, pool, "test-agent-b", 60)

	identityA, err := Authenticate(t.Context(), pool, tokenA)
	if err != nil {
		t.Fatalf("authenticate agent A: %v", err)
	}
	identityB, err := Authenticate(t.Context(), pool, tokenB)
	if err != nil {
		t.Fatalf("authenticate agent B: %v", err)
	}

	if identityA.AgentID != agentA || identityB.AgentID != agentB {
		t.Fatalf("credentials resolved to the wrong agent: A->%s (want %s), B->%s (want %s)",
			identityA.AgentID, agentA, identityB.AgentID, agentB)
	}
	if identityA.AgentID == identityB.AgentID {
		t.Fatal("two distinct agents' tokens must never resolve to the same identity")
	}

	// tokenA must never authenticate as agent B, and vice versa.
	if wrong, err := Authenticate(t.Context(), pool, tokenA); err != nil || wrong.AgentID == agentB {
		t.Fatalf("agent A's token must resolve only to agent A, got %+v (err=%v)", wrong, err)
	}
}
