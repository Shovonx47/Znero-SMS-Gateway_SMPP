<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { i18n } from '$lib/i18n.svelte';
	import type { CustomerDashboard } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let data = $state<CustomerDashboard | null>(null);
	let range = $state('7d');

	async function loadCustomer() {
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

			const res = await apiClient.getCustomerDashboard({
				start_time: start.toISOString(),
				end_time: now.toISOString()
			});
			data = res.data;
		} catch (err: any) {
			error = err.message || 'Failed to load customer analytics';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		loadCustomer();
	});
</script>

<svelte:head>
	<title>{i18n.t('nav_customer')} | {i18n.t('app_title')}</title>
</svelte:head>

<div class="space-y-6 p-6">
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
				{i18n.t('nav_customer')}
			</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Your message delivery success rates, total spend, and delivery error diagnostics.
			</p>
		</div>

		<div class="flex items-center gap-2">
			<select
				bind:value={range}
				onchange={loadCustomer}
				class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-sm dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
			>
				<option value="today">{i18n.t('filter_today')}</option>
				<option value="7d">{i18n.t('filter_7d')}</option>
				<option value="30d">{i18n.t('filter_30d')}</option>
			</select>

			<button
				type="button"
				onclick={loadCustomer}
				class="inline-flex items-center gap-1.5 rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500"
			>
				<span>{i18n.t('filter_apply')}</span>
			</button>
		</div>
	</div>

	{#if loading}
		<Loading message="Loading customer metrics..." />
	{:else if error}
		<ErrorState title="Dashboard Error" message={error} retry={loadCustomer} />
	{:else if data}
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
				<div class="text-xs font-medium text-slate-500">{i18n.t('metric_spend')}</div>
				<div class="mt-2 space-y-1">
					{#each Object.entries(data.spend_by_cur) as [cur, val]}
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

		<!-- Failure Diagnostics Breakdown -->
		<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-base font-semibold text-slate-900 dark:text-white">Delivery Failure Diagnostics</h2>
			<p class="text-xs text-slate-500">Reasons for failed or undelivered SMS traffic</p>

			<div class="mt-4 overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 text-slate-500 dark:border-slate-800">
						<tr>
							<th class="pb-2 font-semibold">Error Reason / DLR Code</th>
							<th class="pb-2 font-semibold text-right">Occurrence Count</th>
							<th class="pb-2 font-semibold text-right">Percentage of Failures</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
						{#each data.error_breakdown as err}
							<tr>
								<td class="py-2.5 font-medium text-slate-900 dark:text-white font-mono">
									{err.error_code || 'UNSPECIFIED'}
								</td>
								<td class="py-2.5 text-right font-mono text-slate-600 dark:text-slate-300">
									{err.count.toLocaleString()}
								</td>
								<td class="py-2.5 text-right font-mono font-semibold text-rose-600 dark:text-rose-400">
									{(err.percentage * 100).toFixed(1)}%
								</td>
							</tr>
						{:else}
							<tr>
								<td colspan="3" class="py-4 text-center text-slate-400">
									No delivery errors encountered in this time window! All traffic succeeded.
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>
	{/if}
</div>
