package agentauth

import "testing"

// TestCreateAgent_StoresOnlyTheHashNeverThePlaintext guards against a
// regression where the raw token, not HashToken's digest, ends up in
// credential_hash — which would silently defeat the entire point of
// hashing it.
func TestCreateAgent_StoresOnlyTheHashNeverThePlaintext(t *testing.T) {
	pool := testPool(t)
	agentID, token := insertTestAgent(t, pool, "test-agent-storage", 60)

	var storedHash string
	if err := pool.QueryRow(t.Context(), `SELECT credential_hash FROM agents WHERE id = $1::uuid`, agentID).Scan(&storedHash); err != nil {
		t.Fatalf("read stored credential_hash: %v", err)
	}

	if storedHash == token {
		t.Fatal("credential_hash must never equal the plaintext token")
	}
	if storedHash != HashToken(token) {
		t.Fatalf("credential_hash = %q, want HashToken(token) = %q", storedHash, HashToken(token))
	}
}

// TestCreateAgent_TwoAgentsGetDistinctCredentials guards against a
// regression that would reuse a token or hash across agents.
func TestCreateAgent_TwoAgentsGetDistinctCredentials(t *testing.T) {
	pool := testPool(t)
	idA, tokenA := insertTestAgent(t, pool, "test-agent-distinct-a", 60)
	idB, tokenB := insertTestAgent(t, pool, "test-agent-distinct-b", 60)

	if idA == idB {
		t.Fatal("expected two distinct agent ids")
	}
	if tokenA == tokenB {
		t.Fatal("expected two distinct plaintext tokens")
	}
}
