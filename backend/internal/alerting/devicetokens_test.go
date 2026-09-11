package alerting

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestRegisterDeviceToken_UpsertResurrectsADeadToken is the counterpart to
// dead-token marking, and the reason marking a token dead is safe at all:
// a device that re-registers is, by direct evidence, live again. Without
// this, one UNREGISTERED response — which FCM also returns for a token
// that was merely refreshed — would mute an operator's phone permanently.
func TestRegisterDeviceToken_UpsertResurrectsADeadToken(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperatorRow(t, pool)
	token := "fcm-token-resurrect-" + randomSuffix(t)

	first, err := RegisterDeviceToken(t.Context(), pool, operatorID, "fcm", "android", token)
	if err != nil {
		t.Fatalf("RegisterDeviceToken: %v", err)
	}
	if err := markDeviceTokenDead(t.Context(), pool, first.ID, "fcm rejected device token: UNREGISTERED"); err != nil {
		t.Fatalf("markDeviceTokenDead: %v", err)
	}

	live, err := loadLiveDeviceTokens(t.Context(), pool, "fcm")
	if err != nil {
		t.Fatalf("loadLiveDeviceTokens: %v", err)
	}
	if containsTokenID(live, first.ID) {
		t.Fatal("expected a dead token to be excluded from the fan-out set")
	}

	second, err := RegisterDeviceToken(t.Context(), pool, operatorID, "fcm", "android", token)
	if err != nil {
		t.Fatalf("re-RegisterDeviceToken: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected the same row to be updated (upsert on provider+token), got %s then %s", first.ID, second.ID)
	}
	if second.DeadAt != nil || second.DeadReason != nil {
		t.Fatal("expected re-registration to clear dead_at/dead_reason")
	}
	if !second.LastRegisteredAt.After(first.LastRegisteredAt) && !second.LastRegisteredAt.Equal(first.LastRegisteredAt) {
		t.Fatal("expected last_registered_at to advance on re-registration")
	}

	live, err = loadLiveDeviceTokens(t.Context(), pool, "fcm")
	if err != nil {
		t.Fatalf("loadLiveDeviceTokens: %v", err)
	}
	if !containsTokenID(live, first.ID) {
		t.Fatal("expected a re-registered token to be back in the fan-out set")
	}
}

// TestRevokeDeviceToken_RemovesFromFanOutAndIsScopedToItsOperator proves the
// sign-out path, and that one operator can never revoke another's device
// even though v1 has exactly one operator (02-requirements.md) — the check
// is structural, not a consequence of there being nobody else.
func TestRevokeDeviceToken_RemovesFromFanOutAndIsScopedToItsOperator(t *testing.T) {
	pool := testPool(t)
	owner := insertTestOperatorRow(t, pool)
	stranger := insertTestOperatorRow(t, pool)

	record, err := RegisterDeviceToken(t.Context(), pool, owner, "apns", "ios", "apns-token-revoke-"+randomSuffix(t))
	if err != nil {
		t.Fatalf("RegisterDeviceToken: %v", err)
	}

	if err := RevokeDeviceToken(t.Context(), pool, stranger, record.ID); !errors.Is(err, ErrDeviceTokenNotFound) {
		t.Fatalf("expected another operator's revoke to be not-found, got %v", err)
	}

	if err := RevokeDeviceToken(t.Context(), pool, owner, record.ID); err != nil {
		t.Fatalf("RevokeDeviceToken: %v", err)
	}
	live, err := loadLiveDeviceTokens(t.Context(), pool, "apns")
	if err != nil {
		t.Fatalf("loadLiveDeviceTokens: %v", err)
	}
	if containsTokenID(live, record.ID) {
		t.Fatal("expected a revoked token to leave the fan-out set")
	}

	// Revoking twice is not-found, not a second success: the row is already
	// revoked and there is nothing further to do.
	if err := RevokeDeviceToken(t.Context(), pool, owner, record.ID); !errors.Is(err, ErrDeviceTokenNotFound) {
		t.Fatalf("expected a second revoke to be not-found, got %v", err)
	}
}

