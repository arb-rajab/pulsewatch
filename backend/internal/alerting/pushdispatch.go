package alerting

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arb-rajab/pulsewatch/backend/internal/pushprovider"
)

// PushDispatcher is ADR-0007's real Dispatcher for "push" channels: a
// genuine send through Firebase Cloud Messaging's HTTP v1 API or Apple's
// APNs HTTP/2 API (internal/pushprovider) to every live device token
// registered for that channel's provider.
//
// It is a sibling of ADR-0006's WebhookDispatcher, not a variation on it,
// because push delivery differs from webhook delivery in the one way that
// matters to a retry policy: a webhook channel has exactly one destination
// and every failure is either "try again" or "give up", whereas a push
// channel fans out to N destinations and the most common failure — an
// expired or uninstalled device token — is neither. It must never be
// retried, and it must be recorded so no future incident pays for it again.
// That third outcome is what this type exists for.
type PushDispatcher struct {
	// Pool is required: unlike a webhook destination, a push channel's
	// actual delivery addresses live in device_tokens, and dead-token
	// marking is a write. A nil Pool makes every dispatch an honest
	// unconfirmed outcome rather than a panic in the scheduler's hot path.
	Pool *pgxpool.Pool
	// Client is handed to the provider clients. Tests point it at a local
	// server speaking the real FCM/APNs protocols; production leaves it nil.
	Client *http.Client
	// MaxAttempts is the total number of provider attempts per device token
	// (including the first), applied only to transient failures. <= 0 means
	// defaultMaxAttempts (3, ADR-0006's value, unchanged here).
	MaxAttempts int
	// BackoffBase/BackoffMax/PerAttemptTimeout mirror WebhookDispatcher's
	// fields and defaults, deliberately: the transient-failure shape (5xx,
	// 429, network) is the same shape for both, so this is one policy, not
	// two that happen to look alike.
	BackoffBase       time.Duration
	BackoffMax        time.Duration
	PerAttemptTimeout time.Duration
	// Logger receives per-device-token detail that would not fit in one
	// alert_dispatches row. nil means slog.Default().
	Logger *slog.Logger
}

// NewPushDispatcher constructs the real push dispatcher with this package's
// default retry policy.
func NewPushDispatcher(pool *pgxpool.Pool, client *http.Client) *PushDispatcher {
	return &PushDispatcher{Pool: pool, Client: client}
}

func (d *PushDispatcher) maxAttempts() int {
	if d.MaxAttempts > 0 {
		return d.MaxAttempts
	}
	return defaultMaxAttempts
}

func (d *PushDispatcher) backoffBase() time.Duration {
	if d.BackoffBase > 0 {
		return d.BackoffBase
	}
	return defaultBackoffBase
}

func (d *PushDispatcher) backoffMax() time.Duration {
	if d.BackoffMax > 0 {
		return d.BackoffMax
	}
	return defaultBackoffMax
}

func (d *PushDispatcher) perAttemptTimeout() time.Duration {
	if d.PerAttemptTimeout > 0 {
		return d.PerAttemptTimeout
	}
	return defaultPerAttemptTimeout
}

func (d *PushDispatcher) logger() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// pushNotification builds the provider-neutral notification for one
// incident edge transition.
//
// The body carries ids, not a target's URL or hostname. That is the same
// choice ADR-0006 made for the webhook payload, and it matters more here:
// a push notification renders on a lock screen, in front of whoever is
// holding the phone. The mobile app resolves the ids against
// GET /targets/{id}/incidents once it is unlocked and authenticated.
func pushNotification(req DispatchRequest) pushprovider.Notification {
	title := "pulsewatch: incident opened"
	body := "A monitored target has started failing its checks."
	if req.Kind == "resolved" {
		title = "pulsewatch: incident resolved"
		body = "A monitored target is passing its checks again."
	}
	return pushprovider.Notification{
		Title: title,
		Body:  body,
		// These three keys are the mobile app's deep-link contract
		// (pulsewatch-mobile): tapping the notification routes to the
		// incident named by incident_id. Provider-neutral — FCM delivers
		// them as the data map, APNs as top-level custom payload keys.
		Data: map[string]string{
			"kind":        req.Kind,
			"incident_id": strconv.FormatInt(req.IncidentID, 10),
			"target_id":   req.TargetID,
		},
		// One collapse id per incident edge transition: if a device is
		// offline while both the open and the resolve fire, it should
		// still receive both, but a duplicate of the same transition
		// replaces rather than stacks.
		CollapseID: req.Kind + "-" + strconv.FormatInt(req.IncidentID, 10),
	}
}

