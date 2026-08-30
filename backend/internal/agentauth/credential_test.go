package agentauth

import "testing"

func TestGenerateToken_ProducesDistinctHighEntropyTokens(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		token, err := GenerateToken()
		if err != nil {
			t.Fatalf("generate token: %v", err)
		}
		if token == "" {
			t.Fatal("expected non-empty token")
		}
		if seen[token] {
			t.Fatalf("generated a duplicate token on iteration %d — RNG source is broken", i)
		}
		seen[token] = true
	}
}

func TestHashToken_DeterministicAndDistinct(t *testing.T) {
	tokenA, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	tokenB, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	firstHash := HashToken(tokenA)
	secondHash := HashToken(tokenA)
	if firstHash != secondHash {
		t.Fatal("HashToken must be deterministic for the same input")
	}
	if HashToken(tokenA) == HashToken(tokenB) {
		t.Fatal("HashToken produced the same digest for two different tokens")
	}
	if HashToken(tokenA) == tokenA {
		t.Fatal("HashToken must not return the plaintext token unchanged")
	}
}

func TestTokensEqual(t *testing.T) {
	a := HashToken("token-a")
	b := HashToken("token-b")

	if !tokensEqual(a, a) {
		t.Fatal("expected identical hashes to compare equal")
	}
	if tokensEqual(a, b) {
		t.Fatal("expected different hashes to compare unequal")
	}
}
