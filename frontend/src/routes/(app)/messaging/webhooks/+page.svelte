<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import type { CustomerWebhookDelivery } from '$lib/types/api';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let deliveries = $state<CustomerWebhookDelivery[]>([]);
	let total = $state(0);
	let page = $state(1);
	let statusFilter = $state('');

	async function loadDeliveries() {
		loading = true;
		error = null;
		try {
			const res = await apiClient.listPlatformWebhookDeliveries({
				page,
				per_page: 25,
				status: statusFilter || undefined
			});
			deliveries = res.data.items || [];
			total = res.data.total || 0;
		} catch (err: any) {
			error = err.message || 'Failed to load platform webhook deliveries';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		loadDeliveries();
	});
</script>

<svelte:head>
	<title>Webhook Deliveries â€” Platform Operations</title>
</svelte:head>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Customer Webhook Deliveries</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Platform-wide operational audit log of customer webhook dispatch attempts, retries, and errors.
			</p>
		</div>
		<div class="flex items-center gap-3">
			<select aria-label="Filter by status"
				bind:value={statusFilter}
				onchange={() => {
					page = 1;
					loadDeliveries();
				}}
				class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-medium text-slate-700 shadow-sm dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
			>
				<option value="">All Statuses</option>
				<option value="pending">Pending</option>
				<option value="retry">Retrying</option>
				<option value="success">Success</option>
				<option value="failed">Failed</option>
			</select>
			<button
				type="button"
				onclick={() => loadDeliveries()}
				class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-medium text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
			>
				Refresh
			</button>
		</div>
	</div>

	{#if error}
		<div class="rounded-lg bg-rose-50 p-4 text-sm text-rose-700 dark:bg-rose-950/40 dark:text-rose-300">{error}</div>
	{/if}

	<!-- Table -->
	<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
		<table class="w-full text-left text-xs">
			<thead class="border-b border-slate-200 bg-slate-50 font-medium text-slate-500 dark:border-slate-800 dark:bg-slate-800/60 dark:text-slate-400">
				<tr>
					<th class="px-4 py-3">Delivery ID</th>
					<th class="px-4 py-3">Event Type</th>
					<th class="px-4 py-3">Status</th>
					<th class="px-4 py-3">Attempts</th>
					<th class="px-4 py-3">Customer Org ID</th>
					<th class="px-4 py-3">Next Attempt</th>
					<th class="px-4 py-3">Created At</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
				{#if loading && deliveries.length === 0}
					<tr>
						<td colspan="7" class="px-4 py-8 text-center text-slate-400">Loading webhook deliveries...</td>
					</tr>
				{:else if deliveries.length === 0}
					<tr>
						<td colspan="7" class="px-4 py-8 text-center text-slate-400">No webhook delivery logs match criteria.</td>
					</tr>
				{:else}
					{#each deliveries as d (d.id)}
						<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/50">
							<td class="px-4 py-3 font-mono text-[11px] text-slate-700 dark:text-slate-300">{d.id}</td>
							<td class="px-4 py-3 font-mono font-medium text-indigo-600 dark:text-indigo-400">{d.event_type}</td>
							<td class="px-4 py-3">
								<span
									class="inline-flex rounded-full px-2 py-0.5 text-[10px] font-semibold {d.status ===
									'success'
										? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
										: d.status === 'failed'
											? 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-300'
											: 'bg-amber-100 text-amber-800 dark:bg-amber-950/60 dark:text-amber-300'}"
								>
									{d.status}
								</span>
							</td>
							<td class="px-4 py-3 font-mono text-slate-700 dark:text-slate-300">{d.attempts} / {d.max_attempts}</td>
							<td class="px-4 py-3 font-mono text-[10px] text-slate-400">{d.customer_org_id}</td>
							<td class="px-4 py-3 text-slate-500">
								{d.next_attempt_at ? new Date(d.next_attempt_at).toLocaleTimeString() : 'â€”'}
							</td>
							<td class="px-4 py-3 text-slate-500">{new Date(d.created_at).toLocaleString()}</td>
						</tr>
					{/each}
				{/if}
			</tbody>
		</table>

		<!-- Pagination Footer -->
		{#if total > 25}
			<div class="flex items-center justify-between border-t border-slate-200 px-4 py-3 dark:border-slate-800">
				<div class="text-xs text-slate-500">Total {total} deliveries</div>
				<div class="flex gap-2">
					<button
						type="button"
						disabled={page <= 1}
						onclick={() => {
							page--;
							loadDeliveries();
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
							loadDeliveries();
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
