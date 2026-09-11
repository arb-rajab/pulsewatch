import { describe, expect, it } from 'vitest';
import { platformLabel, statusLabel, statusOf } from './deviceStatus';
import type { DeviceTokenResponse } from './+page.server';

function device(overrides: Partial<DeviceTokenResponse> = {}): DeviceTokenResponse {
	return {
		id: 'dev-1',
		provider: 'fcm',
		platform: 'android',
		created_at: '2026-09-01T00:00:00Z',
		last_registered_at: '2026-09-10T00:00:00Z',
		last_delivered_at: null,
		revoked_at: null,
		dead_at: null,
		dead_reason: null,
		...overrides
	};
}

describe('statusOf', () => {
	it('reports active when neither revoked_at nor dead_at is set', () => {
		expect(statusOf(device())).toBe('active');
	});

	it('reports dead when dead_at is set', () => {
		expect(
			statusOf(
				device({ dead_at: '2026-09-10T00:00:00Z', dead_reason: 'fcm rejected: UNREGISTERED' })
			)
		).toBe('dead');
	});

	it('reports revoked when revoked_at is set', () => {
		expect(statusOf(device({ revoked_at: '2026-09-10T00:00:00Z' }))).toBe('revoked');
	});

	it('prefers revoked over dead if somehow both are set', () => {
		expect(
			statusOf(device({ revoked_at: '2026-09-10T00:00:00Z', dead_at: '2026-09-09T00:00:00Z' }))
		).toBe('revoked');
	});
});

describe('statusLabel / platformLabel', () => {
	it('has a human label for every DeviceStatus value statusOf can return', () => {
		for (const status of ['active', 'dead', 'revoked'] as const) {
			expect(statusLabel[status]).toBeTruthy();
		}
	});

	it('labels the two real platforms', () => {
		expect(platformLabel.ios).toBe('iOS');
		expect(platformLabel.android).toBe('Android');
	});
});