// TestLoadLiveDeviceTokens_IsScopedToItsProvider proves an APNs credential
// is never handed an FCM token (which it would reject as BadDeviceToken and
// then mark dead — silently killing a perfectly good registration).
func TestLoadLiveDeviceTokens_IsScopedToItsProvider(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperatorRow(t, pool)

	fcmRecord, err := RegisterDeviceToken(t.Context(), pool, operatorID, "fcm", "android", "fcm-token-scope-"+randomSuffix(t))
	if err != nil {
		t.Fatalf("RegisterDeviceToken(fcm): %v", err)
	}
	apnsRecord, err := RegisterDeviceToken(t.Context(), pool, operatorID, "apns", "ios", "apns-token-scope-"+randomSuffix(t))
	if err != nil {
		t.Fatalf("RegisterDeviceToken(apns): %v", err)
	}

	fcmLive, err := loadLiveDeviceTokens(t.Context(), pool, "fcm")
	if err != nil {
		t.Fatalf("loadLiveDeviceTokens(fcm): %v", err)
	}
	if !containsTokenID(fcmLive, fcmRecord.ID) || containsTokenID(fcmLive, apnsRecord.ID) {
		t.Fatal("expected the fcm fan-out set to contain only fcm tokens")
	}

	apnsLive, err := loadLiveDeviceTokens(t.Context(), pool, "apns")
	if err != nil {
		t.Fatalf("loadLiveDeviceTokens(apns): %v", err)
	}
	if !containsTokenID(apnsLive, apnsRecord.ID) || containsTokenID(apnsLive, fcmRecord.ID) {
		t.Fatal("expected the apns fan-out set to contain only apns tokens")
	}
}

// TestRegisterDeviceToken_RejectsInvalidRegistrations proves validation
// happens in Go, so a bad registration is a useful 422 rather than a raw
// constraint-violation 503 — while the schema's own CHECK constraints stay
// as the backstop.
func TestRegisterDeviceToken_RejectsInvalidRegistrations(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperatorRow(t, pool)

	cases := []struct {
		name               string
		provider, platform string
		token              string
	}{
		{"unknown provider", "sms", "android", "t"},
		{"unknown platform", "fcm", "windows-phone", "t"},
		{"empty token", "fcm", "android", "   "},
		{"absurdly long token", "fcm", "android", strings1025()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RegisterDeviceToken(t.Context(), pool, operatorID, tc.provider, tc.platform, tc.token)
			if !errors.Is(err, ErrInvalidDeviceToken) {
				t.Fatalf("expected ErrInvalidDeviceToken, got %v", err)
			}
		})
	}
}

// TestListDeviceTokens_ShowsDeadReasonAndNeverTheTokenItself proves the
// operator-visible view an operator debugging "why didn't my phone buzz"
// actually needs, and its one structural guarantee.
func TestListDeviceTokens_ShowsDeadReasonAndNeverTheTokenItself(t *testing.T) {
	pool := testPool(t)
	operatorID := insertTestOperatorRow(t, pool)
	token := "fcm-token-listed-" + randomSuffix(t)

	record, err := RegisterDeviceToken(t.Context(), pool, operatorID, "fcm", "android", token)
	if err != nil {
		t.Fatalf("RegisterDeviceToken: %v", err)
	}
	const reason = "fcm rejected device token: UNREGISTERED"
	if err := markDeviceTokenDead(t.Context(), pool, record.ID, reason); err != nil {
		t.Fatalf("markDeviceTokenDead: %v", err)
	}

	records, err := ListDeviceTokens(t.Context(), pool, operatorID)
	if err != nil {
		t.Fatalf("ListDeviceTokens: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly this operator's one registration, got %d", len(records))
	}
	got := records[0]
	if got.DeadAt == nil || got.DeadReason == nil || *got.DeadReason != reason {
		t.Fatalf("expected the dead reason to be visible to the operator, got %+v", got)
	}
	// DeviceTokenRecord has no field capable of carrying the token value —
	// this asserts the property the type's shape already guarantees, so a
	// future field addition has to break a test to break the guarantee.
	if containsString(marshalRecord(t, got), token) {
		t.Fatal("FR-023-style: a device token must never be readable back through the API")
	}
}

func containsTokenID(tokens []liveDeviceToken, id string) bool {
	for _, tok := range tokens {
		if tok.id == id {
			return true
		}
	}
	return false
}

func strings1025() string {
	out := make([]byte, maxDeviceTokenLength+1)
	for i := range out {
		out[i] = 'a'
	}
	return string(out)
}

func marshalRecord(t *testing.T, record DeviceTokenRecord) string {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal DeviceTokenRecord: %v", err)
	}
	return string(encoded)
}

func containsString(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
