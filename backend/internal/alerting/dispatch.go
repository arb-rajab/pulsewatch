package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DispatchRequest is what a successful conditional incidents-table write
// (OpenIncident/CloseIncident actually returning a row) produces — the one
// signal that gates dispatch at all. ADR-0002 Consequences: "Alert dispatch
// is triggered only after the conditional write above actually returns a
// row."
type DispatchRequest struct {
	IncidentID int64
	TargetID   string
	Kind       string // "opened" | "resolved"
}

// DispatchOutcome is what a Dispatcher reports back to NotifyChannels after
// attempting (and, for WebhookDispatcher, possibly retrying) one
// notification for one channel — ADR-0006's delivery-status shape,
// persisted verbatim into alert_dispatches.attempts/last_error. Confirmed
// mirrors the old delivery_confirmed column exactly; Attempts and LastError
// are new, additive visibility into what the dispatcher actually did to
// reach that verdict.
type DispatchOutcome struct {
	Confirmed bool
	Attempts  int
	// LastError is empty when the notification fully succeeded. For a
	// single-destination channel (webhook) that is the same thing as
	// Confirmed being true. For a fan-out channel (push, ADR-0007) it is
	// stricter: Confirmed means at least one device was reached, and a
	// partial success reports its shortfall here rather than hiding it.
	//
	// Never contains a channel's destination, a device token, or a provider
	// credential (FR-023) — every Dispatcher implementation in this package
	// is required to keep that guarantee itself, not rely on the caller to
	// scrub it.
	LastError string
}

// Dispatcher sends one notification for one channel. This package ships
// three real implementations and one router:
//
//   - WebhookDispatcher (ADR-0006) — a genuine HTTP POST with retry/backoff
//     for channel.Type == "webhook".
//   - PushDispatcher (ADR-0007) — a genuine FCM HTTP v1 / APNs HTTP/2 send,
//     fanned out over every live device token, for channel.Type == "push".
//   - EmailDispatcher (B-014) — a genuine SMTP send with retry/backoff for
//     channel.Type == "email".
//   - ChannelRouter (ADR-0007) — routes a channel to the implementation for
//     its type, and reports a type with no implementation back as not
//     implemented rather than silently pretending success. Every type
//     alert_channels' own CHECK constraint allows has a real implementation
//     today; this path is now a defensive guard against a mis-wired router.
type Dispatcher interface {
	Dispatch(ctx context.Context, channel Channel, req DispatchRequest) DispatchOutcome
}

// webhookPayload is the JSON body POSTed to a webhook channel's
// destination. Deliberately small and stable: an incident id, its target,
// which edge transition this is, and when this attempt was sent — enough
// for a receiver to correlate against GET /targets/{id}/incidents without
// this repo inventing a larger notification schema than FR-013 asks for.
type webhookPayload struct {
	Kind       string    `json:"kind"`
	IncidentID int64     `json:"incident_id"`
	TargetID   string    `json:"target_id"`
	SentAt     time.Time `json:"sent_at"`
}

// Default retry policy — ADR-0006. Three attempts total (one original plus
// two retries), exponential backoff starting at 200ms, each individual HTTP
// attempt bounded to 5s. Deliberately conservative: this is a single
// operator's own webhook (Slack/PagerDuty/etc.), not a multi-tenant fan-out,
// so a few seconds of total latency on a real outage notification is an
// acceptable trade for not giving up after one transient blip.
const (
	defaultMaxAttempts       = 3
	defaultBackoffBase       = 200 * time.Millisecond
	defaultBackoffMax        = 2 * time.Second
	defaultPerAttemptTimeout = 5 * time.Second
)

// WebhookDispatcher is ADR-0006's real Dispatcher: an actual HTTP POST to a
// webhook channel's decrypted destination, retried with exponential backoff
// on transient failures. Every field has a zero-value-safe default (see
// NewWebhookDispatcher) so tests can override just what they need to (a
// faster backoff, a fixed clock) without reconstructing the whole thing.
type WebhookDispatcher struct {
	// Client sends the actual HTTP request. Per-attempt timeout is applied
	// via context, not Client.Timeout, so one shared client can't leak a
	// timeout tuned for a different dispatcher's attempts.
	Client *http.Client
	// MaxAttempts is the total number of HTTP attempts (including the
	// first), not the number of retries. <= 0 means defaultMaxAttempts.
	MaxAttempts int
	// BackoffBase is the delay before the second attempt; each subsequent
	// delay doubles, capped at BackoffMax. <= 0 means defaultBackoffBase.
	BackoffBase time.Duration
	// BackoffMax caps the computed backoff delay. <= 0 means
	// defaultBackoffMax.
	BackoffMax time.Duration
	// PerAttemptTimeout bounds a single HTTP round-trip. <= 0 means
	// defaultPerAttemptTimeout.
	PerAttemptTimeout time.Duration
}

