package pushprovider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// providerDiscriminator is the one field every push credential must carry,
// on top of the provider's own native fields. An operator pastes the
// service-account JSON (FCM) or fills in the four token-auth values (APNs)
// and adds "provider": "fcm" | "apns" — one column, one blob, no per-
// provider schema in the database (ADR-0007).
type providerDiscriminator struct {
	Provider string `json:"provider"`
}

// ErrUnknownProvider is returned for a credential naming a provider this
// package does not implement. Callers surface it as a configuration error,
// never as a delivery failure to retry.
var ErrUnknownProvider = errors.New("push credential names an unknown provider")

// ClientFromCredentialJSON builds the right Client from one push channel's
// decrypted destination. The returned error is always safe to log: it
// describes the credential's *shape*, never its contents.
func ClientFromCredentialJSON(raw string, httpClient *http.Client) (Client, error) {
	var disc providerDiscriminator
	if err := json.Unmarshal([]byte(raw), &disc); err != nil {
		return nil, errors.New("push credential is not valid JSON")
	}

	switch disc.Provider {
	case "fcm":
		var cred FCMCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return nil, errors.New("push credential is not a valid FCM service-account object")
		}
		return NewFCMClient(cred, httpClient)
	case "apns":
		var cred APNsCredential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return nil, errors.New("push credential is not a valid APNs token-auth object")
		}
		return NewAPNsClient(cred, httpClient)
	case "":
		return nil, fmt.Errorf(`%w: "provider" is missing`, ErrUnknownProvider)
	default:
		// disc.Provider is attacker-influenced only by whoever can write
		// alert_channels (the operator), but it is still echoed back into a
		// log line, so it is bounded to the vocabulary this switch knows.
		return nil, fmt.Errorf(`%w: expected "fcm" or "apns"`, ErrUnknownProvider)
	}
}