// Dispatch implements Dispatcher for "push" channels.
//
// The returned DispatchOutcome aggregates a fan-out into the one row
// alert_dispatches holds per channel per transition (ADR-0006's shape,
// unchanged):
//
//   - Confirmed is true when at least one live device token accepted the
//     notification. An operator with three phones, one of which was wiped
//     last week, genuinely was notified.
//   - Attempts is the total number of provider HTTP attempts across every
//     token, so a retried fan-out reports the real work done.
//   - LastError is empty only when every live token was delivered to.
//     A partial success reports the shortfall rather than hiding it behind
//     Confirmed=true. Per-token detail (which token died, and why) lives in
//     device_tokens.dead_at/dead_reason, which is a better record of a
//     per-device fact than one shared text column could ever be.
//
// Zero live tokens is reported unconfirmed with zero attempts: nothing was
// notified, and saying otherwise would be the silent-success failure mode
// ADR-0006 went out of its way to avoid for email.
func (d *PushDispatcher) Dispatch(ctx context.Context, channel Channel, req DispatchRequest) DispatchOutcome {
	if channel.Type != "push" {
		return DispatchOutcome{
			Confirmed: false,
			Attempts:  1,
			LastError: fmt.Sprintf("%s channel cannot be delivered by the push dispatcher", channel.Type),
		}
	}
	if d.Pool == nil {
		return DispatchOutcome{Confirmed: false, Attempts: 0, LastError: "push dispatcher has no database pool configured"}
	}

	client, err := pushprovider.ClientFromCredentialJSON(channel.destination, d.Client)
	if err != nil {
		// pushprovider's construction errors describe the credential's
		// shape, never its contents — safe to persist verbatim.
		return DispatchOutcome{Confirmed: false, Attempts: 0, LastError: "push channel credential unusable: " + err.Error()}
	}

	tokens, err := loadLiveDeviceTokens(ctx, d.Pool, client.Provider())
	if err != nil {
		return DispatchOutcome{Confirmed: false, Attempts: 0, LastError: "could not load device tokens for push channel"}
	}
	if len(tokens) == 0 {
		return DispatchOutcome{
			Confirmed: false,
			Attempts:  0,
			LastError: fmt.Sprintf("no live device tokens registered for provider %s", client.Provider()),
		}
	}

	notification := pushNotification(req)
	var (
		attempts  int
		delivered int
		dead      int
		failed    int
		reasons   = map[string]int{}
		credAbort string
		notTried  int
		logger    = d.logger()
	)

	for i, token := range tokens {
		if credAbort != "" {
			// Our own credential was rejected: every remaining token would
			// fail identically. Stopping is not giving up on them, it is
			// declining to repeat one configuration failure once per
			// registered device (and, for APNs, declining to walk into
			// TooManyProviderTokenUpdates on top of it).
			notTried = len(tokens) - i
			break
		}

		used, sendErr := d.sendWithRetry(ctx, client, token.token, notification)
		attempts += used

		if sendErr == nil {
			delivered++
			if markErr := markDeviceTokenDelivered(ctx, d.Pool, token.id); markErr != nil {
				logger.Error("record device token delivery", "error", markErr, "device_token_id", token.id)
			}
			continue
		}

		reason := sendErr.Error()
		reasons[reason]++

		switch pushprovider.KindOf(sendErr) {
		case pushprovider.KindDeadToken:
			dead++
			if markErr := markDeviceTokenDead(ctx, d.Pool, token.id, reason); markErr != nil {
				logger.Error("mark device token dead", "error", markErr, "device_token_id", token.id)
			}
			logger.Warn("device token marked dead", "device_token_id", token.id,
				"provider", client.Provider(), "reason", reason, "incident_id", req.IncidentID)
		case pushprovider.KindCredential:
			failed++
			credAbort = reason
			logger.Error("push credential rejected; aborting fan-out", "error", reason,
				"provider", client.Provider(), "channel_id", channel.ID, "incident_id", req.IncidentID)
		default:
			failed++
			logger.Error("push delivery failed", "error", reason, "device_token_id", token.id,
				"provider", client.Provider(), "incident_id", req.IncidentID)
		}
	}

	return DispatchOutcome{
		Confirmed: delivered > 0,
		Attempts:  attempts,
		LastError: summarizePushOutcome(len(tokens), delivered, dead, failed, notTried, reasons),
	}
}

