import { describe, expect, it, vi } from 'vitest';
import { GET } from './+server';
import { SESSION_COOKIE_NAME } from '$lib/server/backend';

function cookieJar(token: string | undefined) {
	const store = new Map<string, string>();
	if (token) store.set(SESSION_COOKIE_NAME, token);
	return { get: (name: string) => store.get(name) };
}

describe('GET /dashboard/events', () => {
	it('returns 401 with no session cookie, without calling the backend', async () => {
		const fetchMock = vi.fn();
		const cookies = cookieJar(undefined);
		const request = new Request('http://localhost/dashboard/events');

		const res = await GET({ cookies, fetch: fetchMock, request } as never);

		expect(res.status).toBe(401);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it('streams the real backend SSE response body through unchanged', async () => {
		const stream = new ReadableStream({
			start(controller) {
				controller.enqueue(new TextEncoder().encode('event: incident\ndata: {}\n\n'));
				controller.close();
			}
		});
		const fetchMock = vi.fn(
			async () =>
				new Response(stream, {
					status: 200,
					headers: { 'Content-Type': 'text/event-stream' }
				})
		);
		const cookies = cookieJar('session-token');
		const request = new Request('http://localhost/dashboard/events');

		const res = await GET({ cookies, fetch: fetchMock, request } as never);

		expect(res.status).toBe(200);
		expect(res.headers.get('Content-Type')).toBe('text/event-stream');
		expect(fetchMock).toHaveBeenCalledWith(
			expect.stringContaining('/api/v1/events'),
			expect.objectContaining({ signal: request.signal })
		);
		const body = await res.text();
		expect(body).toContain('event: incident');
	});

	it('surfaces a non-2xx backend response as an error status', async () => {
		const fetchMock = vi.fn(async () => new Response(null, { status: 503 }));
		const cookies = cookieJar('session-token');
		const request = new Request('http://localhost/dashboard/events');

		const res = await GET({ cookies, fetch: fetchMock, request } as never);

		expect(res.status).toBe(503);
	});
});
