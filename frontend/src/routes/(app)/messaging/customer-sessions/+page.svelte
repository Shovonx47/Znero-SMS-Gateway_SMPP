<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import type { CustomerSessionInfo } from '$lib/types/api';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let successMsg = $state<string | null>(null);
	let sessions = $state<CustomerSessionInfo[]>([]);
	let autoRefresh = $state(true);
	let timer: any = null;
	let disconnectingID = $state<string | null>(null);

	const totalActive = $derived(sessions.filter((s) => s.bound).length);
	const totalInFlight = $derived(sessions.reduce((acc, s) => acc + s.in_flight, 0));

	async function loadSessions(isPolling = false) {
		if (!isPolling) loading = true;
		error = null;
		try {
			const res = await apiClient.getCustomerSMPPSessions();
			sessions = res.data.sessions || [];
		} catch (err: any) {
			error = err.message || 'Failed to load customer SMPP sessions';
		} finally {
			if (!isPolling) loading = false;
		}
	}

	async function disconnectSession(sessionID: string) {
		if (!confirm('Are you sure you want to administratively disconnect this customer session?')) return;
		disconnectingID = sessionID;
		error = null;
		successMsg = null;
		try {
			await apiClient.disconnectCustomerSMPPSession(sessionID);
			successMsg = 'Session disconnected successfully';
			await loadSessions();
		} catch (err: any) {
			error = err.message || 'Failed to disconnect session';
		} finally {
			disconnectingID = null;
		}
	}

	onMount(() => {
		loadSessions();
		timer = setInterval(() => {
			if (autoRefresh) loadSessions(true);
		}, 5000);
	});

	onDestroy(() => {
		if (timer) clearInterval(timer);
	});
</script>

<svelte:head>
	<title>Customer SMPP Sessions â€” Operations</title>
</svelte:head>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Customer SMPP Sessions</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Monitor active customer ESME binds connected to the Znero Customer SMPP Server.
			</p>
		</div>
		<div class="flex items-center gap-3">
			<label class="flex items-center gap-2 text-xs text-slate-600 dark:text-slate-300">
				<input type="checkbox" bind:checked={autoRefresh} class="rounded border-slate-300 text-indigo-600" />
				Auto-refresh (5s)
			</label>
			<button
				type="button"
				onclick={() => loadSessions()}
				class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-medium text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
			>
				Refresh
			</button>
		</div>
	</div>

	{#if error}
		<div class="rounded-lg bg-rose-50 p-4 text-sm text-rose-700 dark:bg-rose-950/40 dark:text-rose-300">{error}</div>
	{/if}
	{#if successMsg}
		<div class="rounded-lg bg-emerald-50 p-4 text-sm text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300">{successMsg}</div>
	{/if}

	<!-- KPI Summary -->
	<div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
		<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="text-xs text-slate-500">ACTIVE CUSTOMER BINDS</div>
			<div class="mt-2 text-2xl font-bold text-slate-900 dark:text-white">{totalActive}</div>
		</div>
		<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="text-xs text-slate-500">TOTAL SESSIONS</div>
			<div class="mt-2 text-2xl font-bold text-indigo-600 dark:text-indigo-400">{sessions.length}</div>
		</div>
		<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="text-xs text-slate-500">CURRENT IN-FLIGHT PDUS</div>
			<div class="mt-2 text-2xl font-bold text-amber-600 dark:text-amber-400">{totalInFlight}</div>
		</div>
		<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="text-xs text-slate-500">LISTENING PORT</div>
			<div class="mt-2 text-2xl font-bold font-mono text-emerald-600 dark:text-emerald-400">:2776</div>
		</div>
	</div>

	<!-- Sessions Table -->
	<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
		<table class="w-full text-left text-xs">
			<thead class="border-b border-slate-200 bg-slate-50 font-medium text-slate-500 dark:border-slate-800 dark:bg-slate-800/60 dark:text-slate-400">
				<tr>
					<th class="px-4 py-3">System ID</th>
					<th class="px-4 py-3">Remote IP</th>
					<th class="px-4 py-3">Bind Type</th>
					<th class="px-4 py-3">Status</th>
					<th class="px-4 py-3">In-Flight</th>
					<th class="px-4 py-3">Customer Org ID</th>
					<th class="px-4 py-3">Bound At</th>
					<th class="px-4 py-3 text-right">Actions</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
				{#if loading && sessions.length === 0}
					<tr>
						<td colspan="8" class="px-4 py-8 text-center text-slate-400">Loading customer sessions...</td>
					</tr>
				{:else if sessions.length === 0}
					<tr>
						<td colspan="8" class="px-4 py-8 text-center text-slate-400">No active customer SMPP sessions connected.</td>
					</tr>
				{:else}
					{#each sessions as s (s.id)}
						<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/50">
							<td class="px-4 py-3 font-mono font-bold text-slate-900 dark:text-white">{s.system_id || 'UNKNOWN'}</td>
							<td class="px-4 py-3 font-mono text-slate-600 dark:text-slate-400">{s.remote_ip}</td>
							<td class="px-4 py-3 font-mono uppercase text-slate-700 dark:text-slate-300">{s.bind_type || 'unbound'}</td>
							<td class="px-4 py-3">
								<span
									class="inline-flex rounded-full px-2 py-0.5 text-[10px] font-semibold {s.bound
										? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
										: 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300'}"
								>
									{s.bound ? 'BOUND' : 'CONNECTING'}
								</span>
							</td>
							<td class="px-4 py-3 font-mono font-semibold text-slate-800 dark:text-slate-200">{s.in_flight}</td>
							<td class="px-4 py-3 font-mono text-[10px] text-slate-400">{s.customer_org_id}</td>
							<td class="px-4 py-3 text-slate-500">{new Date(s.bound_at).toLocaleTimeString()}</td>
							<td class="px-4 py-3 text-right">
								<button
									type="button"
									disabled={disconnectingID === s.id}
									onclick={() => disconnectSession(s.id)}
									class="rounded bg-rose-50 px-2 py-1 text-xs font-semibold text-rose-600 hover:bg-rose-100 dark:bg-rose-950/40 dark:text-rose-400"
								>
									{disconnectingID === s.id ? 'Disconnecting...' : 'Force Disconnect'}
								</button>
							</td>
						</tr>
					{/each}
				{/if}
			</tbody>
		</table>
	</div>
</div>
