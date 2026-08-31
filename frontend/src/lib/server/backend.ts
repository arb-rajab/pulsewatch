import { env } from '$env/dynamic/public';

// PUBLIC_API_URL is docker-compose's own existing env var for the backend's
// base URL (Session 4). $env/dynamic/private explicitly excludes any
// PUBLIC_-prefixed variable (SvelteKit reserves that prefix exclusively for
// the public module, regardless of where the value is actually read from),
// so this has to come from $env/dynamic/public even though every backend
// call is made server-side, from SvelteKit's own load functions and form
// actions (see the module doc below) — this module is never imported from
// client-side code, so the value never actually reaches the browser.
const BACKEND_URL = env.PUBLIC_API_URL ?? 'http://localhost:8020';

// SESSION_COOKIE_NAME matches operatorauth.SessionCookieName
// (backend/internal/operatorauth/session.go) exactly — this frontend never
// mints or verifies a session itself, only carries the backend's own opaque
// token value back and forth.
export const SESSION_COOKIE_NAME = 'pulsewatch_session';

// backendFetch is the one place a request ever leaves this frontend process
// for the real Go/Gin API (05-api-contracts.md). It is always a server-to-
// server call (invoked only from +page.server.ts load functions and form
// actions, never from a <script> that runs in the browser) — the backend
// enables no CORS policy by design (operatorapi/middleware.go), so a direct
// browser-to-backend fetch across the different dev ports would be blocked
// from reading the response anyway. Session 8's operatorSession cookie is
// carried as a plain Cookie header here, not through the browser's own
// cookie jar: the browser only ever holds a cookie issued by this frontend
// origin (see routes/login/+page.server.ts), which this function re-attaches
// to the outbound backend request on every gated call.
export async function backendFetch(
	fetchFn: typeof fetch,
	path: string,
	sessionToken: string | undefined,
	init: RequestInit = {}
): Promise<Response> {
	const headers = new Headers(init.headers);
	if (sessionToken) {
		headers.set('Cookie', `${SESSION_COOKIE_NAME}=${sessionToken}`);
	}
	return fetchFn(`${BACKEND_URL}${path}`, { ...init, headers });
}

export interface BackendErrorEnvelope {
	error: { code: string; message: string; field: string | null };
}

// backendErrorMessage best-efforts a human-readable message out of
// 05-api-contracts.md's one JSON error envelope, without ever throwing on a
// response body that doesn't match it.
export async function backendErrorMessage(res: Response, fallback: string): Promise<string> {
	try {
		const body = (await res.json()) as BackendErrorEnvelope;
		return body?.error?.message ?? fallback;
	} catch {
		return fallback;
	}
}

// parseSessionCookie reads the real session token and its Max-Age out of
// POST /auth/login's real Set-Cookie response headers. The frontend
// deliberately does not forward that header to the browser verbatim: the
// backend issued it for its own origin, and this SSR proxy needs a cookie
// scoped to the frontend's own origin instead (the one the browser will
// actually send back on every subsequent page request) — carrying over the
// same name, value, and lifetime, not the same Domain.
export function parseSessionCookie(
	setCookieHeaders: string[]
): { token: string; maxAge: number } | null {
	for (const header of setCookieHeaders) {
		const [nameValue, ...attrs] = header.split(';').map((part) => part.trim());
		const eq = nameValue.indexOf('=');
		if (eq === -1) continue;
		const name = nameValue.slice(0, eq);
		if (name !== SESSION_COOKIE_NAME) continue;
		const token = nameValue.slice(eq + 1);

		// operatorauth.SessionTTL (24h) is the fallback if Max-Age is somehow
		// absent — real responses always carry one (auth.go's setSessionCookie).
		let maxAge = 60 * 60 * 24;
		for (const attr of attrs) {
			const attrEq = attr.indexOf('=');
			if (attrEq === -1) continue;
			const key = attr.slice(0, attrEq).toLowerCase();
			const value = attr.slice(attrEq + 1);
			if (key === 'max-age') {
				maxAge = Number(value);
			}
		}
		return { token, maxAge };
	}
	return null;
}
