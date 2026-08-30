package operatorauth

import "testing"

func TestCreateOperator_StoresOnlyTheHashNeverThePlaintext(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperator(t, pool, "test-storage@example.invalid", "a-real-password")

	var storedHash string
	if err := pool.QueryRow(t.Context(), `SELECT password_hash FROM operators WHERE id = $1::uuid`, operatorID).Scan(&storedHash); err != nil {
		t.Fatalf("read stored password_hash: %v", err)
	}
	if storedHash == "a-real-password" {
		t.Fatal("password_hash must never equal the plaintext password")
	}
	if !VerifyPassword(storedHash, "a-real-password") {
		t.Fatal("stored hash should verify against the original password")
	}
}

func TestCountOperators_ReflectsRealRows(t *testing.T) {
	pool := testPool(t)

	before, err := CountOperators(t.Context(), pool)
	if err != nil {
		t.Fatalf("CountOperators: %v", err)
	}

	insertTestOperator(t, pool, "test-count@example.invalid", "a-real-password")

	after, err := CountOperators(t.Context(), pool)
	if err != nil {
		t.Fatalf("CountOperators: %v", err)
	}
	if after != before+1 {
		t.Fatalf("expected count to increase by exactly 1, got before=%d after=%d", before, after)
	}
}
