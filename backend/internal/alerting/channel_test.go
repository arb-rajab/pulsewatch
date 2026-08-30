package alerting

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// testEncryptionKey is a fixed, non-secret 32-byte AES-256 key (sequential
// bytes, not randomly generated) used by every test in this session that
// needs to round-trip a real encrypted alert_channels.destination_encrypted
// value against the shared test Postgres instance. Deliberately fixed and
// shared (mirrored verbatim in scheduler's own test helpers) rather than
// generated per-test: LoadChannels decrypts every row in the global
// alert_channels table, so two test processes using different random keys
// against the same shared database would spuriously fail to decrypt each
// other's leftover rows. A single constant key means any row any test in
// this session ever writes stays decryptable by any other.
var testEncryptionKey = []byte{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
	16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
}

func TestEncryptDecryptDestination_RoundTrip(t *testing.T) {
	const plaintext = "https://hooks.example.invalid/T00/B00/a-fake-webhook-token"

	encrypted, err := EncryptDestination(plaintext, testEncryptionKey)
	if err != nil {
		t.Fatalf("EncryptDestination: %v", err)
	}
	if encrypted == plaintext {
		t.Fatal("expected ciphertext to differ from plaintext")
	}

	decrypted, err := decryptDestination(encrypted, testEncryptionKey)
	if err != nil {
		t.Fatalf("decryptDestination: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("round-trip mismatch: got %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptDestination_NonDeterministic(t *testing.T) {
	// Each call must use a fresh random nonce (crypto/rand) — two
	// encryptions of the same plaintext must not produce identical
	// ciphertext, which would leak equality information about destinations.
	const plaintext = "same-destination-both-times"

	a, err := EncryptDestination(plaintext, testEncryptionKey)
	if err != nil {
		t.Fatalf("EncryptDestination (a): %v", err)
	}
	b, err := EncryptDestination(plaintext, testEncryptionKey)
	if err != nil {
		t.Fatalf("EncryptDestination (b): %v", err)
	}
	if a == b {
		t.Fatal("expected two encryptions of the same plaintext to differ (fresh nonce per call)")
	}
}

func TestDecryptDestination_WrongKeyFails(t *testing.T) {
	wrongKey := make([]byte, 32)
	copy(wrongKey, testEncryptionKey)
	wrongKey[0] ^= 0xFF // flip a bit: a different key, still valid length

	encrypted, err := EncryptDestination("secret-destination", testEncryptionKey)
	if err != nil {
		t.Fatalf("EncryptDestination: %v", err)
	}
	if _, err := decryptDestination(encrypted, wrongKey); err == nil {
		t.Fatal("expected decryption with the wrong key to fail")
	}
}

func TestEncryptionKeyFromEnv(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv("ALERT_CHANNEL_ENCRYPTION_KEY", "")
		if _, err := EncryptionKeyFromEnv(); err == nil {
			t.Fatal("expected an error when ALERT_CHANNEL_ENCRYPTION_KEY is unset")
		}
	})

	t.Run("not valid base64", func(t *testing.T) {
		t.Setenv("ALERT_CHANNEL_ENCRYPTION_KEY", "not-valid-base64!!!")
		if _, err := EncryptionKeyFromEnv(); err == nil {
			t.Fatal("expected an error for a value that is not valid base64")
		}
	})

	t.Run("wrong decoded length", func(t *testing.T) {
		t.Setenv("ALERT_CHANNEL_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString([]byte("too-short")))
		if _, err := EncryptionKeyFromEnv(); err == nil {
			t.Fatal("expected an error for a key that doesn't decode to exactly 32 bytes")
		}
	})

	t.Run("valid", func(t *testing.T) {
		t.Setenv("ALERT_CHANNEL_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(testEncryptionKey))
		got, err := EncryptionKeyFromEnv()
		if err != nil {
			t.Fatalf("EncryptionKeyFromEnv: %v", err)
		}
		if !bytes.Equal(got, testEncryptionKey) {
			t.Fatalf("expected the decoded key to round-trip, got %x want %x", got, testEncryptionKey)
		}
	})
}