// NewWebhookDispatcher constructs the real dispatcher with this package's
// default retry policy. A nil client falls back to a plain &http.Client{} —
// per-attempt timeouts are applied per-request via context, so the client
// itself needs no Timeout of its own.
func NewWebhookDispatcher(client *http.Client) *WebhookDispatcher {
	if client == nil {
		client = &http.Client{}
	}
	return &WebhookDispatcher{Client: client}
}

func (d *WebhookDispatcher) maxAttempts() int {
	if d.MaxAttempts > 0 {
		return d.MaxAttempts
	}
	return defaultMaxAttempts
}

func (d *WebhookDispatcher) backoffBase() time.Duration {
	if d.BackoffBase > 0 {
		return d.BackoffBase
	}
	return defaultBackoffBase
}

func (d *WebhookDispatcher) backoffMax() time.Duration {
	if d.BackoffMax > 0 {
		return d.BackoffMax
	}
	return defaultBackoffMax
}

func (d *WebhookDispatcher) perAttemptTimeout() time.Duration {
	if d.PerAttemptTimeout > 0 {
		return d.PerAttemptTimeout
	}
	return defaultPerAttemptTimeout
}

// backoffDelay computes the delay before retry number n (1-indexed: the
// delay before the 2nd overall attempt is backoffDelay(base, maxDelay, 1)),
// doubling each time and capped at maxDelay.
func backoffDelay(base, maxDelay time.Duration, n int) time.Duration {
	delay := base
	for i := 1; i < n; i++ {
		delay *= 2
		if delay >= maxDelay {
			return maxDelay
		}
	}
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

// webhookAttemptError classifies one failed HTTP attempt without ever
// carrying the channel's destination (FR-023) — every message this type can
// produce is a fixed string or an HTTP status code, nothing derived from
// the request itself.
type webhookAttemptError struct {
	kind       string // "malformed" | "transport" | "status"
	statusCode int
}

func (e *webhookAttemptError) Error() string {
	switch e.kind {
	case "status":
		return fmt.Sprintf("webhook POST returned status %d", e.statusCode)
	case "malformed":
		return "webhook destination is not a valid HTTP(S) URL"
	default:
		return "webhook POST transport error"
	}
}

// retryable reports whether another attempt could plausibly succeed. A
// malformed destination never will (it's a configuration error, not a
// transient one); a 4xx other than 429 means the receiver rejected this
// request specifically and a byte-for-byte retry won't change that; every
// other case (network failure, timeout, 429, 5xx) is worth retrying.
func (e *webhookAttemptError) retryable() bool {
	switch e.kind {
	case "malformed":
		return false
	case "status":
		return e.statusCode == http.StatusTooManyRequests || e.statusCode >= 500
	default:
		return true
	}
}

// Dispatch implements Dispatcher. For channel.Type == "webhook" it performs
// a real HTTP POST of webhookPayload, retrying transient failures per this
// dispatcher's backoff policy (ADR-0006).
//
// Any other channel type is reported back unconfirmed rather than silently
// claiming success or silently doing nothing. Since ADR-0007 this is a
// defensive guard against a mis-wired router, not the production answer for
// a real channel type: ChannelRouter (router.go) is what decides which
// Dispatcher a channel type reaches in production.
func (d *WebhookDispatcher) Dispatch(ctx context.Context, channel Channel, req DispatchRequest) DispatchOutcome {
	if channel.Type != "webhook" {
		return DispatchOutcome{
			Confirmed: false,
			Attempts:  1,
			LastError: fmt.Sprintf("%s channel delivery is not implemented by the webhook dispatcher", channel.Type),
		}
	}

	payload, err := json.Marshal(webhookPayload{
		Kind:       req.Kind,
		IncidentID: req.IncidentID,
		TargetID:   req.TargetID,
		SentAt:     time.Now().UTC(),
	})
	if err != nil {
		// Not a destination problem — every field here is our own data, so
		// this can't leak a secret. Still never retryable: the payload
		// won't change shape on a retry.
		return DispatchOutcome{Confirmed: false, Attempts: 1, LastError: "encode webhook payload: " + err.Error()}
	}

	maxAttempts := d.maxAttempts()
	var lastErr *webhookAttemptError
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			delay := backoffDelay(d.backoffBase(), d.backoffMax(), attempt-1)
			select {
			case <-ctx.Done():
				return DispatchOutcome{Confirmed: false, Attempts: attempt - 1, LastError: lastErr.Error()}
			case <-time.After(delay):
			}
		}

		attemptErr := d.attemptOnce(ctx, channel.destination, payload)
		if attemptErr == nil {
			return DispatchOutcome{Confirmed: true, Attempts: attempt}
		}
		lastErr = attemptErr
		if !attemptErr.retryable() {
			return DispatchOutcome{Confirmed: false, Attempts: attempt, LastError: attemptErr.Error()}
		}
	}
	return DispatchOutcome{Confirmed: false, Attempts: maxAttempts, LastError: lastErr.Error()}
}

