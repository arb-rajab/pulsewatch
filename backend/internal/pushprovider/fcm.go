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
	"strings"
	"sync"
	"time"
)

// FCM HTTP v1 (https://firebase.google.com/docs/reference/fcm/rest/v1/
// projects.messages/send) is the current, non-deprecated Firebase send API:
// POST {base}/v1/projects/{project_id}/messages:send with an OAuth 2 bearer
// token minted from the project's service-account key. The legacy
// /fcm/send server-key API is deliberately not implemented — it was turned
// down by Google in 2024, so building against it would be building against
// something that cannot work.
const (
	fcmDefaultBaseURL  = "https://fcm.googleapis.com"
	fcmDefaultTokenURL = "https://oauth2.googleapis.com/token"
	fcmScope           = "https://www.googleapis.com/auth/firebase.messaging"
	fcmAssertionTTL    = time.Hour
	// accessTokenSkew re-mints the access token slightly before it actually
	// expires, so a token that is valid when we check it can't expire in
	// flight between the check and the send.
	accessTokenSkew = 60 * time.Second
)

// FCMCredential is the Firebase service-account key an operator configures
// as a "push" alert channel's destination, plus this repo's two optional
// test/staging overrides. The five required fields are named exactly as
// they appear in the JSON file the Firebase console downloads, so an
// operator pastes that file through unmodified apart from adding
// "provider": "fcm".
type FCMCredential struct {
	ProjectID    string `json:"project_id"`
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	// TokenURI is the service-account file's own token_uri; defaults to
	// Google's production token endpoint when absent.
	TokenURI string `json:"token_uri"`
	// BaseURL overrides https://fcm.googleapis.com. Its only production use
	// is a proxy an operator routes egress through; its test use is
	// pointing this client at a local server that speaks the real FCM
	// protocol (see fcm_test.go).
	BaseURL string `json:"base_url"`
}

func (c FCMCredential) validate() error {
	var missing []string
	if c.ProjectID == "" {
		missing = append(missing, "project_id")
	}
	if c.ClientEmail == "" {
		missing = append(missing, "client_email")
	}
	if c.PrivateKey == "" {
		missing = append(missing, "private_key")
	}
	if len(missing) > 0 {
		return fmt.Errorf("fcm credential is missing %s", strings.Join(missing, ", "))
	}
	return nil
}

// FCMClient is a real FCM HTTP v1 sender. Safe for concurrent use: the
// cached access token is mutex-guarded, and everything else is immutable
// after construction.
type FCMClient struct {
	cred    FCMCredential
	http    *http.Client
	now     func() time.Time
	sendURL string

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

// NewFCMClient validates cred and returns a ready sender. httpClient may be
// nil (a plain &http.Client{} is used; per-attempt deadlines come from the
// caller's context, never from Client.Timeout, so one shared client can't
// impose a timeout tuned for something else).
func NewFCMClient(cred FCMCredential, httpClient *http.Client) (*FCMClient, error) {
	if err := cred.validate(); err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	base := cred.BaseURL
	if base == "" {
		base = fcmDefaultBaseURL
	}
	if _, err := url.Parse(base); err != nil {
		return nil, errors.New("fcm base_url is not a valid URL")
	}
	return &FCMClient{
		cred:    cred,
		http:    httpClient,
		now:     time.Now,
		sendURL: strings.TrimSuffix(base, "/") + "/v1/projects/" + url.PathEscape(cred.ProjectID) + "/messages:send",
	}, nil
}

// Provider implements Client.
func (c *FCMClient) Provider() string { return "fcm" }

// fcmMessage is the v1 Message resource. Only the fields this repo actually
// needs are modeled — a partial struct is the point, not an omission: FCM
// ignores nothing it is not sent, and inventing a wider notification schema
// than FR-013 asks for is exactly what ADR-0006 declined to do for webhooks.
type fcmMessage struct {
	Message struct {
		Token        string            `json:"token"`
		Notification fcmNotification   `json:"notification"`
		Data         map[string]string `json:"data,omitempty"`
		Android      *fcmAndroidConfig `json:"android,omitempty"`
	} `json:"message"`
}

type fcmNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type fcmAndroidConfig struct {
	Priority     string `json:"priority"`
	CollapseKey  string `json:"collapse_key,omitempty"`
	Notification *struct {
		ClickAction string `json:"click_action,omitempty"`
	} `json:"notification,omitempty"`
}

// fcmErrorResponse is the google.rpc.Status envelope FCM returns on every
// failure, including the FcmError detail carrying the specific errorCode
// this client classifies on.
type fcmErrorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Status  string `json:"status"`
		Details []struct {
			Type      string `json:"@type"`
			ErrorCode string `json:"errorCode"`
		} `json:"details"`
	} `json:"error"`
}

func (r fcmErrorResponse) errorCode() string {
	for _, d := range r.Error.Details {
		if d.ErrorCode != "" {
			return d.ErrorCode
		}
	}
	return ""
}

