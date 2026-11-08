<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import type { InboundMessage } from '$lib/types/api';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let messages = $state<InboundMessage[]>([]);
	let total = $state(0);
	let page = $state(1);

	async function loadMessages() {
		loading = true;
		error = null;
		try {
			const res = await apiClient.listInboundMessages({ page, per_page: 25 });
			messages = res.data.items || [];
			total = res.data.total || 0;
		} catch (err: any) {
			error = err.message || 'Failed to load inbound messages';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		loadMessages();
	});
</script>

<svelte:head>
	<title>Inbound MO Messages â€” Operations</title>
</svelte:head>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Inbound MO Messages</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Audit and track mobile-originated SMS messages received by the platform and routed to customer webhooks or SMPP sessions.
			</p>
		</div>
		<button
			type="button"
			onclick={() => loadMessages()}
			class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-medium text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
		>
			Refresh
		</button>
	</div>

	{#if error}
		<div class="rounded-lg bg-rose-50 p-4 text-sm text-rose-700 dark:bg-rose-950/40 dark:text-rose-300">{error}</div>
	{/if}

	<!-- Table -->
	<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
		<table class="w-full text-left text-xs">
			<thead class="border-b border-slate-200 bg-slate-50 font-medium text-slate-500 dark:border-slate-800 dark:bg-slate-800/60 dark:text-slate-400">
				<tr>
					<th class="px-4 py-3">Message ID</th>
					<th class="px-4 py-3">From (Subscriber)</th>
					<th class="px-4 py-3">To (Virtual Number)</th>
					<th class="px-4 py-3">Customer Org ID</th>
					<th class="px-4 py-3">Status</th>
					<th class="px-4 py-3">Encoding</th>
					<th class="px-4 py-3">Received At</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
				{#if loading && messages.length === 0}
					<tr>
						<td colspan="7" class="px-4 py-8 text-center text-slate-400">Loading inbound messages...</td>
					</tr>
				{:else if messages.length === 0}
					<tr>
						<td colspan="7" class="px-4 py-8 text-center text-slate-400">No inbound messages received yet.</td>
					</tr>
				{:else}
					{#each messages as m (m.id)}
						<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/50">
							<td class="px-4 py-3 font-mono text-[11px] text-slate-700 dark:text-slate-300">{m.id}</td>
							<td class="px-4 py-3 font-mono font-bold text-slate-900 dark:text-white">{m.source_e164}</td>
							<td class="px-4 py-3 font-mono text-indigo-600 dark:text-indigo-400">{m.destination_e164}</td>
							<td class="px-4 py-3 font-mono text-[10px] text-slate-500">{m.customer_org_id || 'â€”'}</td>
							<td class="px-4 py-3">
								<span
									class="inline-flex rounded-full px-2 py-0.5 text-[10px] font-semibold {m.status ===
									'routed'
										? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
										: m.status === 'unmatched'
											? 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-300'
											: 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300'}"
								>
									{m.status}
								</span>
							</td>
							<td class="px-4 py-3 font-mono text-[10px] text-slate-500">{m.encoding}</td>
							<td class="px-4 py-3 text-slate-500">{new Date(m.created_at).toLocaleString()}</td>
						</tr>
					{/each}
				{/if}
			</tbody>
		</table>

		<!-- Pagination Footer -->
		{#if total > 25}
			<div class="flex items-center justify-between border-t border-slate-200 px-4 py-3 dark:border-slate-800">
				<div class="text-xs text-slate-500">Total {total} inbound messages</div>
				<div class="flex gap-2">
					<button
						type="button"
						disabled={page <= 1}
						onclick={() => {
							page--;
							loadMessages();
						}}
						class="rounded border border-slate-300 px-2.5 py-1 text-xs disabled:opacity-50 dark:border-slate-700"
					>
						Previous
					</button>
					<button
						type="button"
						disabled={page * 25 >= total}
						onclick={() => {
							page++;
							loadMessages();
						}}
						class="rounded border border-slate-300 px-2.5 py-1 text-xs disabled:opacity-50 dark:border-slate-700"
					>
						Next
					</button>
				</div>
			</div>
		{/if}
	</div>
</div>
