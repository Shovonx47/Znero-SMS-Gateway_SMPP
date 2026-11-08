<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import type { UnmatchedDLR } from '$lib/types/api';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let items = $state<UnmatchedDLR[]>([]);
	let total = $state(0);
	let page = $state(1);
	let statusFilter = $state('unmatched');
	const perPage = 25;

	async function loadDLRs() {
		loading = true;
		error = null;
		try {
			const res = await apiClient.getUnmatchedDLRs({
				page,
				per_page: perPage,
				match_status: statusFilter || undefined
			});
			items = res.data.items || [];
			total = res.data.total || 0;
		} catch (err: any) {
			error = err.message || 'Failed to load unmatched DLRs';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		loadDLRs();
	});
</script>

<svelte:head>
	<title>Unmatched DLRs — Znero</title>
</svelte:head>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-xl font-bold text-slate-900 dark:text-white">Unmatched DLR Receipts Buffer</h1>
			<p class="text-xs text-slate-500 dark:text-slate-400">
				Delivery receipts buffered for asynchronous provider message ID reconciliation and format normalization.
			</p>
		</div>

		<button
			type="button"
			onclick={loadDLRs}
			class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
		>
			Refresh Buffer
		</button>
	</div>

	<!-- Status Tabs -->
	<div class="flex border-b border-slate-200 dark:border-slate-800 text-xs">
		<button
			type="button"
			onclick={() => { statusFilter = 'unmatched'; page = 1; loadDLRs(); }}
			class="px-4 py-2 font-semibold border-b-2 {statusFilter === 'unmatched' ? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400' : 'border-transparent text-slate-500 hover:text-slate-700'}"
		>
			Pending Retry Window
		</button>
		<button
			type="button"
			onclick={() => { statusFilter = 'flagged_unmatched'; page = 1; loadDLRs(); }}
			class="px-4 py-2 font-semibold border-b-2 {statusFilter === 'flagged_unmatched' ? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400' : 'border-transparent text-slate-500 hover:text-slate-700'}"
		>
			Flagged Unmatched (Expired Window)
		</button>
		<button
			type="button"
			onclick={() => { statusFilter = ''; page = 1; loadDLRs(); }}
			class="px-4 py-2 font-semibold border-b-2 {statusFilter === '' ? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400' : 'border-transparent text-slate-500 hover:text-slate-700'}"
		>
			All Receipts
		</button>
	</div>

	<!-- Table -->
	<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
		{#if loading}
			<div class="p-8 text-center text-xs text-slate-500">Scanning unmatched DLR buffer...</div>
		{:else if error}
			<div class="p-8 text-center text-xs text-rose-600">{error}</div>
		{:else if items.length === 0}
			<div class="p-12 text-center text-xs text-slate-500">
				No unmatched delivery receipts found matching this filter.
			</div>
		{:else}
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50 text-[11px] font-semibold uppercase text-slate-500 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-400">
						<tr>
							<th class="px-4 py-3">Received At</th>
							<th class="px-4 py-3">Connection</th>
							<th class="px-4 py-3">Provider Msg ID</th>
							<th class="px-4 py-3">Stat</th>
							<th class="px-4 py-3">Err</th>
							<th class="px-4 py-3">Match State</th>
							<th class="px-4 py-3">Retries</th>
							<th class="px-4 py-3">Raw Receipt</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
						{#each items as item}
							<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/50">
								<td class="px-4 py-3 whitespace-nowrap text-slate-600 dark:text-slate-400">
									{new Date(item.received_at).toLocaleTimeString()}
								</td>
								<td class="px-4 py-3 font-mono text-[11px] text-slate-500 truncate max-w-[150px]">
									{item.connection_id || 'Unknown'}
								</td>
								<td class="px-4 py-3 font-mono font-bold text-slate-900 dark:text-white">
									{item.provider_message_id}
								</td>
								<td class="px-4 py-3 font-mono font-semibold">
									{item.status}
								</td>
								<td class="px-4 py-3 font-mono text-slate-500">
									{item.error_code || 'None'}
								</td>
								<td class="px-4 py-3 whitespace-nowrap">
									{#if item.match_status === 'matched'}
										<span class="rounded bg-emerald-50 px-2 py-0.5 text-[10px] font-bold text-emerald-700 uppercase">
											Matched
										</span>
									{:else if item.match_status === 'flagged_unmatched'}
										<span class="rounded bg-rose-50 px-2 py-0.5 text-[10px] font-bold text-rose-700 uppercase">
											Flagged Unmatched
										</span>
									{:else}
										<span class="rounded bg-amber-50 px-2 py-0.5 text-[10px] font-bold text-amber-700 uppercase">
											Retrying
										</span>
									{/if}
								</td>
								<td class="px-4 py-3 font-mono text-slate-600 dark:text-slate-400">
									{item.retry_count}
								</td>
								<td class="px-4 py-3 font-mono text-[11px] text-slate-500 max-w-xs truncate">
									{item.raw_receipt}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>

			<!-- Pagination -->
			<div class="flex items-center justify-between border-t border-slate-200 px-4 py-3 dark:border-slate-800">
				<span class="text-xs text-slate-500">Showing {items.length} of {total}</span>
				<div class="flex gap-2">
					<button
						type="button"
						disabled={page <= 1}
						onclick={() => { page--; loadDLRs(); }}
						class="rounded border border-slate-300 px-2.5 py-1 text-xs font-semibold text-slate-700 disabled:opacity-50 dark:border-slate-700 dark:text-slate-300"
					>
						Previous
					</button>
					<button
						type="button"
						disabled={page * perPage >= total}
						onclick={() => { page++; loadDLRs(); }}
						class="rounded border border-slate-300 px-2.5 py-1 text-xs font-semibold text-slate-700 disabled:opacity-50 dark:border-slate-700 dark:text-slate-300"
					>
						Next
					</button>
				</div>
			</div>
		{/if}
	</div>
</div>
