<script lang="ts">
	import type { PageProps } from './$types';

	let { data }: PageProps = $props();

	const stateLabel: Record<string, string> = {
		healthy: 'Healthy',
		suspect: 'Suspect',
		alerting: 'Alerting',
		unknown: 'Unknown (agent stale)'
	};

	function targetName(target: (typeof data.rows)[number]['target']): string {
		return target.url ?? `${target.host}:${target.port}`;
	}

	function formatUptime(slo: (typeof data.rows)[number]['slo']): string {
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
		<form method="POST" action="?/logout">
			<button type="submit">Log out</button>
		</form>
	</header>

	{#if data.loadError}
		<p class="error">{data.loadError}</p>
	{/if}

	{#if data.rows.length === 0 && !data.loadError}
		<p>No targets registered yet.</p>
	{:else}
		<table>
			<thead>
				<tr>
					<th>Target</th>
					<th>Status</th>
					<th>Last checked</th>
					<th>Uptime ({data.rows[0]?.slo?.window_days ?? 30}d)</th>
				</tr>
			</thead>
			<tbody>
				{#each data.rows as row (row.target.id)}
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
</style>