// Send performs one real FCM HTTP v1 send. It does not retry — retry policy
// (and the backoff between attempts) belongs to the caller, which is the
// only layer that knows how many other device tokens are waiting behind
// this one.
func (c *FCMClient) Send(ctx context.Context, deviceToken string, n Notification) error {
	accessToken, err := c.accessTokenFor(ctx)
	if err != nil {
		return err
	}

	var msg fcmMessage
	msg.Message.Token = deviceToken
	msg.Message.Notification = fcmNotification{Title: n.Title, Body: n.Body}
	msg.Message.Data = n.Data
	msg.Message.Android = &fcmAndroidConfig{Priority: "high", CollapseKey: n.CollapseID}

	body, err := json.Marshal(msg)
	if err != nil {
		return newSendError(KindPermanent, "encode fcm message: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.sendURL, bytes.NewReader(body))
	if err != nil {
		return newSendError(KindCredential, "fcm send URL is not a valid request target")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctxErr(ctx)
		}
		return newSendError(KindTransient, "fcm send transport error")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	// Bounded read: an error body is small, and an unbounded read of a
	// misbehaving endpoint's response is a denial-of-service foot-gun.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	var parsed fcmErrorResponse
	_ = json.Unmarshal(raw, &parsed)
	return classifyFCM(resp.StatusCode, parsed.errorCode())
}

// classifyFCM maps FCM's published error codes onto ErrorKind. The mapping
// is the whole point of this file: "UNREGISTERED" and "UNAVAILABLE" are both
// just failures at the HTTP level and must lead to opposite actions.
func classifyFCM(status int, errorCode string) *SendError {
	switch errorCode {
	case "UNREGISTERED":
		// The app was uninstalled, or the token was rotated/invalidated by
		// the device. Permanently dead.
		return newSendError(KindDeadToken, "fcm rejected device token: UNREGISTERED")
	case "INVALID_ARGUMENT":
		// FCM returns this for a malformed registration token. Every other
		// field of the message this client sends is fixed and covered by
		// this package's own tests, so the token is the only variable a
		// caller could have got wrong — and a byte-for-byte retry of an
		// invalid token can never start succeeding.
		return newSendError(KindDeadToken, "fcm rejected device token: INVALID_ARGUMENT")
	case "SENDER_ID_MISMATCH":
		// The token is real but was issued for a different Firebase
		// project. It will never be deliverable with this credential.
		return newSendError(KindDeadToken, "fcm rejected device token: SENDER_ID_MISMATCH")
	case "QUOTA_EXCEEDED":
		return newSendError(KindTransient, "fcm quota exceeded (QUOTA_EXCEEDED)")
	case "UNAVAILABLE":
		return newSendError(KindTransient, "fcm temporarily unavailable (UNAVAILABLE)")
	case "INTERNAL":
		return newSendError(KindTransient, "fcm internal error (INTERNAL)")
	case "THIRD_PARTY_AUTH_ERROR":
		// FCM could not authenticate to APNs on our behalf: an expired
		// APNs key configured in the Firebase project. Our configuration,
		// not the device's.
		return newSendError(KindCredential, "fcm could not authenticate to APNs (THIRD_PARTY_AUTH_ERROR)")
	}

	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return newSendError(KindCredential, "fcm rejected our credential with status %d", status)
	case status == http.StatusTooManyRequests, status >= 500:
		return newSendError(KindTransient, "fcm send returned status %d", status)
	default:
		return newSendError(KindPermanent, "fcm send returned status %d", status)
	}
}

// accessTokenFor returns a cached-or-freshly-minted OAuth 2 access token.
func (c *FCMClient) accessTokenFor(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.accessToken != "" && now.Add(accessTokenSkew).Before(c.expiresAt) {
		return c.accessToken, nil
	}

	tokenURL := c.cred.TokenURI
	if tokenURL == "" {
		tokenURL = fcmDefaultTokenURL
	}

	assertion, err := signRS256Assertion(
		[]byte(c.cred.PrivateKey), c.cred.PrivateKeyID, c.cred.ClientEmail,
		fcmScope, tokenURL, now, fcmAssertionTTL,
	)
	if err != nil {
		// The message is this package's own text about the credential's
		// shape; it never contains the key material itself.
		return "", newSendError(KindCredential, "fcm service-account key unusable: %v", err)
	}

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", newSendError(KindCredential, "fcm token_uri is not a valid request target")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctxErr(ctx)
		}
		return "", newSendError(KindTransient, "fcm token endpoint transport error")
	}
	defer func() { _ = resp.Body.Close() }()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if readErr != nil {
		return "", newSendError(KindTransient, "fcm token endpoint response could not be read")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A 4xx here is a rejected assertion — our credential, not the
		// device's token. A 5xx is worth another attempt.
		if resp.StatusCode >= 500 {
			return "", newSendError(KindTransient, "fcm token endpoint returned status %d", resp.StatusCode)
		}
		return "", newSendError(KindCredential, "fcm rejected our service-account assertion with status %d", resp.StatusCode)
	}

	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &token); err != nil || token.AccessToken == "" {
		return "", newSendError(KindTransient, "fcm token endpoint returned an unreadable access token")
	}

	expiresIn := token.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	c.accessToken = token.AccessToken
	c.expiresAt = now.Add(time.Duration(expiresIn) * time.Second)
	return c.accessToken, nil
}
