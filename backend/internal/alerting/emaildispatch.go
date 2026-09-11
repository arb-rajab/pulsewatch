package alerting

import (
	"context"
	"fmt"
	"time"

	"github.com/arb-rajab/pulsewatch/backend/internal/emailprovider"
)

// EmailDispatcher is B-014's real Dispatcher for "email" channels: a
// genuine SMTP send (internal/emailprovider) to the channel's decrypted
// destination — the recipient address, exactly like a webhook channel's
// destination is the URL to POST to (migration 000008 named it that from
// the start; ADR-0006 just hadn't built a sender for it yet).
//
// It is a sibling of WebhookDispatcher, not PushDispatcher: an email
// channel has exactly one destination, so there is no fan-out, no dead-
// token table, and no "abort the rest of the fan-out" case to design
// around. What email adds over webhook is a different failure vocabulary —
// SMTP reply codes instead of HTTP status codes — and one genuinely new
// failure shape webhook never has: our own relay *account* being rejected
// (SMTP AUTH failure), which is never the recipient's fault and must not be
// blamed on — or retried against — the destination address.
type EmailDispatcher struct {
	// Config is the outgoing relay account (internal/emailprovider). A zero
	// Config (no SMTP_HOST/SMTP_FROM_ADDRESS configured) is not a panic: it
	// makes every dispatch an honest unconfirmed outcome, the same
	// fail-safe PushDispatcher's nil Pool already establishes.
	Config emailprovider.Config
	// MaxAttempts is the total number of SMTP attempts (including the
	// first), applied only to transient failures. <= 0 means
	// defaultMaxAttempts (3, ADR-0006's value, unchanged here).
	MaxAttempts int
	// BackoffBase/BackoffMax/PerAttemptTimeout mirror WebhookDispatcher's
	// fields and defaults: the transient-failure shape (a 4xx SMTP reply, a
	// dial timeout, a connection reset) is the same shape webhook already
	// retries, so this is one retry policy, not a second one that happens
	// to look alike.
	BackoffBase       time.Duration
	BackoffMax        time.Duration
	PerAttemptTimeout time.Duration
}

// NewEmailDispatcher constructs the real email dispatcher with this
// package's default retry policy.
func NewEmailDispatcher(cfg emailprovider.Config) *EmailDispatcher {
	return &EmailDispatcher{Config: cfg}
}

func (d *EmailDispatcher) maxAttempts() int {
	if d.MaxAttempts > 0 {
		return d.MaxAttempts
	}
	return defaultMaxAttempts
}

func (d *EmailDispatcher) backoffBase() time.Duration {
	if d.BackoffBase > 0 {
		return d.BackoffBase
	}
	return defaultBackoffBase
}

func (d *EmailDispatcher) backoffMax() time.Duration {
	if d.BackoffMax > 0 {
		return d.BackoffMax
	}
	return defaultBackoffMax
}

func (d *EmailDispatcher) perAttemptTimeout() time.Duration {
	if d.PerAttemptTimeout > 0 {
		return d.PerAttemptTimeout
	}
	return defaultPerAttemptTimeout
}

// emailMessage builds the provider-neutral message for one incident edge
// transition. Deliberately text/plain and terse, the same "ids, not a
// rendered dashboard" restraint ADR-0006's webhook payload and ADR-0007's
// push notification both apply — an email inbox is not the place to
// duplicate the dashboard's own incident detail, and keeping this small
// keeps it identical in shape to what an operator's webhook/push already
// receives.
func emailMessage(req DispatchRequest) emailprovider.Message {
	subject := fmt.Sprintf("pulsewatch: incident opened (target %s)", req.TargetID)
	body := fmt.Sprintf(
		"A monitored target has started failing its checks.\n\nTarget: %s\nIncident: %d\n",
		req.TargetID, req.IncidentID,
	)
	if req.Kind == "resolved" {
		subject = fmt.Sprintf("pulsewatch: incident resolved (target %s)", req.TargetID)
		body = fmt.Sprintf(
			"A monitored target is passing its checks again.\n\nTarget: %s\nIncident: %d\n",
			req.TargetID, req.IncidentID,
		)
	}
	return emailprovider.Message{Subject: subject, Body: body}
}

// Dispatch implements Dispatcher for "email" channels. Any other channel
// type is reported back unconfirmed rather than silently claiming success
// or silently doing nothing — the same defensive type guard
// WebhookDispatcher and PushDispatcher each keep for a mis-wired router
// (ADR-0007's ChannelRouter is what actually decides which dispatcher a
// channel type reaches in production).
func (d *EmailDispatcher) Dispatch(ctx context.Context, channel Channel, req DispatchRequest) DispatchOutcome {
	if channel.Type != "email" {
		return DispatchOutcome{
			Confirmed: false,
			Attempts:  1,
			LastError: fmt.Sprintf("%s channel cannot be delivered by the email dispatcher", channel.Type),
		}
	}
	if err := d.Config.Validate(); err != nil {
		return DispatchOutcome{Confirmed: false, Attempts: 0, LastError: err.Error()}
	}

	client, err := emailprovider.NewClient(d.Config)
	if err != nil {
		// Validate above already guards the one construction failure
		// NewClient can hit, but never trust that invariant silently.
		return DispatchOutcome{Confirmed: false, Attempts: 0, LastError: "email relay configuration invalid: " + err.Error()}
	}

	msg := emailMessage(req)
	maxAttempts := d.maxAttempts()
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			delay := backoffDelay(d.backoffBase(), d.backoffMax(), attempt-1)
			select {
			case <-ctx.Done():
				return DispatchOutcome{Confirmed: false, Attempts: attempt - 1, LastError: lastErr.Error()}
			case <-time.After(delay):
			}
		}

		attemptCtx, cancel := context.WithTimeout(ctx, d.perAttemptTimeout())
		sendErr := client.Send(attemptCtx, channel.destination, msg)
		cancel()

		if sendErr == nil {
			return DispatchOutcome{Confirmed: true, Attempts: attempt}
		}
		lastErr = sendErr
		if emailprovider.KindOf(sendErr) != emailprovider.KindTransient {
			return DispatchOutcome{Confirmed: false, Attempts: attempt, LastError: sendErr.Error()}
		}
	}
	return DispatchOutcome{Confirmed: false, Attempts: maxAttempts, LastError: lastErr.Error()}
}
