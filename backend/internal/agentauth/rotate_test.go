package agentauth

import "testing"

func TestRotateCredential_OldTokenStopsWorkingNewTokenWorks(t *testing.T) {
	pool := testPool(t)
	agentID, oldToken := insertTestAgent(t, pool, "test-rotate-agent", 60)

	rotated, err := RotateCredential(t.Context(), pool, agentID)
	if err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	if rotated.AgentID != agentID {
		t.Fatalf("expected agent id %s, got %s", agentID, rotated.AgentID)
	}
	if rotated.Token == oldToken {
		t.Fatal("expected a fresh token, got the same one back")
	}

	if _, err := Authenticate(t.Context(), pool, oldToken); err != ErrInvalidCredential {
		t.Fatalf("expected the old token to be rejected after rotation, got %v", err)
	}

	identity, err := Authenticate(t.Context(), pool, rotated.Token)
	if err != nil {
		t.Fatalf("expected the new token to authenticate, got %v", err)
	}
	if identity.AgentID != agentID {
		t.Fatalf("expected agent id %s, got %s", agentID, identity.AgentID)
	}
}

func TestRotateCredential_UnknownAgentReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	_, err := RotateCredential(t.Context(), pool, "00000000-0000-0000-0000-000000000000")
	if err != ErrAgentNotFound {
		t.Fatalf("expected ErrAgentNotFound, got %v", err)
	}
}
