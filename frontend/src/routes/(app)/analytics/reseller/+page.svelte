<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { i18n } from '$lib/i18n.svelte';
	import type { ResellerDashboard } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let data = $state<ResellerDashboard | null>(null);
	let range = $state('7d');

	async function loadReseller() {
		try {
			loading = true;
			error = null;
			const now = new Date();
			let start = new Date();
			if (range === 'today') {
				start.setHours(0, 0, 0, 0);
			} else if (range === '7d') {
				start.setDate(now.getDate() - 7);
			} else if (range === '30d') {
				start.setDate(now.getDate() - 30);
			}

			const res = await apiClient.getResellerDashboard({
				start_time: start.toISOString(),
				end_time: now.toISOString()
			});
			data = res.data;
		} catch (err: any) {
			error = err.message || 'Failed to load reseller analytics';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		loadReseller();
	});
</script>

<svelte:head>
	<title>{i18n.t('nav_reseller')} | {i18n.t('app_title')}</title>
</svelte:head>

<div class="space-y-6 p-6">
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
				{i18n.t('nav_reseller')}
			</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Child customer volume, aggregated revenue, and client delivery statistics.
			</p>
		</div>

		<div class="flex items-center gap-2">
			<select
				bind:value={range}
				onchange={loadReseller}
				class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-sm dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
			>
				<option value="today">{i18n.t('filter_today')}</option>
				<option value="7d">{i18n.t('filter_7d')}</option>
				<option value="30d">{i18n.t('filter_30d')}</option>
			</select>

			<button
				type="button"
				onclick={loadReseller}
				class="inline-flex items-center gap-1.5 rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500"
			>
				<span>{i18n.t('filter_apply')}</span>
			</button>
		</div>
	</div>

	{#if loading}
		<Loading message="Loading reseller metrics..." />
	{:else if error}
		<ErrorState title="Reseller Dashboard Error" message={error} retry={loadReseller} />
	{:else if data}
		<!-- KPI Grid -->
		<div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="text-xs font-medium text-slate-500">{i18n.t('metric_submitted')}</div>
				<div class="mt-2 text-3xl font-bold tracking-tight text-slate-900 dark:text-white">
					{data.total_submitted.toLocaleString()}
				</div>
				<div class="mt-1 text-xs text-slate-500">
					Delivered: <strong class="text-emerald-600">{data.total_delivered.toLocaleString()}</strong>
				</div>
			</div>

			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="text-xs font-medium text-slate-500">{i18n.t('metric_rate')}</div>
				<div class="mt-2 text-3xl font-bold tracking-tight text-emerald-600 dark:text-emerald-400">
					{(data.delivery_rate * 100).toFixed(2)}%
				</div>
				<div class="mt-1 text-xs text-slate-500">
					Failed: <strong class="text-rose-600">{data.total_failed.toLocaleString()}</strong>
				</div>
			</div>

			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="text-xs font-medium text-slate-500">{i18n.t('metric_revenue')}</div>
				<div class="mt-2 space-y-1">
					{#each Object.entries(data.revenue_by_cur) as [cur, val]}
						<div class="flex items-baseline justify-between">
							<span class="text-xs font-semibold text-slate-500">{cur}</span>
							<span class="text-xl font-bold text-slate-900 dark:text-white">{parseFloat(val).toFixed(2)}</span>
						</div>
					{:else}
						<div class="text-sm font-medium text-slate-400">0.00</div>
					{/each}
				</div>
			</div>
		</div>

		<!-- Top Client Accounts -->
		<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-base font-semibold text-slate-900 dark:text-white">Client Accounts Breakdown</h2>
			<div class="mt-4 overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 text-slate-500 dark:border-slate-800">
						<tr>
							<th class="pb-2 font-semibold">Client Name</th>
							<th class="pb-2 font-semibold text-right">Volume</th>
							<th class="pb-2 font-semibold text-right">Delivered</th>
							<th class="pb-2 font-semibold text-right">Revenue</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
						{#each data.top_customers as cust}
							<tr>
								<td class="py-2.5 font-medium text-slate-900 dark:text-white truncate max-w-[160px]">
									{cust.customer_name}
								</td>
								<td class="py-2.5 text-right font-mono text-slate-600 dark:text-slate-300">
									{cust.submitted_count.toLocaleString()}
								</td>
								<td class="py-2.5 text-right font-mono text-emerald-600 dark:text-emerald-400">
									{cust.delivered_count.toLocaleString()}
								</td>
								<td class="py-2.5 text-right font-mono font-semibold text-slate-900 dark:text-white">
									{cust.currency} {parseFloat(cust.revenue).toFixed(2)}
								</td>
							</tr>
						{:else}
							<tr>
								<td colspan="4" class="py-4 text-center text-slate-400">No child customer activity in period</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>
	{/if}
</div>