// sendWithRetry performs up to maxAttempts sends for one device token,
// retrying only pushprovider.KindTransient failures. It returns the number
// of attempts actually made and the final error (nil on success).
//
// A dead-token, credential, or permanent failure returns after one attempt
// by construction — that is the whole behavioural difference between this
// and WebhookDispatcher's loop, and the reason a push channel does not
// spend 3 attempts and two backoff sleeps per uninstalled app on every
// single incident.
func (d *PushDispatcher) sendWithRetry(ctx context.Context, client pushprovider.Client, deviceToken string, n pushprovider.Notification) (int, error) {
	maxAttempts := d.maxAttempts()
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			delay := backoffDelay(d.backoffBase(), d.backoffMax(), attempt-1)
			select {
			case <-ctx.Done():
				return attempt - 1, lastErr
			case <-time.After(delay):
			}
		}

		attemptCtx, cancel := context.WithTimeout(ctx, d.perAttemptTimeout())
		err := client.Send(attemptCtx, deviceToken, n)
		cancel()

		if err == nil {
			return attempt, nil
		}
		lastErr = err
		if pushprovider.KindOf(err) != pushprovider.KindTransient {
			return attempt, err
		}
		if ctx.Err() != nil {
			return attempt, err
		}
	}
	return maxAttempts, lastErr
}

// summarizePushOutcome renders the aggregate LastError. Empty means every
// live token was delivered to.
func summarizePushOutcome(total, delivered, dead, failed, notTried int, reasons map[string]int) string {
	if delivered == total {
		return ""
	}
	summary := fmt.Sprintf("delivered to %d of %d device tokens", delivered, total)
	if dead > 0 {
		summary += fmt.Sprintf("; %d marked dead", dead)
	}
	if failed > 0 {
		summary += fmt.Sprintf("; %d failed", failed)
	}
	if notTried > 0 {
		summary += fmt.Sprintf("; %d not attempted after a credential rejection", notTried)
	}
	// Deterministic ordering: map iteration order would otherwise make this
	// column's contents differ run to run for the same real outcome.
	distinct := make([]string, 0, len(reasons))
	for reason := range reasons {
		distinct = append(distinct, reason)
	}
	sort.Strings(distinct)
	if len(distinct) > 0 {
		summary += " (" + joinReasons(distinct, reasons) + ")"
	}
	return summary
}

func joinReasons(distinct []string, counts map[string]int) string {
	out := ""
	for i, reason := range distinct {
		if i > 0 {
			out += "; "
		}
		if counts[reason] > 1 {
			out += fmt.Sprintf("%s x%d", reason, counts[reason])
		} else {
			out += reason
		}
	}
	return out
}
