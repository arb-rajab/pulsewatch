import { fail, redirect } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import { backendErrorMessage, backendFetch, SESSION_COOKIE_NAME } from '$lib/server/backend';

// DeviceTokenResponse mirrors alerting.DeviceTokenRecord
// (backend/internal/alerting/devicetokens.go) / openapi.yaml's DeviceToken
// schema verbatim — deliberately has no `token` property, the same
// discipline TargetResponse and AlertChannel already follow for their own
// sensitive values (05-api-contracts.md, FR-023).
export interface DeviceTokenResponse {
	id: string;
	provider: string;
	platform: string;
	created_at: string;
	last_registered_at: string;
	last_delivered_at: string | null;
	revoked_at: string | null;
	dead_at: string | null;
	dead_reason: string | null;
}

// load is B-017's real, gated read: GET /api/v1/device-tokens, the same
// endpoint the mobile app already uses to display its own registration —
// this route is the first thing to surface it to an operator through the
// dashboard rather than only `curl`/the app (12-session-handoff.md, "No
// dashboard surface for device_tokens").
export const load: PageServerLoad = async ({ cookies, fetch }) => {
	const sessionToken = cookies.get(SESSION_COOKIE_NAME);
	if (!sessionToken) {
		redirect(303, '/login');
	}

	const res = await backendFetch(fetch, '/api/v1/device-tokens', sessionToken);
	if (res.status === 401) {
		cookies.delete(SESSION_COOKIE_NAME, { path: '/' });
		redirect(303, '/login');
	}
	if (!res.ok) {
		return {
			devices: [] as DeviceTokenResponse[],
			loadError: `Could not load devices (backend returned ${res.status}).`
		};
	}
	const devices = (await res.json()) as DeviceTokenResponse[];
	return { devices, loadError: null as string | null };
};

export const actions: Actions = {
	// revoke calls DELETE /api/v1/device-tokens/{id} — Session 18's existing
	// sign-out path (alerting.RevokeDeviceToken). It has no request body, so
	// unlike the POST/PATCH/PUT actions elsewhere in this frontend it carries
	// no Content-Type header: the backend's RequireJSONContentType CSRF
	// mitigation is deliberately not wired onto any DELETE route
	// (operatorapi/router.go), the same as /targets/{id} and
	// /alert-channels/{id}.
	revoke: async ({ request, cookies, fetch }) => {
		const sessionToken = cookies.get(SESSION_COOKIE_NAME);
		if (!sessionToken) {
			redirect(303, '/login');
		}

		const form = await request.formData();
		const deviceId = String(form.get('device_id') ?? '');
		if (!deviceId) {
			return fail(400, { error: 'Missing device id.' });
		}

		const res = await backendFetch(fetch, `/api/v1/device-tokens/${deviceId}`, sessionToken, {
			method: 'DELETE'
		});
		if (res.status === 401) {
			cookies.delete(SESSION_COOKIE_NAME, { path: '/' });
			redirect(303, '/login');
		}
		// A 404 (already revoked, or never existed) is treated as success here:
		// the operator's intent — this device should no longer receive pushes —
		// is already satisfied, matching UnregisterDeviceToken's own doc comment
		// that a 404 deliberately never distinguishes those cases.
		if (!res.ok && res.status !== 404) {
			const message = await backendErrorMessage(res, 'Could not revoke device.');
			return fail(res.status, { error: message });
		}

		return { revoked: true };
	}
};
