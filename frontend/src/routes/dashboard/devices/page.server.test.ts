import { describe, expect, it, vi } from 'vitest';
import { isActionFailure, isRedirect } from '@sveltejs/kit';
import { load, actions, type DeviceTokenResponse } from './+page.server';
import { SESSION_COOKIE_NAME } from '$lib/server/backend';

// The exported `load` is typed against the generated `PageServerLoad`,
// whose default `OutputData` generic widens to include `void` (a
// SvelteKit typegen quirk unrelated to this route's actual behavior — see
// the sibling dashboard/+page.server.ts, which has the identical
// `export const load: PageServerLoad = ...` shape). Calling it directly
// here, rather than through the generated per-route proxy, needs the real
// shape spelled out once so the assertions below can be type-checked.
interface LoadResult {
	devices: DeviceTokenResponse[];
	loadError: string | null;
}
async function callLoad(event: unknown): Promise<LoadResult> {
	return (await load(event as never)) as unknown as LoadResult;
}

function cookieJar(token: string | undefined) {
	const store = new Map<string, string>();
	if (token) store.set(SESSION_COOKIE_NAME, token);
	return {
		get: (name: string) => store.get(name),
		delete: vi.fn((name: string) => store.delete(name))
	};
}

const device = {
	id: 'dev-1',
	provider: 'fcm',
	platform: 'android',
	created_at: '2026-09-01T00:00:00Z',
	last_registered_at: '2026-09-10T00:00:00Z',
	last_delivered_at: '2026-09-10T01:00:00Z',
	revoked_at: null,
	dead_at: null,
	dead_reason: null
};

describe('dashboard/devices load', () => {
	it('redirects to /login when there is no session cookie', async () => {
		const cookies = cookieJar(undefined);
		const fetchMock = vi.fn();

		try {
			await load({ cookies, fetch: fetchMock } as never);
			expect.unreachable('load should have redirected');
		} catch (e) {
			expect(isRedirect(e)).toBe(true);
			expect((e as { location: string }).location).toBe('/login');
		}
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it('returns the operator devices list on a successful backend call', async () => {
		const cookies = cookieJar('session-token');
		const fetchMock = vi.fn(async () => new Response(JSON.stringify([device]), { status: 200 }));

		const result = await callLoad({ cookies, fetch: fetchMock });

		expect(result.loadError).toBeNull();
		expect(result.devices).toEqual([device]);
		expect(fetchMock).toHaveBeenCalledWith(
			expect.stringContaining('/api/v1/device-tokens'),
			expect.any(Object)
		);
	});

	it('clears the cookie and redirects to /login on a 401', async () => {
		const cookies = cookieJar('stale-token');
		const fetchMock = vi.fn(async () => new Response(null, { status: 401 }));

		try {
			await load({ cookies, fetch: fetchMock } as never);
			expect.unreachable('load should have redirected');
		} catch (e) {
			expect(isRedirect(e)).toBe(true);
		}
		expect(cookies.delete).toHaveBeenCalledWith(SESSION_COOKIE_NAME, { path: '/' });
	});

	it('surfaces a loadError instead of throwing on a non-401 backend failure', async () => {
		const cookies = cookieJar('session-token');
		const fetchMock = vi.fn(async () => new Response(null, { status: 503 }));

		const result = await callLoad({ cookies, fetch: fetchMock });

		expect(result.devices).toEqual([]);
		expect(result.loadError).toContain('503');
	});
});

function formRequest(fields: Record<string, string>) {
	const form = new FormData();
	for (const [key, value] of Object.entries(fields)) form.append(key, value);
	return { formData: async () => form };
}

describe('dashboard/devices revoke action', () => {
	it('fails with 400 when no device_id is given', async () => {
		const cookies = cookieJar('session-token');
		const request = formRequest({});
		const fetchMock = vi.fn();

		const result = await actions.revoke({ request, cookies, fetch: fetchMock } as never);

		expect(isActionFailure(result)).toBe(true);
		expect((result as { status: number }).status).toBe(400);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it('calls DELETE /api/v1/device-tokens/{id} and reports success', async () => {
		const cookies = cookieJar('session-token');
		const request = formRequest({ device_id: 'dev-1' });
		const fetchMock = vi.fn(async () => new Response(null, { status: 204 }));

		const result = await actions.revoke({ request, cookies, fetch: fetchMock } as never);

		expect(result).toEqual({ revoked: true });
		expect(fetchMock).toHaveBeenCalledWith(
			expect.stringContaining('/api/v1/device-tokens/dev-1'),
			expect.objectContaining({ method: 'DELETE' })
		);
	});

	it('treats a 404 as success (already revoked)', async () => {
		const cookies = cookieJar('session-token');
		const request = formRequest({ device_id: 'dev-1' });
		const fetchMock = vi.fn(async () => new Response(null, { status: 404 }));

		const result = await actions.revoke({ request, cookies, fetch: fetchMock } as never);

		expect(result).toEqual({ revoked: true });
	});

	it('fails with the backend status and message on a real error', async () => {
		const cookies = cookieJar('session-token');
		const request = formRequest({ device_id: 'dev-1' });
		const fetchMock = vi.fn(
			async () =>
				new Response(JSON.stringify({ error: { code: 'database_unavailable', message: 'nope' } }), {
					status: 503
				})
		);

		const result = await actions.revoke({ request, cookies, fetch: fetchMock } as never);

		expect(isActionFailure(result)).toBe(true);
		expect((result as { status: number }).status).toBe(503);
		expect((result as { data: { error: string } }).data.error).toBe('nope');
	});

	it('clears the cookie and redirects to /login on a 401', async () => {
		const cookies = cookieJar('stale-token');
		const request = formRequest({ device_id: 'dev-1' });
		const fetchMock = vi.fn(async () => new Response(null, { status: 401 }));

		try {
			await actions.revoke({ request, cookies, fetch: fetchMock } as never);
			expect.unreachable('action should have redirected');
		} catch (e) {
			expect(isRedirect(e)).toBe(true);
		}
		expect(cookies.delete).toHaveBeenCalledWith(SESSION_COOKIE_NAME, { path: '/' });
	});
});
