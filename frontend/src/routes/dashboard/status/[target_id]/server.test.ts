import { describe, expect, it, vi } from 'vitest';
import { isHttpError } from '@sveltejs/kit';
import { GET } from './+server';
import { SESSION_COOKIE_NAME } from '$lib/server/backend';

function cookieJar(token: string | undefined) {
	const store = new Map<string, string>();
	if (token) store.set(SESSION_COOKIE_NAME, token);
	return { get: (name: string) => store.get(name) };
}

const status = {
	target_id: 'target-1',
	display_state: 'alerting',
	raw_state: 'alerting',
	streak: 3,
	last_checked_at: '2026-09-21T00:00:00Z',
	next_due_at: '2026-09-21T00:01:00Z',
	agent_id: null,
	agent_stale: null,
	open_incident: { id: 42, opened_at: '2026-09-21T00:00:00Z' }
};

describe('GET /dashboard/status/[target_id]', () => {
	it('throws a 401 with no session cookie, without calling the backend', async () => {
		const fetchMock = vi.fn();
		const cookies = cookieJar(undefined);

		try {
			await GET({ params: { target_id: 'target-1' }, cookies, fetch: fetchMock } as never);
			expect.unreachable('expected a 401 error');
		} catch (e) {
			expect(isHttpError(e)).toBe(true);
			expect((e as { status: number }).status).toBe(401);
		}
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it('returns the real backend TargetStatusResponse for a live-push refetch', async () => {
		const fetchMock = vi.fn(async () => new Response(JSON.stringify(status), { status: 200 }));
		const cookies = cookieJar('session-token');

		const res = await GET({
			params: { target_id: 'target-1' },
			cookies,
			fetch: fetchMock
		} as never);

		expect(await res.json()).toEqual(status);
		expect(fetchMock).toHaveBeenCalledWith(
			expect.stringContaining('/api/v1/targets/target-1/status'),
			expect.any(Object)
		);
	});

	it('surfaces a backend failure as the same status code', async () => {
		const fetchMock = vi.fn(async () => new Response(null, { status: 404 }));
		const cookies = cookieJar('session-token');

		try {
			await GET({ params: { target_id: 'missing' }, cookies, fetch: fetchMock } as never);
			expect.unreachable('expected a 404 error');
		} catch (e) {
			expect(isHttpError(e)).toBe(true);
			expect((e as { status: number }).status).toBe(404);
		}
	});
});