// attemptOnce performs exactly one HTTP POST attempt, bounded by this
// dispatcher's PerAttemptTimeout. Every returned error is a
// *webhookAttemptError — never a raw net/http error, which for a bad URL
// can embed the URL itself (the channel's secret destination, FR-023) in
// its own Error() string.
func (d *WebhookDispatcher) attemptOnce(ctx context.Context, destination string, payload []byte) *webhookAttemptError {
	attemptCtx, cancel := context.WithTimeout(ctx, d.perAttemptTimeout())
	defer cancel()

	httpReq, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, destination, bytes.NewReader(payload))
	if err != nil {
		return &webhookAttemptError{kind: "malformed"}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := d.Client.Do(httpReq)
	if err != nil {
		return &webhookAttemptError{kind: "transport"}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection can be reused

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &webhookAttemptError{kind: "status", statusCode: resp.StatusCode}
	}
	return nil
}

// NotifyChannels loads every configured channel, hands the request to
// dispatcher for each, and records one alert_dispatches row per attempted
// notification — "record of each attempted notification, for
// exactly-once verification and debugging" (04-data-model.md). Zero
// configured channels is not an error: the incidents row is still the
// durable record of the transition (ADR-0002) — there is simply nowhere to
// notify yet, since no channel-registration API exists this session.
//
// delivery_confirmed/attempts/last_error reflect exactly what dispatcher
// reported (DispatchOutcome) — for WebhookDispatcher, that means a real
// HTTP 2xx was received, not just that a log write succeeded.
func NotifyChannels(ctx context.Context, pool *pgxpool.Pool, dispatcher Dispatcher, key []byte, req DispatchRequest, logger *slog.Logger) {
	channels, err := LoadChannels(ctx, pool, key)
	if err != nil {
		logger.Error("load alert channels; skipping dispatch", "error", err, "incident_id", req.IncidentID)
		return
	}

	for _, channel := range channels {
		outcome := dispatcher.Dispatch(ctx, channel, req)
		if outcome.Confirmed {
			logger.Info("alert dispatched", "kind", req.Kind, "incident_id", req.IncidentID,
				"channel_id", channel.ID, "channel_type", channel.Type, "attempts", outcome.Attempts)
		} else {
			logger.Error("dispatch alert", "error", outcome.LastError, "incident_id", req.IncidentID,
				"channel_id", channel.ID, "channel_type", channel.Type, "attempts", outcome.Attempts)
		}

		const insertDispatch = `
INSERT INTO alert_dispatches (incident_id, alert_channel_id, kind, delivery_confirmed, attempts, last_error)
VALUES ($1, $2::uuid, $3, $4, $5, NULLIF($6, ''))`
		if _, execErr := pool.Exec(ctx, insertDispatch, req.IncidentID, channel.ID, req.Kind, outcome.Confirmed, outcome.Attempts, outcome.LastError); execErr != nil {
			logger.Error("record alert_dispatches row", "error", execErr, "incident_id", req.IncidentID, "channel_id", channel.ID)
		}
	}
}
