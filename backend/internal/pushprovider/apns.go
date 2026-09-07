package pushprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// APNs' provider API (https://developer.apple.com/documentation/
// usernotifications/sending-notification-requests-to-apns) is HTTP/2 only:
// POST {base}/3/device/{device_token} with an ES256 provider authentication
// token. Go's net/http negotiates HTTP/2 over TLS automatically, so this
// client needs no HTTP/2 library — but it does mean a plaintext HTTP base
// URL cannot speak the real protocol, which is why this package's own tests
// run against a TLS httptest server with HTTP/2 explicitly enabled rather
// than a plain one.
const (
	apnsProductionBaseURL = "https://api.push.apple.com"
	apnsSandboxBaseURL    = "https://api.sandbox.push.apple.com"
	// apnsTokenLifetime is deliberately well under Apple's one-hour
	// validity and well over its "no more than once every 20 minutes"
	// regeneration limit.
	apnsTokenLifetime = 40 * time.Minute
)

// APNsCredential is the token-based authentication material an operator
// configures as a "push" alert channel's destination: the .p8 signing key
// Apple issues, its key id, the team id, and the app's bundle id (the APNs
// topic).
type APNsCredential struct {
	TeamID     string `json:"team_id"`
	KeyID      string `json:"key_id"`
	PrivateKey string `json:"private_key"`
	// Topic is the app's bundle identifier, sent as the apns-topic header.
	Topic string `json:"topic"`
	// Environment is "production" (default) or "sandbox". Development
	// builds get tokens only the sandbox endpoint will accept — sending
	// them to production returns BadDeviceToken, which is exactly why this
	// is configuration and not a guess.
	Environment string `json:"environment"`
	// BaseURL overrides the environment-derived host. Same rationale as
	// FCMCredential.BaseURL.
	BaseURL string `json:"base_url"`
}

func (c APNsCredential) validate() error {
	var missing []string
	if c.TeamID == "" {
		missing = append(missing, "team_id")
	}
	if c.KeyID == "" {
		missing = append(missing, "key_id")
	}
	if c.PrivateKey == "" {
		missing = append(missing, "private_key")
	}
	if c.Topic == "" {
		missing = append(missing, "topic")
	}
	if len(missing) > 0 {
		return fmt.Errorf("apns credential is missing %s", strings.Join(missing, ", "))
	}
	if c.Environment != "" && c.Environment != "production" && c.Environment != "sandbox" {
		return errors.New(`apns environment must be "production" or "sandbox"`)
	}
	return nil
}

// APNsClient is a real APNs HTTP/2 sender. Safe for concurrent use.
type APNsClient struct {
	cred    APNsCredential
	http    *http.Client
	now     func() time.Time
	baseURL string

	mu       sync.Mutex
	jwt      string
	jwtIssue time.Time
}

// NewAPNsClient validates cred and returns a ready sender. httpClient may be
// nil; a plain &http.Client{} negotiates HTTP/2 with APNs over TLS-ALPN.
func NewAPNsClient(cred APNsCredential, httpClient *http.Client) (*APNsClient, error) {
	if err := cred.validate(); err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	base := cred.BaseURL
	if base == "" {
		base = apnsProductionBaseURL
		if cred.Environment == "sandbox" {
			base = apnsSandboxBaseURL
		}
	}
	if _, err := url.Parse(base); err != nil {
		return nil, errors.New("apns base_url is not a valid URL")
	}
	// Fail fast on an unusable signing key at construction time rather than
	// on the first real incident.
	if _, err := parseECPrivateKey([]byte(cred.PrivateKey)); err != nil {
		return nil, err
	}
	return &APNsClient{cred: cred, http: httpClient, now: time.Now, baseURL: strings.TrimSuffix(base, "/")}, nil
}

// Provider implements Client.
func (c *APNsClient) Provider() string { return "apns" }

// apnsPayload is the notification body. Apple reserves the "aps" key and
// treats every sibling key as the app's own custom data — which is where
// the deep-link fields go, so the mobile app reads them from exactly one
// place regardless of which provider delivered the notification.
type apnsPayload struct {
	APS apnsAPS `json:"aps"`
	// Data mirrors FCM's data map. Flattened into the top level by
	// MarshalJSON below.
	Data map[string]string `json:"-"`
}

type apnsAPS struct {
	Alert apnsAlert `json:"alert"`
	Sound string    `json:"sound,omitempty"`
	// MutableContent lets the app's notification service extension run,
	// which is what a future rich-notification feature would need; it is
	// harmless without one.
	MutableContent int `json:"mutable-content,omitempty"`
}

type apnsAlert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// MarshalJSON flattens Data alongside "aps", the shape Apple's payload
// format requires (custom keys are siblings of aps, not nested under it).
func (p apnsPayload) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(p.Data)+1)
	for k, v := range p.Data {
		if k == "aps" {
			// Structurally impossible to honour; refusing beats silently
			// producing a payload Apple rejects.
			return nil, errors.New(`apns payload data may not contain the reserved key "aps"`)
		}
		out[k] = v
	}
	out["aps"] = p.APS
	return json.Marshal(out)
}

