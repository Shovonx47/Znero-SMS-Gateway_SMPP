<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import { formatMoney, isNegativeAmount } from '$lib/formatMoney';
	import type { ReconciliationReport } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let reports = $state<ReconciliationReport[]>([]);
	let loading = $state(true);
	let repairing = $state(false);
	let errorMessage = $state('');

	const canReconcile = $derived(auth.hasPermission('ledger.reconcile'));

	const totalWallets = $derived(reports.length);
	const mismatches = $derived(reports.filter((r) => r.status === 'mismatch').length);
	const matched = $derived(reports.filter((r) => r.status === 'matched').length);
	const repaired = $derived(reports.filter((r) => r.status === 'repaired').length);

	async function loadReport() {
		loading = true;
		errorMessage = '';
		try {
			const res = await apiClient.getReconciliationReport();
			reports = res.data || [];
		} catch (err: any) {
			errorMessage = err.message || 'Failed to run reconciliation check';
		} finally {
			loading = false;
		}
	}

	async function handleRepair() {
		if (!confirm('Are you sure you want to repair all mismatched balances? This will update cached wallet balance rows to match the mathematical sum of all double-entry ledger entries.')) {
			return;
		}
		repairing = true;
		try {
			const res = await apiClient.runReconciliationRepair();
			reports = res.data || [];
		} catch (err: any) {
			alert('Repair failed: ' + (err.message || 'Unknown error'));
		} finally {
			repairing = false;
		}
	}

	onMount(() => {
		if (canReconcile) {
			loadReport();
		} else {
			loading = false;
			errorMessage = 'Access denied: You do not have permission to view ledger reconciliation.';
		}
	});
</script>

<div class="space-y-6">
	<!-- Page Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
				Ledger Reconciliation & Integrity
			</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Mathematical verification of cached wallet balances against double-entry ledger journal entries.
			</p>
		</div>

		{#if canReconcile}
			<div class="flex items-center gap-2">
				<button
					onclick={loadReport}
					disabled={loading || repairing}
					class="rounded-lg border border-slate-300 bg-white px-4 py-2 text-xs font-semibold text-slate-700 shadow-sm hover:bg-slate-50 disabled:opacity-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
				>
					Re-Run Integrity Check
				</button>
				{#if mismatches > 0}
					<button
						onclick={handleRepair}
						disabled={loading || repairing}
						class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-700 disabled:opacity-50"
					>
						{repairing ? 'Repairing...' : 'Repair Mismatches'}
					</button>
				{/if}
			</div>
		{/if}
	</div>

	<!-- Status Summary Metric Cards -->
	<div class="grid grid-cols-1 gap-4 sm:grid-cols-4">
		<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="text-[11px] font-semibold uppercase tracking-wider text-slate-500">Total Wallets Checked</div>
			<div class="mt-2 text-2xl font-bold font-mono text-slate-900 dark:text-white">{totalWallets}</div>
		</div>

		<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="text-[11px] font-semibold uppercase tracking-wider text-emerald-600">Matched (100% Balanced)</div>
			<div class="mt-2 text-2xl font-bold font-mono text-emerald-600">{matched}</div>
		</div>

		<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="text-[11px] font-semibold uppercase tracking-wider text-rose-600">Mismatches Detected</div>
			<div class="mt-2 text-2xl font-bold font-mono text-rose-600">{mismatches}</div>
		</div>

		<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="text-[11px] font-semibold uppercase tracking-wider text-indigo-600">Repaired in Session</div>
			<div class="mt-2 text-2xl font-bold font-mono text-indigo-600">{repaired}</div>
		</div>
	</div>

	{#if mismatches > 0}
		<div class="rounded-xl border border-rose-200 bg-rose-50 p-4 text-xs text-rose-800 dark:border-rose-900 dark:bg-rose-950/40 dark:text-rose-300">
			<div class="font-bold">Discrepancy Alert</div>
			<p class="mt-1">
				{mismatches} wallet(s) have cached balances that do not match the sum of their ledger entries.
				Click "Repair Mismatches" to synchronize cached balances with the immutable ledger entries.
			</p>
		</div>
	{/if}

	<!-- Reconciliation Table -->
	{#if loading}
		<Loading message="Computing balance reconciliation across all wallets..." />
	{:else if errorMessage}
		<ErrorState message={errorMessage} retry={loadReport} />
	{:else if reports.length === 0}
		<Empty description="No wallets in system to reconcile." />
	{:else}
		<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50 font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-400">
						<tr>
							<th class="px-4 py-3">Wallet ID</th>
							<th class="px-4 py-3">Organization</th>
							<th class="px-4 py-3">Currency</th>
							<th class="px-4 py-3 text-right">Cached Balance</th>
							<th class="px-4 py-3 text-right">Computed Balance</th>
							<th class="px-4 py-3 text-right">Difference</th>
							<th class="px-4 py-3 text-center">Journal Entries</th>
							<th class="px-4 py-3 text-center">Status</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
						{#each reports as r (r.wallet_id)}
							<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40">
								<td class="px-4 py-3 font-mono text-slate-600 dark:text-slate-400">
									<a href="/wallets/{r.wallet_id}" class="text-indigo-600 hover:underline dark:text-indigo-400">
										{r.wallet_id.slice(0, 8)}...
									</a>
								</td>
								<td class="px-4 py-3 font-medium text-slate-900 dark:text-white">
									{r.organization_name}
								</td>
								<td class="px-4 py-3 font-semibold text-slate-600 dark:text-slate-300">
									{r.currency}
								</td>
								<td class="px-4 py-3 text-right font-mono {isNegativeAmount(r.cached_balance) ? 'text-rose-600' : 'text-slate-900 dark:text-white'}">
									{formatMoney(r.cached_balance, r.currency)}
								</td>
								<td class="px-4 py-3 text-right font-mono {isNegativeAmount(r.computed_balance) ? 'text-rose-600' : 'text-slate-900 dark:text-white'}">
									{formatMoney(r.computed_balance, r.currency)}
								</td>
								<td class="px-4 py-3 text-right font-mono font-bold {r.status === 'mismatch' ? 'text-rose-600' : 'text-slate-400'}">
									{formatMoney(r.difference, r.currency)}
								</td>
								<td class="px-4 py-3 text-center font-mono text-slate-600 dark:text-slate-400">
									{r.entries_count}
								</td>
								<td class="px-4 py-3 text-center">
									{#if r.status === 'matched'}
										<span class="inline-block rounded-full bg-emerald-100 px-2.5 py-0.5 text-[10px] font-semibold uppercase text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300">
											Matched
										</span>
									{:else if r.status === 'repaired'}
										<span class="inline-block rounded-full bg-blue-100 px-2.5 py-0.5 text-[10px] font-semibold uppercase text-blue-800 dark:bg-blue-950 dark:text-blue-300">
											Repaired
										</span>
									{:else}
										<span class="inline-block rounded-full bg-rose-100 px-2.5 py-0.5 text-[10px] font-semibold uppercase text-rose-800 dark:bg-rose-950 dark:text-rose-300">
											Mismatch
										</span>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>
	{/if}
</div>
