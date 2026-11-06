<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { MarginReportItem, RateCard } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let items = $state<MarginReportItem[]>([]);
	let sellCards = $state<RateCard[]>([]);
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	// Filters
	let selectedCardId = $state('');
	let selectedCurrency = $state('');
	let violationsOnly = $state(false);
	let search = $state('');

	const isPlatform = $derived(auth.tenant?.actor_org_type === 'platform');
	const canViewMargin = $derived(isPlatform && auth.hasPermission('rate.margin.view'));

	async function loadCards() {
		try {
			const res = await apiClient.listRateCards({ kind: 'customer_sell', status: 'active', pageSize: 100 });
			sellCards = res.data.items || [];
		} catch {
			// ignore
		}
	}

	async function loadReport() {
		if (!canViewMargin) {
			errorMessage = '403 Forbidden: Margin & Deficit report is accessible to platform operators only.';
			isLoading = false;
			return;
		}
		isLoading = true;
		errorMessage = null;
		try {
			const res = await apiClient.getMarginReport({
				card_id: selectedCardId || undefined,
				deficit: violationsOnly
			});
			items = res.data.items || [];
		} catch (err) {
			errorMessage = err instanceof ApiClientError ? err.message : 'Failed to load margin report.';
		} finally {
			isLoading = false;
		}
	}

	onMount(async () => {
		await loadCards();
		await loadReport();
	});

	const filteredItems = $derived(
		items.filter((item) => {
			if (!search.trim()) return true;
			const q = search.toLowerCase();
			return (
				item.country_name.toLowerCase().includes(q) ||
				item.country_iso2.toLowerCase().includes(q) ||
				(item.operator_name && item.operator_name.toLowerCase().includes(q)) ||
				(item.card_name || item.sell_card_name || '').toLowerCase().includes(q)
			);
		})
	);

	const stats = $derived.by(() => {
		const total = items.length;
		const deficits = items.filter((x) => x.violation || x.has_violation || (x.margin_amount && parseFloat(x.margin_amount) < 0)).length;
		const healthy = total - deficits;
		let sumPct = 0;
		let pctCount = 0;
		for (const x of items) {
			if (x.margin_percent) {
				const val = parseFloat(x.margin_percent);
				if (!isNaN(val)) {
					sumPct += val;
					pctCount++;
				}
			}
		}
		const avgPct = pctCount > 0 ? (sumPct / pctCount).toFixed(1) : '0.0';
		return { total, deficits, healthy, avgPct };
	});

	function exportReportCSV() {
		if (filteredItems.length === 0) return;
		const headers = [
			'Sell Card',
			'Country',
			'ISO2',
			'Operator',
			'Type',
			'Sell Price',
			'Currency',
			'Lowest Buy Cost',
			'Lowest Buy Provider',
			'Margin Amount',
			'Margin Percent',
			'Has Violation'
		];
		const rows = filteredItems.map((i) => [
			`"${i.card_name || i.sell_card_name || ''}"`,
			`"${i.country_name}"`,
			i.country_iso2,
			`"${i.operator_name || 'Country Default'}"`,
			i.message_type,
			i.sell_price,
			i.currency,
			i.lowest_buy_cost || '',
			`"${i.lowest_buy_provider || ''}"`,
			i.margin_amount || '',
			i.margin_percent ? `${i.margin_percent}%` : '',
			i.violation || i.has_violation ? 'YES' : 'NO'
		]);
		const csvContent = 'data:text/csv;charset=utf-8,' + [headers.join(','), ...rows.map((r) => r.join(','))].join('\n');
		const encodedUri = encodeURI(csvContent);
		const link = document.createElement('a');
		link.setAttribute('href', encodedUri);
		link.setAttribute('download', `margin-report-${new Date().toISOString().slice(0, 10)}.csv`);
		document.body.appendChild(link);
		link.click();
		document.body.removeChild(link);
		toast.success('Margin report exported to CSV');
	}