// apnsErrorResponse is Apple's error body: a single "reason" string.
type apnsErrorResponse struct {
	Reason string `json:"reason"`
}

// Send performs one real APNs send. Like FCMClient.Send it does not retry.
func (c *APNsClient) Send(ctx context.Context, deviceToken string, n Notification) error {
	providerToken, err := c.providerToken()
	if err != nil {
		return err
	}

	body, err := json.Marshal(apnsPayload{
		APS:  apnsAPS{Alert: apnsAlert{Title: n.Title, Body: n.Body}, Sound: "default"},
		Data: n.Data,
	})
	if err != nil {
		return newSendError(KindPermanent, "encode apns payload: %v", err)
	}

	// A device token is hex; path-escaping it is belt-and-braces against a
	// malformed registration ever producing a request to a different path.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/3/device/"+url.PathEscape(deviceToken), bytes.NewReader(body))
	if err != nil {
		return newSendError(KindCredential, "apns base_url is not a valid request target")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "bearer "+providerToken)
	req.Header.Set("apns-topic", c.cred.Topic)
	req.Header.Set("apns-push-type", "alert")
	// Priority 10 is "deliver immediately" — correct for an outage alert,
	// and required for a notification with an alert payload.
	req.Header.Set("apns-priority", "10")
	// An outage notification that could not be delivered within an hour is
	// no longer worth waking a device for; the in-app incident list is the
	// durable record either way.
	req.Header.Set("apns-expiration", strconv.FormatInt(c.now().Add(time.Hour).Unix(), 10))
	if n.CollapseID != "" {
		req.Header.Set("apns-collapse-id", n.CollapseID)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctxErr(ctx)
		}
		return newSendError(KindTransient, "apns send transport error")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	var parsed apnsErrorResponse
	_ = json.Unmarshal(raw, &parsed)
	return classifyAPNs(resp.StatusCode, parsed.Reason)
}

// classifyAPNs maps Apple's published rejection reasons onto ErrorKind.
func classifyAPNs(status int, reason string) *SendError {
	switch reason {
	case "Unregistered":
		// 410. The app was uninstalled or the token is no longer valid for
		// this topic. Apple's own guidance is to stop sending to it.
		return newSendError(KindDeadToken, "apns rejected device token: Unregistered")
	case "BadDeviceToken":
		// The token is malformed, or belongs to the other environment
		// (a sandbox token sent to production). Either way this credential
		// will never deliver to it.
		return newSendError(KindDeadToken, "apns rejected device token: BadDeviceToken")
	case "DeviceTokenNotForTopic":
		return newSendError(KindDeadToken, "apns rejected device token: DeviceTokenNotForTopic")
	case "ExpiredProviderToken", "InvalidProviderToken", "MissingProviderToken":
		return newSendError(KindCredential, "apns rejected our provider token: %s", reason)
	case "BadCertificate", "BadCertificateEnvironment", "Forbidden":
		return newSendError(KindCredential, "apns rejected our credential: %s", reason)
	case "TooManyRequests", "TooManyProviderTokenUpdates", "ServiceUnavailable", "InternalServerError", "Shutdown":
		return newSendError(KindTransient, "apns temporarily unavailable: %s", reason)
	}

	switch {
	case status == http.StatusGone:
		// A 410 with an unrecognised reason still means "gone".
		return newSendError(KindDeadToken, "apns returned 410 for device token")
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return newSendError(KindCredential, "apns rejected our credential with status %d", status)
	case status == http.StatusTooManyRequests, status >= 500:
		return newSendError(KindTransient, "apns send returned status %d", status)
	case reason != "":
		return newSendError(KindPermanent, "apns send returned status %d (%s)", status, sanitizeReason(reason))
	default:
		return newSendError(KindPermanent, "apns send returned status %d", status)
	}
}

// sanitizeReason bounds an unrecognised, server-supplied reason string
// before it reaches a log line or alert_dispatches.last_error. Apple's
// reasons are short CamelCase identifiers; anything else is a server this
// client should not be quoting at length.
func sanitizeReason(reason string) string {
	const maxReasonLen = 64
	cleaned := make([]rune, 0, maxReasonLen)
	for _, r := range reason {
		if len(cleaned) == maxReasonLen {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			cleaned = append(cleaned, r)
		}
	}
	if len(cleaned) == 0 {
		return "unrecognised"
	}
	return string(cleaned)
}

// providerToken returns the cached ES256 provider token, re-signing it once
// it is older than apnsTokenLifetime. Apple rate-limits regeneration, so
// re-signing per request would itself be a failure mode.
func (c *APNsClient) providerToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.jwt != "" && now.Sub(c.jwtIssue) < apnsTokenLifetime {
		return c.jwt, nil
	}
	signed, err := signES256([]byte(c.cred.PrivateKey), c.cred.KeyID, c.cred.TeamID, now)
	if err != nil {
		return "", newSendError(KindCredential, "apns signing key unusable: %v", err)
	}
	c.jwt, c.jwtIssue = signed, now
	return signed, nil
}
