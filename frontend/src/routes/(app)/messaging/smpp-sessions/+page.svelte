<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import type { SessionStats } from '$lib/types/api';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let sessions = $state<SessionStats[]>([]);
	let autoRefresh = $state(true);
	let timer: any = null;

	const totalBound = $derived(sessions.filter((s) => s.bound).length);
	const totalInFlight = $derived(sessions.reduce((acc, s) => acc + s.in_flight, 0));
	const totalTPS = $derived(sessions.reduce((acc, s) => acc + s.current_tps, 0));
	const totalSuccess = $derived(sessions.reduce((acc, s) => acc + s.success_count, 0));
	const totalFailures = $derived(sessions.reduce((acc, s) => acc + s.failure_count, 0));

	async function loadSessions(isPolling = false) {
		if (!isPolling) loading = true;
		error = null;
		try {
			const res = await apiClient.getSMPPSessions();
			sessions = res.data.sessions || [];
		} catch (err: any) {
			error = err.message || 'Failed to load SMPP sessions';
		} finally {
			if (!isPolling) loading = false;
		}
	}

	onMount(() => {
		loadSessions();
		timer = setInterval(() => {
			if (autoRefresh) {
				loadSessions(true);
			}
		}, 4000);
	});

	onDestroy(() => {
		if (timer) clearInterval(timer);
	});
</script>

<svelte:head>
	<title>SMPP Sessions Monitor — Znero</title>
</svelte:head>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-xl font-bold text-slate-900 dark:text-white">SMPP Sessions Monitor</h1>
			<p class="text-xs text-slate-500 dark:text-slate-400">
				Real-time connection states, sliding window in-flight flow, and carrier TPS metrics.
			</p>
		</div>

		<div class="flex items-center gap-3">
			<label class="flex items-center gap-2 text-xs text-slate-600 dark:text-slate-400 cursor-pointer">
				<input
					type="checkbox"
					bind:checked={autoRefresh}
					class="rounded border-slate-300 text-indigo-600 focus:ring-indigo-500 dark:border-slate-700"
				/>
				<span>Auto-refresh (4s)</span>
			</label>

			<button
				type="button"
				onclick={() => loadSessions()}
				class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200 dark:hover:bg-slate-700"
			>
				Refresh
			</button>
		</div>
	</div>

	<!-- Stats Summary Grid -->
	<div class="grid grid-cols-2 gap-4 sm:grid-cols-5 text-xs">
		<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<span class="block text-[11px] font-semibold uppercase text-slate-400">Active Binds</span>
			<span class="mt-2 block text-2xl font-bold text-slate-900 dark:text-white font-mono">
				{totalBound} / {sessions.length}
			</span>
		</div>
		<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<span class="block text-[11px] font-semibold uppercase text-slate-400">In-Flight Queue</span>
			<span class="mt-2 block text-2xl font-bold text-slate-900 dark:text-white font-mono">
				{totalInFlight}
			</span>
		</div>
		<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<span class="block text-[11px] font-semibold uppercase text-slate-400">Total TPS</span>
			<span class="mt-2 block text-2xl font-bold text-indigo-600 dark:text-indigo-400 font-mono">
				{totalTPS.toFixed(1)}
			</span>
		</div>
		<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<span class="block text-[11px] font-semibold uppercase text-slate-400">Success Count</span>
			<span class="mt-2 block text-2xl font-bold text-emerald-600 dark:text-emerald-400 font-mono">
				{totalSuccess.toLocaleString()}
			</span>
		</div>
		<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<span class="block text-[11px] font-semibold uppercase text-slate-400">Failures / Backoffs</span>
			<span class="mt-2 block text-2xl font-bold text-rose-600 font-mono">
				{totalFailures.toLocaleString()}
			</span>
		</div>
	</div>

	<!-- Sessions Table -->
	<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
		{#if loading}
			<div class="p-8 text-center text-xs text-slate-500">Loading session states...</div>
		{:else if error}
			<div class="p-8 text-center text-xs text-rose-600 dark:text-rose-400">{error}</div>
		{:else if sessions.length === 0}
			<div class="p-12 text-center text-xs text-slate-500">
				No active SMPP connections configured in the database.
			</div>
		{:else}
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50 text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-400">
						<tr>
							<th class="px-4 py-3">Connection ID</th>
							<th class="px-4 py-3">Bind State</th>
							<th class="px-4 py-3">In-Flight</th>
							<th class="px-4 py-3">TPS</th>
							<th class="px-4 py-3">Successes</th>
							<th class="px-4 py-3">Failures</th>
							<th class="px-4 py-3">Reconnects</th>
							<th class="px-4 py-3">Last Error Code</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
						{#each sessions as s}
							<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/50">
								<td class="px-4 py-3 font-mono font-medium text-slate-900 dark:text-white">
									{s.connection_id}
								</td>
								<td class="px-4 py-3">
									{#if s.bound}
										<span class="inline-flex items-center gap-1.5 rounded-full bg-emerald-50 px-2.5 py-0.5 text-[11px] font-semibold text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300">
											<span class="h-1.5 w-1.5 rounded-full bg-emerald-500"></span>
											Bound
										</span>
									{:else}
										<span class="inline-flex items-center gap-1.5 rounded-full bg-slate-100 px-2.5 py-0.5 text-[11px] font-semibold text-slate-600 dark:bg-slate-800 dark:text-slate-400">
											<span class="h-1.5 w-1.5 rounded-full bg-slate-400"></span>
											Unbound / Idle
										</span>
									{/if}
								</td>
								<td class="px-4 py-3 font-mono font-bold text-slate-900 dark:text-white">
									{s.in_flight}
								</td>
								<td class="px-4 py-3 font-mono text-indigo-600 dark:text-indigo-400 font-bold">
									{s.current_tps.toFixed(1)}/s
								</td>
								<td class="px-4 py-3 font-mono text-emerald-600 dark:text-emerald-400">
									{s.success_count}
								</td>
								<td class="px-4 py-3 font-mono text-rose-600 font-medium">
									{s.failure_count}
								</td>
								<td class="px-4 py-3 font-mono text-slate-600 dark:text-slate-400">
									{s.reconnect_count}
								</td>
								<td class="px-4 py-3 font-mono text-slate-500">
									{s.last_error_code || 'None'}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</div>
</div>
