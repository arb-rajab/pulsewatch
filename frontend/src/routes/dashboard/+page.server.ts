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

// TargetSloResponse mirrors operatorapi.targetSloResponse
// (backend/internal/operatorapi/slo.go) / openapi.yaml's TargetSlo schema
// verbatim — this session's own new endpoint.
export interface TargetSloResponse {
	target_id: string;
	window_days: number;
	window_start: string;
	window_end: string;
	expected_checks: number;
	success_count: number;
	failure_count: number;
	unknown_count: number;
	uptime_pct: number;
	slo_target_pct: number;
	error_budget_consumed_pct: number;
}

export interface DashboardRow {
	target: TargetResponse;
	status: TargetStatusResponse | null;
	statusError: string | null;
	slo: TargetSloResponse | null;
	sloError: string | null;
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
	// pagination reasoning) — one status call and one slo call per target,
	// matching openapi.yaml's per-target /status and /slo contracts exactly
	// rather than inventing a bulk endpoint the spec doesn't define.
	//
	// The /slo call below never passes window_days or slo_target_pct — it
	// always takes the backend's own default (30 days, 99.9%). This is
	// Session 12's own "pick one fixed rolling window" scope decision, made
	// at this call site rather than in the backend contract itself: the
	// already-validated openapi.yaml (Session 3.5) makes window_days a
	// request-time query parameter, the same "lens over existing rollup
	// data" pattern slo_target_pct already used — reimplementing the backend
	// endpoint to refuse that parameter would mean silently diverging from a
	// committed, redocly-linted spec. The dashboard simply never exposes a
	// picker for it (see docs/project-memory/04-data-model.md's Session 12
	// addendum for the full reasoning).
	const rows: DashboardRow[] = await Promise.all(
		targets.map(async (target): Promise<DashboardRow> => {
			const [statusRes, sloRes] = await Promise.all([
				backendFetch(fetch, `/api/v1/targets/${target.id}/status`, sessionToken),
				backendFetch(fetch, `/api/v1/targets/${target.id}/slo`, sessionToken)
			]);

			const status = statusRes.ok ? ((await statusRes.json()) as TargetStatusResponse) : null;
			const statusError = statusRes.ok ? null : `status unavailable (${statusRes.status})`;

			const slo = sloRes.ok ? ((await sloRes.json()) as TargetSloResponse) : null;
			const sloError = sloRes.ok ? null : `slo unavailable (${sloRes.status})`;

			return { target, status, statusError, slo, sloError };
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
