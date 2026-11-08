<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import type { QueueBacklogStats } from '$lib/types/api';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let stats = $state<QueueBacklogStats | null>(null);
	let timer: any = null;

	async function loadQueueStats(isPolling = false) {
		if (!isPolling) loading = true;
		error = null;
		try {
			const res = await apiClient.getQueueStatus();
			stats = res.data;
		} catch (err: any) {
			error = err.message || 'Failed to load queue status';
		} finally {
			if (!isPolling) loading = false;
		}
	}

	onMount(() => {
		loadQueueStats();
		timer = setInterval(() => {
			loadQueueStats(true);
		}, 3000);
	});

	onDestroy(() => {
		if (timer) clearInterval(timer);
	});
</script>

<svelte:head>
	<title>Queue Backlog Monitor — Znero</title>
</svelte:head>

<div class="space-y-6">
	<!-- Header -->
	<div class="flex items-center justify-between">
		<div>
			<h1 class="text-xl font-bold text-slate-900 dark:text-white">Transactional Outbox Monitor</h1>
			<p class="text-xs text-slate-500 dark:text-slate-400">
				Live queue depth, worker concurrency locks, and latency tracking for asynchronous outbound SMS dispatch.
			</p>
		</div>

		<button
			type="button"
			onclick={() => loadQueueStats()}
			class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
		>
			Refresh Now
		</button>
	</div>

	{#if loading && !stats}
		<div class="p-12 text-center text-xs text-slate-500">Checking queue status...</div>
	{:else if error}
		<div class="rounded-xl border border-rose-200 bg-rose-50 p-6 text-center text-xs text-rose-800 dark:border-rose-900 dark:bg-rose-950/40 dark:text-rose-300">
			{error}
		</div>
	{:else if stats}
		<!-- Stats Grid -->
		<div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
			<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<span class="block text-xs font-semibold uppercase tracking-wider text-slate-400">Pending Backlog</span>
				<div class="mt-3 flex items-baseline gap-2">
					<span class="text-3xl font-bold font-mono text-slate-900 dark:text-white">{stats.pending_count}</span>
					<span class="text-xs text-slate-500">messages waiting</span>
				</div>
				<p class="mt-2 text-xs text-slate-500">
					Awaiting outbox worker lock acquisition and route resolution.
				</p>
			</div>

			<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<span class="block text-xs font-semibold uppercase tracking-wider text-slate-400">Currently Sending</span>
				<div class="mt-3 flex items-baseline gap-2">
					<span class="text-3xl font-bold font-mono text-indigo-600 dark:text-indigo-400">{stats.sending_count}</span>
					<span class="text-xs text-slate-500">locked by workers</span>
				</div>
				<p class="mt-2 text-xs text-slate-500">
					Dispatched over SMPP sliding windows to carrier SMSCs.
				</p>
			</div>

			<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<span class="block text-xs font-semibold uppercase tracking-wider text-slate-400">Oldest Backlog Item</span>
				<div class="mt-3 flex items-baseline gap-2">
					<span class="text-3xl font-bold font-mono {stats.oldest_age_seconds > 60 ? 'text-rose-600' : 'text-emerald-600 dark:text-emerald-400'}">
						{stats.oldest_age_seconds.toFixed(1)}s
					</span>
					<span class="text-xs text-slate-500">age</span>
				</div>
				<p class="mt-2 text-xs text-slate-500">
					{#if stats.oldest_pending_time}
						First queued at {new Date(stats.oldest_pending_time).toLocaleTimeString()}
					{:else}
						Queue is fully drained. Zero backlog.
					{/if}
				</p>
			</div>
		</div>

		<!-- Architectural Safety Box -->
		<div class="rounded-2xl border border-slate-200 bg-slate-50/50 p-6 dark:border-slate-800 dark:bg-slate-800/20 text-xs text-slate-600 dark:text-slate-400 space-y-2">
			<h4 class="text-sm font-bold text-slate-900 dark:text-white">Guaranteed Delivery & Concurrency Safety</h4>
			<p>
				The message outbox employs transactional leases (<code class="font-mono text-slate-800 dark:text-slate-200">locked_until</code>, <code class="font-mono text-slate-800 dark:text-slate-200">locked_by</code>).
				If a worker crashes while a message is in flight, the lock lease expires and the safety reaper recovers the item.
			</p>
			<p>
				Ambiguous outcomes (network timeouts, socket crashes after write) transition messages to <strong class="text-slate-800 dark:text-slate-200">UNKNOWN</strong>
				and are strictly <strong class="text-rose-600 dark:text-rose-400">NEVER</strong> auto-resent, preventing duplicate carrier charges and repeated customer SMS receipts.
			</p>
		</div>
	{/if}
</div>

