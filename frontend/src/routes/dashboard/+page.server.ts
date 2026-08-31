import { redirect } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import { backendFetch, SESSION_COOKIE_NAME } from '$lib/server/backend';

// TargetResponse mirrors operatorapi.targetResponse
// (backend/internal/operatorapi/targets.go) / openapi.yaml's Target schema.
export interface TargetResponse {
	id: string;
	type: string;
	url: string | null;
	host: string | null;
	port: number | null;
	interval_seconds: number;
	failure_threshold: number;
	timeout_seconds: number;
	agent_id: string | null;
	created_at: string;
}

// TargetStatusResponse mirrors operatorapi.targetStatusResponse
// (backend/internal/operatorapi/status.go) / openapi.yaml's TargetStatus
// schema verbatim — this session's own new endpoint.
export interface TargetStatusResponse {
	target_id: string;
	display_state: string;
	raw_state: string;
	streak: number;
	last_checked_at: string | null;
	next_due_at: string;
	agent_id: string | null;
	agent_stale: boolean | null;
	open_incident: { id: number; opened_at: string } | null;
}

export interface DashboardRow {
	target: TargetResponse;
	status: TargetStatusResponse | null;
	statusError: string | null;
}

// load is this session's real, gated dashboard read: it never computes
// status itself, only calls GET /targets and GET /targets/{id}/status — the
// real Gin handlers wired behind RequireOperator that read through
// alerting.DisplayState/agentauth.IsStale. A missing or backend-rejected
// session redirects to /login; the backend's 401, not cookie presence
// alone, is what actually gates this page (Session 8's auth reused as-is,
// never re-implemented or weakened here).
export const load: PageServerLoad = async ({ cookies, fetch }) => {
	const sessionToken = cookies.get(SESSION_COOKIE_NAME);
	if (!sessionToken) {
		redirect(303, '/login');
	}

	const listRes = await backendFetch(fetch, '/api/v1/targets', sessionToken);
	if (listRes.status === 401) {
		cookies.delete(SESSION_COOKIE_NAME, { path: '/' });
		redirect(303, '/login');
	}
	if (!listRes.ok) {
		return {
			rows: [] as DashboardRow[],
			loadError: `Could not load targets (backend returned ${listRes.status}).`
		};
	}
	const targets = (await listRes.json()) as TargetResponse[];

	// Small, bounded scale (~5-10 targets, 05-api-contracts.md's own
	// pagination reasoning) — one status call per target, matching
	// openapi.yaml's per-target /status contract exactly rather than
	// inventing a bulk endpoint the spec doesn't define.
	const rows: DashboardRow[] = await Promise.all(
		targets.map(async (target): Promise<DashboardRow> => {
			const statusRes = await backendFetch(
				fetch,
				`/api/v1/targets/${target.id}/status`,
				sessionToken
			);
			if (!statusRes.ok) {
				return { target, status: null, statusError: `status unavailable (${statusRes.status})` };
			}
			return {
				target,
				status: (await statusRes.json()) as TargetStatusResponse,
				statusError: null
			};
		})
	);

	return { rows, loadError: null as string | null };
};

export const actions: Actions = {
	logout: async ({ cookies, fetch }) => {
		const sessionToken = cookies.get(SESSION_COOKIE_NAME);
		await backendFetch(fetch, '/api/v1/auth/logout', sessionToken, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' }
		});
		cookies.delete(SESSION_COOKIE_NAME, { path: '/' });
		redirect(303, '/login');
	}
};
