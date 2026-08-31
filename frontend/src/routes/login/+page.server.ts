import { fail, redirect } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import {
	backendErrorMessage,
	backendFetch,
	parseSessionCookie,
	SESSION_COOKIE_NAME
} from '$lib/server/backend';

// If the browser already carries a frontend-issued session cookie, skip
// straight to the dashboard rather than showing the login form again — the
// dashboard's own load function is what actually re-validates it against
// the backend (a stale/expired cookie there redirects back here), so this
// is purely a UX shortcut, not a second place auth is decided.
export const load: PageServerLoad = ({ cookies }) => {
	if (cookies.get(SESSION_COOKIE_NAME)) {
		redirect(303, '/dashboard');
	}
};

export const actions: Actions = {
	default: async ({ request, cookies, fetch }) => {
		const form = await request.formData();
		const email = String(form.get('email') ?? '').trim();
		const password = String(form.get('password') ?? '');

		if (!email || !password) {
			return fail(400, { error: 'Email and password are required.', email });
		}

		const res = await backendFetch(fetch, '/api/v1/auth/login', undefined, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ email, password })
		});

		if (!res.ok) {
			const message = await backendErrorMessage(res, 'Login failed.');
			return fail(res.status, { error: message, email });
		}

		const issued = parseSessionCookie(res.headers.getSetCookie());
		if (!issued) {
			return fail(502, { error: 'Login succeeded but the backend issued no session.', email });
		}

		cookies.set(SESSION_COOKIE_NAME, issued.token, {
			path: '/',
			httpOnly: true,
			secure: true,
			sameSite: 'strict',
			maxAge: issued.maxAge
		});

		redirect(303, '/dashboard');
	}
};
