package operatorauth

import "testing"

func TestHashPassword_VerifyRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "correct-horse-battery-staple" {
		t.Fatal("password_hash must never equal the plaintext password")
	}
	if !VerifyPassword(hash, "correct-horse-battery-staple") {
		t.Fatal("expected the correct password to verify")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatal("expected an incorrect password to fail verification")
	}
}

func TestHashPassword_TwoHashesOfSameInputDiffer(t *testing.T) {
	hashA, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	hashB, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hashA == hashB {
		t.Fatal("expected bcrypt's per-hash random salt to make two hashes of the same password differ")
	}
	if !VerifyPassword(hashA, "same-password") || !VerifyPassword(hashB, "same-password") {
		t.Fatal("both distinct hashes should still verify the same original password")
	}
}
