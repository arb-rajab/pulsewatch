<script lang="ts">
	import { resolve } from '$app/paths';
	import type { PageProps } from './$types';
	import { platformLabel, statusLabel, statusOf } from './deviceStatus';

	let { data, form }: PageProps = $props();
</script>

<svelte:head>
	<title>Devices — pulsewatch</title>
</svelte:head>

<main>
	<header>
		<h1>Devices</h1>
		<a href={resolve('/dashboard')}>Back to dashboard</a>
	</header>

	{#if data.loadError}
		<p class="error">{data.loadError}</p>
	{/if}

	{#if form?.error}
		<p class="error">{form.error}</p>
	{/if}

	{#if data.devices.length === 0 && !data.loadError}
		<p>No devices registered yet.</p>
	{:else}
		<table>
			<thead>
				<tr>
					<th>Platform</th>
					<th>Registered</th>
					<th>Last delivery</th>
					<th>Status</th>
					<th></th>
				</tr>
			</thead>
			<tbody>
				{#each data.devices as device (device.id)}
					<tr>
						<td>{platformLabel[device.platform] ?? device.platform}</td>
						<td>{device.created_at}</td>
						<td>{device.last_delivered_at ?? 'never'}</td>
						<td>
							<span class="badge status-{statusOf(device)}">
								{statusLabel[statusOf(device)]}
							</span>
							{#if device.dead_at}
								<div class="dead-reason">{device.dead_reason}</div>
							{/if}
						</td>
						<td>
							{#if statusOf(device) === 'active'}
								<form method="POST" action="?/revoke">
									<input type="hidden" name="device_id" value={device.id} />
									<button type="submit">Revoke</button>
								</form>
							{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</main>

<style>
	main {
		max-width: 48rem;
		margin: 2rem auto;
		font-family: system-ui, sans-serif;
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}
	table {
		border-collapse: collapse;
		width: 100%;
	}
	th,
	td {
		text-align: left;
		padding: 0.5rem;
		border-bottom: 1px solid #ddd;
	}
	.badge {
		display: inline-block;
		padding: 0.15rem 0.5rem;
		border-radius: 0.25rem;
		font-size: 0.85rem;
		font-weight: 600;
	}
	.status-active {
		color: #1a7f37;
		background: #dafbe1;
	}
	.status-dead,
	.status-revoked {
		color: #57606a;
		background: #eaeef2;
	}
	.dead-reason {
		font-size: 0.75rem;
		color: #57606a;
	}
	.error {
		color: #cf222e;
	}
</style>
