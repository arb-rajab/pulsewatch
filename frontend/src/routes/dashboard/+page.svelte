<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import type { PageProps } from './$types';
	import { connectLiveEvents, type LiveEvent } from '$lib/liveEvents';
	import type { TargetStatusResponse } from './+page.server';

	let { data }: PageProps = $props();

	// rows is this page's own live-mutable copy of data.rows (ADR-0010): the
	// SSR load stays the initial-load/fallback path, unmodified — this
	// writable $derived is what live-push events reassign in place so a
	// connected dashboard reflects a real state change without a manual
	// refresh, while still resetting to data.rows whenever a fresh `data`
	// arrives (e.g. a real navigation), so a live-mutated row never
	// survives past the load it came from.
	let rows = $derived(data.rows);

	let liveStatus = $state<'connected' | 'reconnecting'>('reconnecting');

	async function refetchTargetStatus(targetID: string) {
		const res = await fetch(`${resolve('/dashboard/status/[target_id]', { target_id: targetID })}`);
		if (!res.ok) return; // a transient refetch failure just leaves the row as-is until the next event or navigation
		const status = (await res.json()) as TargetStatusResponse;
		rows = rows.map((row) =>
			row.target.id === targetID ? { ...row, status, statusError: null } : row
		);
	}

	function handleLiveEvent(event: LiveEvent) {
		// Both event kinds (target_status, incident) mean the same thing to
		// this page: this target's status may have changed, go re-read the
		// real, authoritative computation (display_state's agent-staleness
		// overlay included) rather than trying to derive it from the smaller
		// event payload — see status/[target_id]/+server.ts's own doc comment.
		void refetchTargetStatus(event.target_id);
	}

	onMount(() => {
		const handle = connectLiveEvents(handleLiveEvent, (status) => {
			liveStatus = status;
		});
		onDestroy(handle.close);
	});

	const stateLabel: Record<string, string> = {
		healthy: 'Healthy',
		suspect: 'Suspect',
		alerting: 'Alerting',
		unknown: 'Unknown (agent stale)'
	};

	function targetName(target: (typeof rows)[number]['target']): string {
		return target.url ?? `${target.host}:${target.port}`;
	}

	function formatUptime(slo: (typeof rows)[number]['slo']): string {
		if (!slo) return '—';
		// A window with zero observed success-or-failure checks (e.g. a
		// target created within the last window_days) reports 100.0 as a
		// documented vacuous-true choice (backend/internal/operatorapi/slo.go)
		// — surfaced here as "no data yet" instead, since "100% uptime" would
		// overstate confidence for a target with nothing observed at all.
		if (slo.success_count + slo.failure_count === 0) return 'no data yet';
		return `${slo.uptime_pct.toFixed(2)}%`;
	}
</script>

<svelte:head>
	<title>Dashboard — pulsewatch</title>
</svelte:head>

<main>
	<header>
		<h1>pulsewatch</h1>
		<nav>
			<span class="live-indicator live-{liveStatus}" title="Live updates: {liveStatus}">
				● {liveStatus === 'connected' ? 'Live' : 'Reconnecting…'}
			</span>
			<a href={resolve('/dashboard/devices')}>Devices</a>
			<form method="POST" action="?/logout">
				<button type="submit">Log out</button>
			</form>
		</nav>
	</header>

	{#if data.loadError}
		<p class="error">{data.loadError}</p>
	{/if}

	{#if rows.length === 0 && !data.loadError}
		<p>No targets registered yet.</p>
	{:else}
		<table>
			<thead>
				<tr>
					<th>Target</th>
					<th>Status</th>
					<th>Last checked</th>
					<th>Uptime ({rows[0]?.slo?.window_days ?? 30}d)</th>
				</tr>
			</thead>
			<tbody>
				{#each rows as row (row.target.id)}
					<tr>
						<td>{targetName(row.target)}</td>
						<td>
							{#if row.status}
								<span class="badge state-{row.status.display_state}">
									{stateLabel[row.status.display_state] ?? row.status.display_state}
								</span>
							{:else}
								<span class="badge state-error">{row.statusError}</span>
							{/if}
						</td>
						<td>{row.status?.last_checked_at ?? '—'}</td>
						<td>
							{#if row.slo}
								{formatUptime(row.slo)}
							{:else}
								<span class="badge state-error">{row.sloError}</span>
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
	nav {
		display: flex;
		align-items: center;
		gap: 1rem;
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
	.state-healthy {
		color: #1a7f37;
		background: #dafbe1;
	}
	.state-suspect {
		color: #9a6700;
		background: #fff8c5;
	}
	.state-alerting {
		color: #cf222e;
		background: #ffebe9;
	}
	.state-unknown,
	.state-error {
		color: #57606a;
		background: #eaeef2;
	}
	.error {
		color: #cf222e;
	}
	.live-indicator {
		font-size: 0.8rem;
		font-weight: 600;
	}
	.live-connected {
		color: #1a7f37;
	}
	.live-reconnecting {
		color: #9a6700;
	}
</style>
