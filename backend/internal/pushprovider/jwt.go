package pushprovider

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// This file implements exactly the two JWT flavors the two real provider
// APIs require, and nothing else — no general-purpose JWT library, no new
// module dependency:
//
//   - ES256 (ECDSA P-256 + SHA-256), for APNs provider authentication
//     tokens (Apple: "Establishing a token-based connection to APNs").
//   - RS256 (RSA PKCS#1 v1.5 + SHA-256), for the Google service-account
//     JWT-bearer assertion that FCM's HTTP v1 API exchanges for an OAuth 2
//     access token (Google: "Using OAuth 2.0 for Server to Server
//     Applications").
//
// Both are ~30 lines each over crypto/ecdsa and crypto/rsa. Pulling in a
// JWT library to save that would add a dependency (and its own CVE surface)
// to a repo whose ADR-0006 predecessor made a point of adding none.

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// signingInput assembles base64url(header) + "." + base64url(claims).
func signingInput(header, claims any) (string, error) {
	h, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshal jwt header: %w", err)
	}
	c, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal jwt claims: %w", err)
	}
	return b64url(h) + "." + b64url(c), nil
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ,omitempty"`
	Kid string `json:"kid,omitempty"`
}

// apnsClaims is Apple's provider-token claim set: issuer (team id) and
// issued-at, and nothing else. Apple derives expiry from iat (tokens are
// valid for one hour and must not be regenerated more than once every 20
// minutes), so there is no exp claim to set.
type apnsClaims struct {
	Iss string `json:"iss"`
	Iat int64  `json:"iat"`
}

// signES256 produces an APNs provider authentication token. Apple issues the
// signing key as a PKCS#8 PEM (.p8) wrapping an ECDSA P-256 private key.
func signES256(pemKey []byte, keyID, teamID string, now time.Time) (string, error) {
	key, err := parseECPrivateKey(pemKey)
	if err != nil {
		return "", err
	}
	input, err := signingInput(
		jwtHeader{Alg: "ES256", Typ: "JWT", Kid: keyID},
		apnsClaims{Iss: teamID, Iat: now.Unix()},
	)
	if err != nil {
		return "", err
	}

	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign apns provider token: %w", err)
	}
	// JWS ES256 signatures are the fixed-width R||S concatenation (RFC 7518
	// §3.4), not ASN.1 — ecdsa.SignASN1 would produce a token Apple rejects.
	const coordBytes = 32
	sig := make([]byte, 2*coordBytes)
	padInto(sig[:coordBytes], r)
	padInto(sig[coordBytes:], s)

	return input + "." + b64url(sig), nil
}

func padInto(dst []byte, v *big.Int) {
	b := v.Bytes()
	copy(dst[len(dst)-len(b):], b)
}

func parseECPrivateKey(pemKey []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemKey)
	if block == nil {
		return nil, errors.New("apns signing key is not valid PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		// Fall back to SEC 1, the other shape an EC key is distributed in.
		if sec1, sec1Err := x509.ParseECPrivateKey(block.Bytes); sec1Err == nil {
			return sec1, nil
		}
		return nil, errors.New("apns signing key is not a valid PKCS#8 or SEC 1 EC private key")
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apns signing key is not an ECDSA key")
	}
	return key, nil
}

// googleAssertionClaims is Google's service-account JWT-bearer assertion
// claim set (RFC 7523 profile).
type googleAssertionClaims struct {
	Iss   string `json:"iss"`
	Scope string `json:"scope"`
	Aud   string `json:"aud"`
	Exp   int64  `json:"exp"`
	Iat   int64  `json:"iat"`
}

// signRS256Assertion produces the signed assertion FCM's token endpoint
// exchanges for an access token.
func signRS256Assertion(pemKey []byte, keyID, clientEmail, scope, audience string, now time.Time, ttl time.Duration) (string, error) {
	key, err := parseRSAPrivateKey(pemKey)
	if err != nil {
		return "", err
	}
	input, err := signingInput(
		jwtHeader{Alg: "RS256", Typ: "JWT", Kid: keyID},
		googleAssertionClaims{
			Iss:   clientEmail,
			Scope: scope,
			Aud:   audience,
			Exp:   now.Add(ttl).Unix(),
			Iat:   now.Unix(),
		},
	)
	if err != nil {
		return "", err
	}

	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign google assertion: %w", err)
	}
	return input + "." + b64url(sig), nil
}

func parseRSAPrivateKey(pemKey []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemKey)
	if block == nil {
		return nil, errors.New("fcm service-account private_key is not valid PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("fcm service-account private_key is not a valid PKCS#1 or PKCS#8 key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("fcm service-account private_key is not an RSA key")
	}
	return key, nil
}
