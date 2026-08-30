package alerting

import (
	"context"
	"log/slog"

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

// Dispatcher sends one notification for one channel. The only
// implementation this session ships is LogDispatcher — a clearly-labeled
// stub, matching this portfolio's established stub-tier pattern
// (bookslot/lexicon): no real webhook/email/SMS provider integration is in
// this session's scope (ADR-0002 Consequences explicitly leaves delivery
// mechanism to Implementation). A real provider is a drop-in Dispatcher
// implementation for a later session — nothing above this interface needs
// to change for that.
type Dispatcher interface {
	Dispatch(ctx context.Context, channel Channel, req DispatchRequest) error
}

// LogDispatcher is this session's stub notification sender: it logs that a
// notification would be sent, and to which channel (by id/type only), and
// nothing more.
//
// FR-023's credential-handling contract is enforced even against this
// stub: channel.destination is never logged, never included in the log
// line below, even though LoadChannels decrypted it — exactly as a real
// provider integration would have to. This proves the decrypt path is real
// and exercised end-to-end, not that the secret would be safe to print if a
// real provider replaced this stub.
type LogDispatcher struct {
	logger *slog.Logger
}

// NewLogDispatcher constructs the stub dispatcher. A nil logger falls back
// to slog.Default(), matching scheduler.New's own convention.
func NewLogDispatcher(logger *slog.Logger) *LogDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogDispatcher{logger: logger}
}

// Dispatch implements Dispatcher — see LogDispatcher's own doc comment for
// what it logs and, deliberately, what it never logs.
func (d *LogDispatcher) Dispatch(_ context.Context, channel Channel, req DispatchRequest) error {
	d.logger.Info("stub alert dispatch — no real notification provider configured this session",
		"kind", req.Kind, "incident_id", req.IncidentID, "target_id", req.TargetID,
		"channel_id", channel.ID, "channel_type", channel.Type)
	return nil
}

// NotifyChannels loads every configured channel, hands the request to
// dispatcher for each, and records one alert_dispatches row per attempt —
// "record of each attempted notification, for exactly-once verification and
// debugging" (04-data-model.md). Zero configured channels is not an error:
// the incidents row is still the durable record of the transition
// (ADR-0002) — there is simply nowhere to notify yet, since no
// channel-registration API exists this session.
//
// delivery_confirmed reflects only whether dispatcher.Dispatch itself
// reported success — for the stub, that means the log write succeeded, not
// that any real party received anything. A real provider Dispatcher would
// give this column its intended meaning without any change here.
func NotifyChannels(ctx context.Context, pool *pgxpool.Pool, dispatcher Dispatcher, key []byte, req DispatchRequest, logger *slog.Logger) {
	channels, err := LoadChannels(ctx, pool, key)
	if err != nil {
		logger.Error("load alert channels; skipping dispatch", "error", err, "incident_id", req.IncidentID)
		return
	}

	for _, channel := range channels {
		dispatchErr := dispatcher.Dispatch(ctx, channel, req)
		confirmed := dispatchErr == nil
		if dispatchErr != nil {
			logger.Error("dispatch alert", "error", dispatchErr, "incident_id", req.IncidentID, "channel_id", channel.ID)
		}

		const insertDispatch = `
INSERT INTO alert_dispatches (incident_id, alert_channel_id, kind, delivery_confirmed)
VALUES ($1, $2::uuid, $3, $4)`
		if _, execErr := pool.Exec(ctx, insertDispatch, req.IncidentID, channel.ID, req.Kind, confirmed); execErr != nil {
			logger.Error("record alert_dispatches row", "error", execErr, "incident_id", req.IncidentID, "channel_id", channel.ID)
		}
	}
}
