package pushprovider

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"sync"
	"testing"
)

// Real keys, generated once per test binary. Nothing here is a fixture
// pasted from somewhere — every signature these tests verify is produced by
// the same crypto/ecdsa and crypto/rsa code paths production uses, and
// verified against the matching public key, so a change that produced a
// structurally-valid-but-unverifiable JWT (the classic ES256 ASN.1-vs-R||S
// mistake) fails these tests rather than only failing at Apple.

var (
	keysOnce sync.Once
	rsaKey   *rsa.PrivateKey
	ecKey    *ecdsa.PrivateKey
)

func testKeys(t *testing.T) (*rsa.PrivateKey, *ecdsa.PrivateKey) {
	t.Helper()
	keysOnce.Do(func() {
		var err error
		rsaKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		ecKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			panic(err)
		}
	})
	return rsaKey, ecKey
}

func pkcs8PEM(t *testing.T, key any) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal PKCS#8: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// decodeJWT splits a compact JWS and returns its header, claims, signing
// input and raw signature.
func decodeJWT(t *testing.T, token string) (header, claims map[string]any, signingInput string, sig []byte) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected a 3-part compact JWS, got %d parts", len(parts))
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode jwt header: %v", err)
	}
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode jwt claims: %v", err)
	}
	sig, err = base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode jwt signature: %v", err)
	}
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		t.Fatalf("unmarshal jwt header: %v", err)
	}
	if err := json.Unmarshal(claimsRaw, &claims); err != nil {
		t.Fatalf("unmarshal jwt claims: %v", err)
	}
	return header, claims, parts[0] + "." + parts[1], sig
}

func verifyES256(t *testing.T, pub *ecdsa.PublicKey, signingInput string, sig []byte) {
	t.Helper()
	if len(sig) != 64 {
		t.Fatalf("ES256 signature must be the 64-byte R||S concatenation (RFC 7518 §3.4), got %d bytes — an ASN.1 signature here is exactly what APNs rejects", len(sig))
	}
	digest := sha256.Sum256([]byte(signingInput))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		t.Fatal("ES256 signature does not verify against the signing key's public key")
	}
}

func verifyRS256(t *testing.T, pub *rsa.PublicKey, signingInput string, sig []byte) {
	t.Helper()
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("RS256 signature does not verify: %v", err)
	}
}
