// Package pushprovider implements real mobile-push provider clients for
// ADR-0007: Firebase Cloud Messaging's HTTP v1 API and Apple Push
// Notification service's HTTP/2 API, spoken directly over net/http with no
// vendor SDK and no new module dependency (the same "no new dependency"
// property ADR-0006's WebhookDispatcher has).
//
// This package deliberately knows nothing about pulsewatch's incidents,
// channels, or database. It takes a credential, a device token, and a
// Notification, and reports back a classified outcome — which is the whole
// input alerting.PushDispatcher needs to decide "retry", "give up", or
// "this token is dead, stop paying for it".
//
// Sanitization discipline (FR-023, inherited from ADR-0006): no error this
// package returns ever embeds a device token, a provider credential, a
// signed JWT, or a raw net/http error string (which for a bad URL can carry
// the URL itself). Every SendError.Reason is assembled from a fixed
// vocabulary plus a provider-published error code or HTTP status.
package pushprovider

import (
	"context"
	"errors"
	"fmt"
)

// Notification is the provider-neutral shape of one incident notification.
// Data is delivered as the provider's own data/custom payload and is what
// the mobile app deep-links on (ADR-0007): incident_id, target_id, kind.
type Notification struct {
	Title string
	Body  string
	Data  map[string]string
	// CollapseID lets a provider replace a previous, still-undelivered
	// notification for the same incident rather than stacking a second one
	// on the device — the closest thing either provider offers to the
	// "exactly one visible alert per edge transition" property ADR-0002
	// already guarantees on the database side.
	CollapseID string
}

// ErrorKind classifies a failed send into the three decisions a caller can
// actually act on. This is the whole reason this package exists as a
// separate layer: "the token is dead" and "the provider is briefly
// unavailable" look nearly identical at the HTTP level and must not be
// treated alike (ADR-0007).
type ErrorKind string

const (
	// KindTransient — another attempt could plausibly succeed (5xx, 429,
	// network/transport failure, timeout). Retry with backoff.
	KindTransient ErrorKind = "transient"
	// KindDeadToken — the provider says this specific device token is
	// unregistered, invalid, or belongs to a different sender. Never
	// retryable, and the token must be marked dead so no future fan-out
	// pays for it again.
	KindDeadToken ErrorKind = "dead_token"
	// KindCredential — our own provider credential is missing, malformed,
	// expired, or rejected. Never retryable, and never the device's fault:
	// every other token in the same fan-out will fail identically, so the
	// caller should stop immediately rather than repeat the failure once
	// per registered device.
	KindCredential ErrorKind = "credential"
	// KindPermanent — the request was rejected for a reason that is
	// neither the token's nor the credential's (an unexpected 4xx, a
	// payload the provider refused). Not retryable; the token stays live.
	KindPermanent ErrorKind = "permanent"
)

// SendError is the only error type this package's Send methods return.
type SendError struct {
	Kind ErrorKind
	// Reason is safe to log and to persist verbatim into
	// device_tokens.dead_reason / alert_dispatches.last_error: it is built
	// from this package's own fixed vocabulary plus a provider error code
	// or HTTP status, never from a token, a credential, or an underlying
	// error's own Error() string.
	Reason string
}

func (e *SendError) Error() string { return e.Reason }

func newSendError(kind ErrorKind, format string, args ...any) *SendError {
	return &SendError{Kind: kind, Reason: fmt.Sprintf(format, args...)}
}

// KindOf reports the ErrorKind of err, defaulting to KindPermanent for any
// error that is not a *SendError — an unclassified failure is never retried
// and never blamed on a device token.
func KindOf(err error) ErrorKind {
	var se *SendError
	if errors.As(err, &se) {
		return se.Kind
	}
	return KindPermanent
}

// Client is one configured provider credential, ready to send. Send returns
// nil on an accepted notification, or a *SendError.
type Client interface {
	// Provider is "fcm" or "apns" — the same vocabulary
	// device_tokens.provider uses, so a caller can match tokens to the
	// client that can actually deliver to them.
	Provider() string
	Send(ctx context.Context, deviceToken string, n Notification) error
}

// ctxErr converts a cancelled/expired context into a transient SendError
// without leaking the underlying error text.
func ctxErr(ctx context.Context) *SendError {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return newSendError(KindTransient, "push send timed out")
	}
	return newSendError(KindTransient, "push send cancelled")
}
