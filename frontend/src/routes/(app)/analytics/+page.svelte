<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import { i18n } from '$lib/i18n.svelte';
	import type { ExecutiveDashboard } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let data = $state<ExecutiveDashboard | null>(null);
	let range = $state('7d');

	async function loadDashboard() {
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

			const res = await apiClient.getExecutiveDashboard({
				start_time: start.toISOString(),
				end_time: now.toISOString(),
				granularity: range === 'today' ? '15m' : '1h'
			});
			data = res.data;
		} catch (err: any) {
			error = err.message || 'Failed to load executive dashboard';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		loadDashboard();
	});
</script>

<svelte:head>
	<title>{i18n.t('nav_executive')} | {i18n.t('app_title')}</title>
</svelte:head>

<div class="space-y-6 p-6">
	<!-- Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
				{i18n.t('nav_executive')}
			</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				High-level business intelligence, message volumes, multi-currency revenue, and delivery performance.
			</p>
		</div>

		<!-- Date range filter -->
		<div class="flex items-center gap-2">
			<select
				bind:value={range}
				onchange={loadDashboard}
				class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-sm transition hover:border-slate-400 focus:outline-none focus:ring-2 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
			>
				<option value="today">{i18n.t('filter_today')}</option>
				<option value="7d">{i18n.t('filter_7d')}</option>
				<option value="30d">{i18n.t('filter_30d')}</option>
			</select>

			<button
				type="button"
				onclick={loadDashboard}
				class="inline-flex items-center gap-1.5 rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-semibold text-white shadow-sm transition hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-500"
			>
				<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
				</svg>
				<span>{i18n.t('filter_apply')}</span>
			</button>
		</div>
	</div>

	{#if loading}
		<Loading message="Loading analytics overview..." />
	{:else if error}
		<ErrorState title="Dashboard Error" message={error} retry={loadDashboard} />
	{:else if data}
		<!-- Primary KPI Cards -->
		<div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
			<!-- Total Submitted -->
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="flex items-center justify-between text-xs font-medium text-slate-500 dark:text-slate-400">
					<span>{i18n.t('metric_submitted')}</span>
					<span class="rounded bg-indigo-50 px-1.5 py-0.5 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-400">SMS</span>
				</div>
				<div class="mt-2 text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
					{data.total_submitted.toLocaleString()}
				</div>
				<div class="mt-1 text-xs text-slate-500">
					Delivered: <strong class="text-emerald-600 dark:text-emerald-400">{data.total_delivered.toLocaleString()}</strong>
				</div>
			</div>

			<!-- Delivery Rate -->
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="flex items-center justify-between text-xs font-medium text-slate-500 dark:text-slate-400">
					<span>{i18n.t('metric_rate')}</span>
					<span class="rounded bg-emerald-50 px-1.5 py-0.5 text-emerald-700 dark:bg-emerald-950/50 dark:text-emerald-400">DLR</span>
				</div>
				<div class="mt-2 text-2xl font-bold tracking-tight text-emerald-600 dark:text-emerald-400">
					{(data.delivery_rate * 100).toFixed(2)}%
				</div>
				<div class="mt-1 text-xs text-slate-500">
					Failed: <strong class="text-rose-600 dark:text-rose-400">{data.total_failed.toLocaleString()}</strong>
				</div>
			</div>

			<!-- Multi-currency Gross Revenue -->
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="flex items-center justify-between text-xs font-medium text-slate-500 dark:text-slate-400">
					<span>{i18n.t('metric_revenue')}</span>
					<span class="rounded bg-amber-50 px-1.5 py-0.5 text-amber-700 dark:bg-amber-950/50 dark:text-amber-400">REV</span>
				</div>
				<div class="mt-2 space-y-1">
					{#each Object.entries(data.revenue_by_cur) as [cur, val]}
						<div class="flex items-baseline justify-between">
							<span class="text-xs font-semibold text-slate-500">{cur}</span>
							<span class="text-lg font-bold text-slate-900 dark:text-white">{parseFloat(val).toFixed(2)}</span>
						</div>
					{:else}
						<div class="text-sm font-medium text-slate-400">No revenue recorded</div>
					{/each}
				</div>
			</div>

			<!-- Multi-currency Net Margin (Platform Only) or Total Spend -->
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				{#if auth.isPlatformAdmin}
					<div class="flex items-center justify-between text-xs font-medium text-slate-500 dark:text-slate-400">
						<span>{i18n.t('metric_margin')}</span>
						<span class="rounded bg-purple-50 px-1.5 py-0.5 text-purple-700 dark:bg-purple-950/50 dark:text-purple-400">PLATFORM</span>
					</div>
					<div class="mt-2 space-y-1">
						{#each Object.entries(data.margin_by_cur) as [cur, val]}
							<div class="flex items-baseline justify-between">
								<span class="text-xs font-semibold text-slate-500">{cur}</span>
								<span class="text-lg font-bold text-purple-600 dark:text-purple-400">{parseFloat(val).toFixed(2)}</span>
							</div>
						{:else}
							<div class="text-sm font-medium text-slate-400">No margin recorded</div>
						{/each}
					</div>
				{:else}
					<div class="flex items-center justify-between text-xs font-medium text-slate-500 dark:text-slate-400">
						<span>{i18n.t('metric_spend')}</span>
						<span class="rounded bg-blue-50 px-1.5 py-0.5 text-blue-700 dark:bg-blue-950/50 dark:text-blue-400">CUSTOMER</span>
					</div>
					<div class="mt-2 space-y-1">
						{#each Object.entries(data.revenue_by_cur) as [cur, val]}
							<div class="flex items-baseline justify-between">
								<span class="text-xs font-semibold text-slate-500">{cur}</span>
								<span class="text-lg font-bold text-slate-900 dark:text-white">{parseFloat(val).toFixed(2)}</span>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		</div>

		<!-- Time Series Visual Grid -->
		<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-base font-semibold text-slate-900 dark:text-white">
				Traffic Volume & Delivery Performance Trends
			</h2>
			<p class="text-xs text-slate-500">Hourly message intake vs. successful delivery</p>

			<div class="mt-6 flex h-48 items-end gap-2 overflow-x-auto pb-4">
				{#each data.time_series as pt}
					{@const maxSub = Math.max(...data.time_series.map((p) => p.submitted), 1)}
					{@const heightPercent = Math.min(Math.round((pt.submitted / maxSub) * 100), 100)}
					{@const delPercent = Math.min(Math.round((pt.delivered / maxSub) * 100), 100)}
					<div class="flex min-w-[28px] flex-1 flex-col items-center gap-1 group relative">
						<!-- Tooltip -->
						<div class="pointer-events-none absolute bottom-full mb-2 hidden rounded-md bg-slate-900 px-2 py-1 text-[10px] text-white shadow-lg group-hover:block z-10 whitespace-nowrap">
							<div>{new Date(pt.bucket).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</div>
							<div>Sub: {pt.submitted}</div>
							<div>Del: {pt.delivered} ({((pt.delivery_rate || 0) * 100).toFixed(1)}%)</div>
						</div>

						<div class="flex h-36 w-full items-end gap-0.5 rounded bg-slate-100 p-0.5 dark:bg-slate-800">
							<div class="w-1/2 rounded-t bg-indigo-500 transition-all" style="height: {heightPercent}%"></div>
							<div class="w-1/2 rounded-t bg-emerald-500 transition-all" style="height: {delPercent}%"></div>
						</div>
						<span class="text-[9px] text-slate-400">
							{new Date(pt.bucket).getHours()}:00
						</span>
					</div>
				{:else}
					<div class="flex h-full w-full items-center justify-center text-xs text-slate-400">
						No traffic points recorded for this interval.
					</div>
				{/each}
			</div>
		</div>

		<!-- Tables Section -->
		<div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
			<!-- Top Customers -->
			<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<h2 class="text-base font-semibold text-slate-900 dark:text-white">Top Active Accounts</h2>
				<p class="text-xs text-slate-500">Highest volume customer accounts</p>

				<div class="mt-4 overflow-x-auto">
					<table class="w-full text-left text-xs">
						<thead class="border-b border-slate-200 text-slate-500 dark:border-slate-800">
							<tr>
								<th class="pb-2 font-semibold">Customer</th>
								<th class="pb-2 font-semibold text-right">Volume</th>
								<th class="pb-2 font-semibold text-right">Delivered</th>
								<th class="pb-2 font-semibold text-right">Revenue</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
							{#each data.top_customers || [] as c}
								<tr>
									<td class="py-2.5 font-medium text-slate-900 dark:text-white truncate max-w-[140px]">
										{c.customer_name}
									</td>
									<td class="py-2.5 text-right font-mono text-slate-600 dark:text-slate-300">
										{c.submitted_count.toLocaleString()}
									</td>
									<td class="py-2.5 text-right font-mono text-emerald-600 dark:text-emerald-400">
										{c.delivered_count.toLocaleString()}
									</td>
									<td class="py-2.5 text-right font-mono font-semibold text-slate-900 dark:text-white">
										{c.currency} {parseFloat(c.revenue).toFixed(2)}
									</td>
								</tr>
							{:else}
								<tr>
									<td colspan="4" class="py-4 text-center text-slate-400">No customer usage in period</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			</div>

			<!-- Provider Route Performance (Platform Only) -->
			{#if auth.isPlatformAdmin}
				<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
					<div class="flex items-center justify-between">
						<div>
							<h2 class="text-base font-semibold text-slate-900 dark:text-white">Upstream Carrier Performance</h2>
							<p class="text-xs text-slate-500">Internal supplier metrics & cost</p>
						</div>
						<span class="rounded bg-purple-100 px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider text-purple-800 dark:bg-purple-900/50 dark:text-purple-300">
							Platform Only
						</span>
					</div>

					<div class="mt-4 overflow-x-auto">
						<table class="w-full text-left text-xs">
							<thead class="border-b border-slate-200 text-slate-500 dark:border-slate-800">
								<tr>
									<th class="pb-2 font-semibold">Carrier</th>
									<th class="pb-2 font-semibold text-right">DLR %</th>
									<th class="pb-2 font-semibold text-right">Latency</th>
									<th class="pb-2 font-semibold text-right">Buy Cost</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
								{#each data.provider_breakdown || [] as p}
									<tr>
										<td class="py-2.5 font-medium text-slate-900 dark:text-white truncate max-w-[140px]">
											{p.provider_name}
										</td>
										<td class="py-2.5 text-right font-mono font-semibold {p.delivery_rate >= 0.85 ? 'text-emerald-600' : 'text-amber-600'}">
											{(p.delivery_rate * 100).toFixed(1)}%
										</td>
										<td class="py-2.5 text-right font-mono text-slate-600 dark:text-slate-300">
											{p.avg_latency_ms.toFixed(0)} ms
										</td>
										<td class="py-2.5 text-right font-mono text-slate-900 dark:text-white">
											{p.currency} {parseFloat(p.cost).toFixed(2)}
										</td>
									</tr>
								{:else}
									<tr>
										<td colspan="4" class="py-4 text-center text-slate-400">No provider metrics in period</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				</div>
			{/if}
		</div>
	{/if}
</div>
