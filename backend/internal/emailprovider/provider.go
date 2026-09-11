// Package emailprovider implements a real SMTP client for B-014: sending one
// alert notification email over the wire protocol RFC 5321 defines, spoken
// directly with net/smtp (stdlib) — no vendor SDK, no new module dependency,
// the same "no new dependency" property ADR-0006's WebhookDispatcher and
// ADR-0007's internal/pushprovider both have.
//
// This package deliberately knows nothing about pulsewatch's incidents,
// channels, or database. It takes a relay Config, a recipient address, and a
// Message, and reports back a classified outcome — which is the whole input
// alerting.EmailDispatcher needs to decide "retry" or "give up".
//
// Sanitization discipline (FR-023, inherited from ADR-0006/ADR-0007): no
// error this package returns ever embeds the recipient address, the relay
// password, or a raw net/textproto error's own server-supplied text. Every
// SendError.Reason is assembled from a fixed vocabulary plus an SMTP reply
// code — never from the server's free-text message, which for some relays
// can itself echo back the address that was rejected.
package emailprovider

import (
	"errors"
	"fmt"
)

// Message is the provider-neutral shape of one notification email.
type Message struct {
	Subject string
	Body    string
}

// ErrorKind classifies a failed send into the decisions a caller can
// actually act on — the same shape internal/pushprovider uses for the
// identical reason: "the address is bad" and "the relay is briefly
// unavailable" look similar at the protocol level and must not be treated
// alike (see EmailDispatcher, alerting/emaildispatch.go).
type ErrorKind string

const (
	// KindTransient — another attempt could plausibly succeed: a 4xx SMTP
	// reply at any protocol phase, or a network/transport failure (dial
	// refused, timeout, connection reset). Retry with backoff.
	KindTransient ErrorKind = "transient"
	// KindPermanent — the relay rejected this specific recipient, sender,
	// or message body with a 5xx reply outside the AUTH phase. The classic
	// case is RCPT TO returning 550 "mailbox unavailable" — a hard bounce,
	// the SMTP-protocol sibling of push's dead-token case. Never retryable.
	KindPermanent ErrorKind = "permanent"
	// KindCredential — our own relay account was rejected (SMTP AUTH
	// failed, or authentication could not even be attempted because the
	// connection isn't encrypted). Never the recipient's fault, and never
	// retryable: repeating the same credentials will fail identically.
	KindCredential ErrorKind = "credential"
)

// SendError is the only error type Client.Send returns.
type SendError struct {
	Kind ErrorKind
	// Reason is safe to log and to persist verbatim into
	// alert_dispatches.last_error: built from this package's own fixed
	// vocabulary plus an SMTP reply code, never from a server's free-text
	// message or this client's own recipient/credential values.
	Reason string
}

func (e *SendError) Error() string { return e.Reason }

func newSendError(kind ErrorKind, format string, args ...any) *SendError {
	return &SendError{Kind: kind, Reason: fmt.Sprintf(format, args...)}
}

// KindOf reports the ErrorKind of err, defaulting to KindPermanent for any
// error that is not a *SendError — an unclassified failure is never blindly
// retried.
func KindOf(err error) ErrorKind {
	var se *SendError
	if errors.As(err, &se) {
		return se.Kind
	}
	return KindPermanent
}