</script>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Margin & Deficit Report</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Audit profit margins across active customer sell rates against upstream wholesale supplier costs.
			</p>
		</div>

		<div class="flex items-center gap-2">
			<button
				type="button"
				onclick={exportReportCSV}
				disabled={filteredItems.length === 0}
				class="inline-flex items-center gap-2 rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs font-semibold text-slate-700 shadow-sm hover:bg-slate-50 disabled:opacity-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" />
				</svg>
				Export CSV
			</button>
		</div>
	</div>

	{#if errorMessage}
		<ErrorState message={errorMessage} retry={loadReport} />
	{:else}
		<!-- Stats Overview -->
		<div class="grid grid-cols-2 gap-4 sm:grid-cols-4">
			<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<span class="text-xs font-semibold text-slate-500">Total Analyzed</span>
				<div class="mt-1 font-mono text-2xl font-bold text-slate-900 dark:text-white">{stats.total}</div>
			</div>

			<div class="rounded-xl border border-rose-200 bg-rose-50/50 p-4 shadow-sm dark:border-rose-950 dark:bg-rose-950/20">
				<div class="flex items-center justify-between">
					<span class="text-xs font-semibold text-rose-600 dark:text-rose-400">Deficits (Violations)</span>
					<span class="rounded-full bg-rose-200 px-1.5 py-0.5 text-[10px] font-bold text-rose-800 dark:bg-rose-900 dark:text-rose-200">Alert</span>
				</div>
				<div class="mt-1 font-mono text-2xl font-bold text-rose-700 dark:text-rose-300">{stats.deficits}</div>
			</div>

			<div class="rounded-xl border border-emerald-200 bg-emerald-50/50 p-4 shadow-sm dark:border-emerald-950 dark:bg-emerald-950/20">
				<span class="text-xs font-semibold text-emerald-600 dark:text-emerald-400">Profitable Routes</span>
				<div class="mt-1 font-mono text-2xl font-bold text-emerald-700 dark:text-emerald-300">{stats.healthy}</div>
			</div>

			<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<span class="text-xs font-semibold text-slate-500">Average Margin</span>
				<div class="mt-1 font-mono text-2xl font-bold text-indigo-600 dark:text-indigo-400">{stats.avgPct}%</div>
			</div>
		</div>

		<!-- Filter Bar -->
		<div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900 sm:flex-row sm:items-center">
			<div class="relative flex-1">
				<input
					type="search"
					bind:value={search}
					placeholder="Search destination country, operator or rate card..."
					class="w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
				/>
			</div>

			<select
				bind:value={selectedCardId}
				onchange={loadReport}
				class="rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
			>
				<option value="">All Sell Cards</option>
				{#each sellCards as card}
					<option value={card.id}>{card.name} ({card.currency_code})</option>
				{/each}
			</select>

			<select
				bind:value={selectedCurrency}
				onchange={loadReport}
				class="rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
			>
				<option value="">All Currencies</option>
				<option value="USD">USD</option>
				<option value="EUR">EUR</option>
				<option value="GBP">GBP</option>
				<option value="BDT">BDT</option>
				<option value="INR">INR</option>
			</select>

			<label class="flex items-center gap-2 text-xs font-semibold text-rose-600 dark:text-rose-400 cursor-pointer select-none">
				<input
					type="checkbox"
					bind:checked={violationsOnly}
					onchange={loadReport}
					class="rounded border-slate-300 text-rose-600 focus:ring-rose-500 dark:border-slate-700"
				/>
				<span>Deficits Only</span>
			</label>
		</div>

		{#if isLoading}
			<Loading message="Computing margin analytics..." />
		{:else if filteredItems.length === 0}
			<Empty
				title="No margin data found"
				description={violationsOnly ? 'Great news! No margin deficits or violations detected.' : 'No rates found matching criteria.'}
			/>
		{:else}
			<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="overflow-x-auto">
					<table class="w-full text-left text-xs">
						<thead class="border-b border-slate-200 bg-slate-50 font-semibold text-slate-700 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-300">
							<tr>
								<th class="px-4 py-3">Destination</th>
								<th class="px-4 py-3">Operator</th>
								<th class="px-4 py-3">Sell Card</th>
								<th class="px-4 py-3">Type</th>
								<th class="px-4 py-3">Supplier Cost</th>
								<th class="px-4 py-3">Sell Price</th>
								<th class="px-4 py-3">Margin ($)</th>
								<th class="px-4 py-3">Margin (%)</th>
								<th class="px-4 py-3 text-right">Status</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
							{#each filteredItems as item}
								{@const isDeficit = item.violation || item.has_violation || (item.margin_amount && parseFloat(item.margin_amount) < 0)}
								<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/30 {isDeficit ? 'bg-rose-50/40 dark:bg-rose-950/20' : ''}">
									<td class="px-4 py-3">
										<div class="font-semibold text-slate-900 dark:text-white">
											{item.country_name}
										</div>
										<span class="font-mono text-[11px] text-slate-400">
											{item.country_iso2}
										</span>
									</td>
									<td class="px-4 py-3">
										{#if item.operator_name}
											<span class="font-medium text-slate-800 dark:text-slate-200">{item.operator_name}</span>
										{:else}
											<span class="inline-flex items-center rounded bg-slate-100 px-1.5 py-0.5 text-[10px] font-medium text-slate-600 dark:bg-slate-800 dark:text-slate-400">
												Country Default
											</span>
										{/if}
									</td>
									<td class="px-4 py-3">
										<a
											href="/rates/sell-cards/{item.card_id || item.sell_card_id}"
											class="font-medium text-indigo-600 hover:text-indigo-800 dark:text-indigo-400"
										>
											{item.card_name || item.sell_card_name}
										</a>
									</td>
									<td class="px-4 py-3 font-mono text-slate-500">
										{item.message_type}
									</td>
									<td class="px-4 py-3 font-mono">
										{#if item.lowest_buy_cost}
											<span class="font-semibold text-slate-700 dark:text-slate-300">
												{item.lowest_buy_cost} {item.currency}
											</span>
											{#if item.lowest_buy_provider}
												<div class="text-[10px] text-slate-400">via {item.lowest_buy_provider}</div>
											{/if}
										{:else}
											<span class="text-slate-400">—</span>
										{/if}
									</td>
									<td class="px-4 py-3 font-mono font-bold text-slate-900 dark:text-white">
										{item.sell_price} <span class="text-[10px] font-normal text-slate-400">{item.currency}</span>
									</td>
									<td class="px-4 py-3 font-mono font-semibold">
										{#if item.margin_amount}
											{#if isDeficit}
												<span class="text-rose-600 dark:text-rose-400">{item.margin_amount}</span>
											{:else}
												<span class="text-emerald-600 dark:text-emerald-400">+{item.margin_amount}</span>
											{/if}
										{:else}
											<span class="text-slate-400">—</span>
										{/if}
									</td>
									<td class="px-4 py-3 font-mono font-semibold">
										{#if item.margin_percent}
											{#if isDeficit}
												<span class="text-rose-600 dark:text-rose-400">{item.margin_percent}%</span>
											{:else}
												<span class="text-emerald-600 dark:text-emerald-400">+{item.margin_percent}%</span>
											{/if}
										{:else}
											<span class="text-slate-400">—</span>
										{/if}
									</td>
									<td class="px-4 py-3 text-right">
										{#if isDeficit}
											<span class="inline-flex items-center gap-1 rounded-full bg-rose-100 px-2 py-0.5 text-[10px] font-bold text-rose-700 dark:bg-rose-950 dark:text-rose-300">
												<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
													<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" />
												</svg>
												Deficit
											</span>
										{:else}
											<span class="inline-flex items-center rounded-full bg-emerald-50 px-2 py-0.5 text-[10px] font-semibold text-emerald-700 dark:bg-emerald-950/60 dark:text-emerald-300">
												Profitable
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
	{/if}
</div>
