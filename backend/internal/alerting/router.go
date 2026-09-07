package alerting

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ChannelRouter is ADR-0007's answer to "there is now more than one real
// Dispatcher": a Dispatcher that owns no delivery logic of its own and
// simply hands each channel to the implementation for its type.
//
// Before this session there was exactly one real implementation
// (WebhookDispatcher, ADR-0006), which handled the single-implementation
// case by answering "not implemented" for any type that wasn't its own.
// That shape does not extend to two implementations without one of them
// knowing about the other, so the type-to-implementation decision is lifted
// out into this one place instead. WebhookDispatcher and PushDispatcher
// each keep their own defensive type guard — a mis-wired router should
// produce an honest unconfirmed outcome, not a webhook POST to a push
// credential.
//
// A channel type with no registered dispatcher (today: "email", FR-014,
// still deliberately unbuilt — see B-014) is reported back exactly as
// ADR-0006 already reported it: unconfirmed, with a "not implemented"
// last_error recorded in alert_dispatches. Never silently dropped, never
// falsely confirmed.
type ChannelRouter struct {
	byType map[string]Dispatcher
}

// NewChannelRouter builds a router from a channel-type-keyed map. The map is
// copied, so a caller cannot mutate the router's routing table after
// construction.
func NewChannelRouter(byType map[string]Dispatcher) *ChannelRouter {
	copied := make(map[string]Dispatcher, len(byType))
	for channelType, dispatcher := range byType {
		copied[channelType] = dispatcher
	}
	return &ChannelRouter{byType: copied}
}

// NewDefaultDispatcher is the one construction every production entry point
// uses (scheduler.New, main.go's agent-facing OTLP path). Having exactly one
// such function is deliberate: Session 17 had to update two independent call
// sites in lockstep to add one dispatcher, and this session would have made
// that three.
//
// pool is required by PushDispatcher (device_tokens lives there); client may
// be nil.
func NewDefaultDispatcher(pool *pgxpool.Pool, client *http.Client) *ChannelRouter {
	return NewChannelRouter(map[string]Dispatcher{
		"webhook": NewWebhookDispatcher(client),
		"push":    NewPushDispatcher(pool, client),
	})
}

// Dispatch implements Dispatcher.
func (r *ChannelRouter) Dispatch(ctx context.Context, channel Channel, req DispatchRequest) DispatchOutcome {
	dispatcher, ok := r.byType[channel.Type]
	if !ok {
		return DispatchOutcome{
			Confirmed: false,
			Attempts:  1,
			LastError: fmt.Sprintf("%s channel delivery is not implemented (see 11-backlog.md)", channel.Type),
		}
	}
	return dispatcher.Dispatch(ctx, channel, req)
}
