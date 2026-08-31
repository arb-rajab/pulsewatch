<script lang="ts">
	import type { PageProps } from './$types';

	let { form }: PageProps = $props();
</script>

<svelte:head>
	<title>Log in — pulsewatch</title>
</svelte:head>

<main>
	<h1>pulsewatch</h1>
	<form method="POST">
		<label>
			Email
			<input type="email" name="email" value={form?.email ?? ''} required autocomplete="username" />
		</label>
		<label>
			Password
			<input type="password" name="password" required autocomplete="current-password" />
		</label>
		{#if form?.error}
			<p class="error">{form.error}</p>
		{/if}
		<button type="submit">Log in</button>
	</form>
</main>

<style>
	main {
		max-width: 20rem;
		margin: 4rem auto;
		font-family: system-ui, sans-serif;
	}
	form {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}
	label {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		font-size: 0.9rem;
	}
	input {
		padding: 0.4rem;
		font-size: 1rem;
	}
	button {
		padding: 0.5rem;
		font-size: 1rem;
		cursor: pointer;
	}
	.error {
		color: #cf222e;
		margin: 0;
	}
</style>
