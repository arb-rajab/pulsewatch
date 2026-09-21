// liveEvents is the browser-side half of ADR-0010's live push: a thin
// wrapper around EventSource pointed at this frontend's own /dashboard/events
// proxy (never the backend directly — see that route's own doc comment).
//
// EventSource already reconnects natively on its own, but only with a
// fixed, server-suggested retry delay — a network blip that takes the
// backend down for a while would otherwise mean this client retries at a
// constant cadence indefinitely. This wrapper takes over reconnection
// itself: on every error it tears down the current EventSource and
// schedules a fresh one after a capped exponential backoff, resetting back
// to the base delay the moment a connection successfully opens. A
// dashboard that silently stopped updating after a blip would be strictly
// worse than the polling this is additive to (Session brief) — this is the
// concrete mechanism that keeps it from doing that.

export interface LiveTargetStatusEvent {
	type: 'target_status';
	target_id: string;
	state: string;
	streak: number;
	occurred_at: string;
}

export interface LiveIncidentEvent {
	type: 'incident';
	target_id: string;
	incident_id: number;
	kind: 'opened' | 'resolved';
	occurred_at: string;
}

export type LiveEvent = LiveTargetStatusEvent | LiveIncidentEvent;

export interface LiveEventsHandle {
	close: () => void;
}

const baseReconnectDelayMS = 1000;
const maxReconnectDelayMS = 30_000;

// connectLiveEvents opens the SSE stream and invokes onEvent for every
// real target_status/incident event it receives. onStatusChange (optional)
// reports 'connected' | 'reconnecting' so a caller can show a small
// "live" / "reconnecting…" indicator without this module owning any UI.
// Returns a handle whose close() stops all reconnection attempts — call it
// from the component's own teardown (onDestroy) so a navigated-away
// dashboard doesn't keep retrying in the background.
export function connectLiveEvents(
	onEvent: (event: LiveEvent) => void,
	onStatusChange?: (status: 'connected' | 'reconnecting') => void
): LiveEventsHandle {
	let source: EventSource | null = null;
	let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	let reconnectDelay = baseReconnectDelayMS;
	let closed = false;

	function handleMessage(kind: LiveEvent['type']) {
		return (message: MessageEvent<string>) => {
			try {
				const parsed = JSON.parse(message.data) as LiveEvent;
				if (parsed.type === kind) {
					onEvent(parsed);
				}
			} catch {
				// Malformed frame — never let one bad event tear down an
				// otherwise-healthy connection.
			}
		};
	}

	function scheduleReconnect() {
		if (closed || reconnectTimer) return;
		onStatusChange?.('reconnecting');
		reconnectTimer = setTimeout(() => {
			reconnectTimer = null;
			reconnectDelay = Math.min(reconnectDelay * 2, maxReconnectDelayMS);
			open();
		}, reconnectDelay);
	}

	function open() {
		if (closed) return;
		source = new EventSource('/dashboard/events');
		source.addEventListener('target_status', handleMessage('target_status'));
		source.addEventListener('incident', handleMessage('incident'));
		source.onopen = () => {
			reconnectDelay = baseReconnectDelayMS;
			onStatusChange?.('connected');
		};
		source.onerror = () => {
			source?.close();
			source = null;
			scheduleReconnect();
		};
	}

	open();

	return {
		close() {
			closed = true;
			if (reconnectTimer) clearTimeout(reconnectTimer);
			source?.close();
			source = null;
		}
	};
}
