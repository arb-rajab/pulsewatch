import type { RequestHandler } from './$types';
import { backendFetch, SESSION_COOKIE_NAME } from '$lib/server/backend';

// GET /dashboard/events is ADR-0010's browser-facing half of the SSE proxy:
// the browser's EventSource only ever talks to this frontend's own origin
// (the same reasoning backendFetch's own doc comment gives for why the
// session cookie is never forwarded to the browser verbatim) — this route
// re-attaches the operator's session cookie server-side, exactly like every
// load function and form action in this app already does, and streams the
// real backend's GET /api/v1/events response body straight through without
// buffering it.
//
// This is a plain passthrough, not a second implementation of anything
// event-shaped: the backend (internal/operatorapi.StreamEvents) is the only
// place that decides what an event is or when one fires.
export const GET: RequestHandler = async ({ cookies, fetch, request }) => {
	const sessionToken = cookies.get(SESSION_COOKIE_NAME);
	if (!sessionToken) {
		return new Response('unauthorized', { status: 401 });
	}

	const backendRes = await backendFetch(fetch, '/api/v1/events', sessionToken, {
		signal: request.signal
	});
	if (!backendRes.ok || !backendRes.body) {
		return new Response('event stream unavailable', { status: backendRes.status || 502 });
	}

	return new Response(backendRes.body, {
		status: 200,
		headers: {
			'Content-Type': 'text/event-stream',
			'Cache-Control': 'no-cache',
			Connection: 'keep-alive',
			'X-Accel-Buffering': 'no'
		}
	});
};
