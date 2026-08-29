import { describe, expect, it } from 'vitest';
import { GET } from './health/+server';

describe('GET /health', () => {
	it('returns 200 with status ok', async () => {
		const response = GET();
		const body = await response.json();

		expect(response.status).toBe(200);
		expect(body).toEqual({ status: 'ok' });
	});
});
