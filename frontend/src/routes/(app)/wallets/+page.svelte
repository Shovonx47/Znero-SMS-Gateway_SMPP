<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import { formatMoney, isNegativeAmount } from '$lib/formatMoney';
	import type { Wallet } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let wallets = $state<Wallet[]>([]);
	let total = $state(0);
	let page = $state(1);
	let pageSize = $state(20);
	let search = $state('');
	let statusFilter = $state('');
	let loading = $state(true);
	let errorMessage = $state('');

	const isCustomer = $derived(auth.tenant?.actor_org_type === 'customer');
	const isPlatform = $derived(auth.tenant?.actor_org_type === 'platform');

	async function loadWallets() {
		loading = true;
		errorMessage = '';
		try {
			if (isCustomer) {
				const res = await apiClient.getMyWallet();
				wallets = [res.data];
				total = 1;
			} else {
				const res = await apiClient.listWallets({
					search: search.trim() || undefined,
					status: statusFilter || undefined,
					page,
					page_size: pageSize
				});
				wallets = res.data.items || [];
				total = res.data.total;
			}
		} catch (err: any) {
			errorMessage = err.message || 'Failed to load wallets';
		} finally {
			loading = false;
		}
	}

	function handleSearch() {
		page = 1;
		loadWallets();
	}

	function getStatusBadge(status: string) {
		switch (status) {
			case 'active':
				return 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300';
			case 'frozen':
				return 'bg-amber-100 text-amber-800 dark:bg-amber-950/60 dark:text-amber-300';
			case 'closed':
				return 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-300';
			default:
				return 'bg-slate-100 text-slate-800 dark:bg-slate-800 dark:text-slate-300';
		}
	}

	onMount(() => {
		loadWallets();
	});
</script>

<div class="space-y-6">
	<!-- Page Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
				{isCustomer ? 'My Organization Wallet' : 'Wallets & Balances'}
			</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				{isCustomer
					? 'Current ledger balance, active holds, and available credit for messaging.'
					: 'Prepaid and postpaid customer wallets, credit enforcement, and live balance ledger.'}
			</p>
		</div>
	</div>

	<!-- Filter & Search Controls (staff/reseller view) -->
	{#if !isCustomer}
		<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="flex flex-wrap items-center gap-3">
				<div class="relative min-w-[240px] flex-1">
					<input aria-label="Select search"
						type="text"
						bind:value={search}
						onkeydown={(e) => e.key === 'Enter' && handleSearch()}
						placeholder="Search organization name..."
						class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 pl-9 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
					<svg class="absolute left-3 top-2.5 h-4 w-4 text-slate-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
					</svg>
				</div>

				<div>
					<select aria-label="Filter by status"
						bind:value={statusFilter}
						onchange={() => { page = 1; loadWallets(); }}
						class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						<option value="">All Statuses</option>
						<option value="active">Active</option>
						<option value="frozen">Frozen</option>
						<option value="closed">Closed</option>
					</select>
				</div>

				<button
					onclick={handleSearch}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-700"
				>
					Search
				</button>
			</div>
		</div>
	{/if}

	<!-- Wallet Cards / Table -->
	{#if loading}
		<Loading message="Loading wallets..." />
	{:else if errorMessage}
		<ErrorState message={errorMessage} retry={loadWallets} />
	{:else if wallets.length === 0}
		<Empty description="No wallets found matching the current criteria." />
	{:else}
		<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50 font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-400">
						<tr>
							<th class="px-4 py-3">Organization</th>
							<th class="px-4 py-3">Type</th>
							<th class="px-4 py-3">Currency</th>
							<th class="px-4 py-3 text-right">Ledger Balance</th>
							<th class="px-4 py-3 text-right">Active Holds</th>
							<th class="px-4 py-3 text-right">Available Funds</th>
							<th class="px-4 py-3 text-center">Status</th>
							<th class="px-4 py-3 text-right">Action</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
						{#each wallets as w (w.id)}
							<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40">
								<td class="px-4 py-3">
									<div class="font-medium text-slate-900 dark:text-white">
										{w.organization_name || 'Organization ' + w.organization_id.slice(0, 8)}
									</div>
									<div class="text-[10px] font-mono text-slate-400">
										ID: {w.id.slice(0, 8)}...
									</div>
								</td>
								<td class="px-4 py-3 capitalize text-slate-600 dark:text-slate-300">
									<span class="inline-flex items-center rounded px-2 py-0.5 text-[10px] font-medium {w.account_type === 'prepaid' ? 'bg-blue-50 text-blue-700 dark:bg-blue-950 dark:text-blue-300' : 'bg-purple-50 text-purple-700 dark:bg-purple-950 dark:text-purple-300'}">
										{w.account_type}
									</span>
								</td>
								<td class="px-4 py-3 font-semibold text-slate-700 dark:text-slate-300">
									{w.currency}
								</td>
								<td class="px-4 py-3 text-right font-mono {isNegativeAmount(w.cached_balance) ? 'text-rose-600 font-bold' : 'text-slate-900 dark:text-white'}">
									{formatMoney(w.cached_balance, w.currency)}
								</td>
								<td class="px-4 py-3 text-right font-mono text-amber-600 dark:text-amber-400">
									{formatMoney(w.active_holds, w.currency)}
								</td>
								<td class="px-4 py-3 text-right font-mono text-sm font-bold {isNegativeAmount(w.available_balance) ? 'text-rose-600 dark:text-rose-400' : 'text-emerald-600 dark:text-emerald-400'}">
									{formatMoney(w.available_balance, w.currency)}
								</td>
								<td class="px-4 py-3 text-center">
									<span class="inline-block rounded-full px-2.5 py-0.5 text-[10px] font-semibold uppercase tracking-wider {getStatusBadge(w.status)}">
										{w.status}
									</span>
								</td>
								<td class="px-4 py-3 text-right">
									<a
										href="/wallets/{w.id}"
										class="rounded-lg bg-slate-100 px-3 py-1.5 font-semibold text-indigo-600 hover:bg-slate-200 dark:bg-slate-800 dark:text-indigo-400 dark:hover:bg-slate-700"
									>
										View Details &rarr;
									</a>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>

			<!-- Pagination -->
			{#if total > pageSize && !isCustomer}
				<div class="flex items-center justify-between border-t border-slate-200 px-4 py-3 dark:border-slate-800">
					<div class="text-xs text-slate-500">
						Showing {(page - 1) * pageSize + 1} to {Math.min(page * pageSize, total)} of {total} wallets
					</div>
					<div class="flex gap-2">
						<button
							disabled={page === 1}
							onclick={() => { page--; loadWallets(); }}
							class="rounded border border-slate-300 px-3 py-1 text-xs text-slate-600 disabled:opacity-50 dark:border-slate-700 dark:text-slate-400"
						>
							Previous
						</button>
						<button
							disabled={page * pageSize >= total}
							onclick={() => { page++; loadWallets(); }}
							class="rounded border border-slate-300 px-3 py-1 text-xs text-slate-600 disabled:opacity-50 dark:border-slate-700 dark:text-slate-400"
						>
							Next
						</button>
					</div>
				</div>
			{/if}
		</div>
	{/if}
</div>
