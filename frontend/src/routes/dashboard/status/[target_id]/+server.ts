import { json, error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { backendFetch, SESSION_COOKIE_NAME } from '$lib/server/backend';
import type { TargetStatusResponse } from '../../+page.server';

// GET /dashboard/status/{target_id} is the live-push dashboard's own
// re-fetch endpoint: when the browser's SSE connection (dashboard/events)
// tells it a target's state may have changed, it calls this route to get
// the real, authoritative TargetStatusResponse — the exact same
// GET /api/v1/targets/{id}/status computation +page.server.ts's own load
// function already calls (alerting.DisplayState's agent-staleness overlay
// included) — rather than this frontend trying to duplicate that
// computation client-side from the SSE event's own smaller payload
// (target_id/state/streak only). The SSE event is a "something changed,
// go refetch" signal, not a replacement data source.
export const GET: RequestHandler = async ({ params, cookies, fetch }) => {
	const sessionToken = cookies.get(SESSION_COOKIE_NAME);
	if (!sessionToken) {
		error(401, 'unauthorized');
	}

	const res = await backendFetch(fetch, `/api/v1/targets/${params.target_id}/status`, sessionToken);
	if (!res.ok) {
		error(res.status, 'could not load target status');
	}
	const status = (await res.json()) as TargetStatusResponse;
	return json(status);
};
