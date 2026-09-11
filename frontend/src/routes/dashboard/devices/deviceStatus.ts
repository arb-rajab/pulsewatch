import type { DeviceTokenResponse } from './+page.server';

export type DeviceStatus = 'revoked' | 'dead' | 'active';

// revoked_at and dead_at are mutually exclusive in practice (both write
// paths clear the other on re-registration — backend/internal/alerting/
// devicetokens.go), but revoked is checked first since it reflects the
// operator's own most recent action on this row.
export function statusOf(device: DeviceTokenResponse): DeviceStatus {
	if (device.revoked_at) return 'revoked';
	if (device.dead_at) return 'dead';
	return 'active';
}

export const statusLabel: Record<DeviceStatus, string> = {
	revoked: 'Revoked',
	dead: 'Dead',
	active: 'Active'
};

export const platformLabel: Record<string, string> = {
	ios: 'iOS',
	android: 'Android'
};
