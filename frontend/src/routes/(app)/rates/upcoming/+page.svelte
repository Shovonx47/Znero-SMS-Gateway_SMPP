<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import type { Rate, RateCard } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let rates = $state<Rate[]>([]);
	let cardsMap = $state<Map<string, RateCard>>(new Map());
	let days = $state(30);
	let search = $state('');
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	async function loadUpcoming() {
		isLoading = true;
		errorMessage = null;
		try {
			const [ratesRes, cardsRes] = await Promise.all([
				apiClient.listUpcomingRates(days),
				apiClient.listRateCards({ pageSize: 200 })
			]);
			rates = ratesRes.data || [];
			const m = new Map<string, RateCard>();
			for (const c of cardsRes.data.items || []) {
				m.set(c.id, c);
			}
			cardsMap = m;
		} catch (err) {
			errorMessage = err instanceof ApiClientError ? err.message : 'Failed to load upcoming rate changes.';
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadUpcoming();
	});

	const filteredRates = $derived(
		rates.filter((r) => {
			if (!search.trim()) return true;
			const q = search.toLowerCase();
			const cName = r.country_name?.toLowerCase() || '';
			const cIso = r.country_iso2?.toLowerCase() || '';
			const op = r.operator_name?.toLowerCase() || '';
			const card = cardsMap.get(r.card_id)?.name.toLowerCase() || '';
			return cName.includes(q) || cIso.includes(q) || op.includes(q) || card.includes(q);
		})
	);

	function formatCountdown(effectiveFromStr: string): string {
		const target = new Date(effectiveFromStr).getTime();
		const now = Date.now();
		const diffMs = target - now;
		if (diffMs <= 0) return 'Effective now';
		const diffHours = Math.floor(diffMs / (1000 * 60 * 60));
		if (diffHours < 24) {
			return `In ${diffHours} hr${diffHours === 1 ? '' : 's'}`;
		}
		const diffDays = Math.floor(diffHours / 24);
		return `In ${diffDays} day${diffDays === 1 ? '' : 's'}`;
	}
</script>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Upcoming Rate Changes</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Scheduled future SMS rate adjustments with pending effective dates.
			</p>
		</div>

		<!-- Window Selector -->
		<div class="flex items-center gap-2">
			<span class="text-xs font-semibold text-slate-500">Window:</span>
			<select aria-label="Select lookahead days"
				bind:value={days}
				onchange={loadUpcoming}
				class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-sm focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
			>
				<option value={7}>Next 7 days</option>
				<option value={14}>Next 14 days</option>
				<option value={30}>Next 30 days</option>
				<option value={60}>Next 60 days</option>
				<option value={90}>Next 90 days</option>
			</select>
		</div>
	</div>

	{#if errorMessage}
		<ErrorState message={errorMessage} retry={loadUpcoming} />
	{:else}
		<!-- Filter bar -->
		<div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900 sm:flex-row sm:items-center">
			<div class="relative flex-1">
				<input
					type="search"
					bind:value={search}
					placeholder="Search by country, operator or rate card name..."
					class="w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
				/>
			</div>
		</div>

		{#if isLoading}
			<Loading message="Scanning scheduled future rate changes..." />
		{:else if filteredRates.length === 0}
			<Empty
				title="No upcoming rate changes"
				description="There are no rate updates scheduled to take effect within the next {days} days."
			/>
		{:else}
			<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="overflow-x-auto">
					<table class="w-full text-left text-xs">
						<thead class="border-b border-slate-200 bg-slate-50 font-semibold text-slate-700 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-300">
							<tr>
								<th class="px-4 py-3">Destination</th>
								<th class="px-4 py-3">Operator</th>
								<th class="px-4 py-3">Rate Card</th>
								<th class="px-4 py-3">Type</th>
								<th class="px-4 py-3">New Scheduled Price</th>
								<th class="px-4 py-3">Effective Date</th>
								<th class="px-4 py-3">Countdown</th>
								<th class="px-4 py-3 text-right">Status</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
							{#each filteredRates as r}
								{@const card = cardsMap.get(r.card_id)}
								<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/30">
									<td class="px-4 py-3">
										<div class="font-semibold text-slate-900 dark:text-white">
											{r.country_name || '—'}
										</div>
										<span class="font-mono text-[11px] text-slate-400">
											{r.country_iso2 || ''}
										</span>
									</td>
									<td class="px-4 py-3">
										{#if r.operator_name}
											<div class="font-medium text-slate-800 dark:text-slate-200">{r.operator_name}</div>
											<div class="font-mono text-[10px] text-slate-400">
												MCC:{r.operator_mcc || '—'} MNC:{r.operator_mnc || '—'}
											</div>
										{:else}
											<span class="inline-flex items-center rounded bg-slate-100 px-1.5 py-0.5 text-[10px] font-medium text-slate-600 dark:bg-slate-800 dark:text-slate-400">
												Country Default
											</span>
										{/if}
									</td>
									<td class="px-4 py-3">
										{#if card}
											<a
												href={card.kind === 'provider_buy' ? `/rates/buy-cards/${card.id}` : `/rates/sell-cards/${card.id}`}
												class="font-medium text-indigo-600 hover:text-indigo-800 dark:text-indigo-400"
											>
												{card.name}
											</a>
											<div class="text-[10px] text-slate-400">
												{card.kind === 'provider_buy' ? 'Supplier Buy Card' : 'Customer Sell Card'}
											</div>
										{:else}
											<span class="font-mono text-slate-500">{r.card_id}</span>
										{/if}
									</td>
									<td class="px-4 py-3 font-mono text-slate-500">
										{r.message_type}
									</td>
									<td class="px-4 py-3 font-mono font-bold text-slate-900 dark:text-white">
										{r.price} <span class="text-[10px] font-normal text-slate-400">{card?.currency_code || 'USD'}</span>
									</td>
									<td class="px-4 py-3 text-[11px] text-slate-700 dark:text-slate-300">
										{new Date(r.effective_from).toLocaleString()}
									</td>
									<td class="px-4 py-3">
										<span class="inline-flex items-center rounded-full bg-blue-50 px-2.5 py-0.5 font-mono text-[11px] font-semibold text-blue-700 dark:bg-blue-950/60 dark:text-blue-300">
											{formatCountdown(r.effective_from)}
										</span>
									</td>
									<td class="px-4 py-3 text-right">
										<span class="inline-flex items-center rounded-full bg-amber-50 px-2 py-0.5 text-[10px] font-semibold text-amber-700 dark:bg-amber-950/60 dark:text-amber-300">
											Scheduled
										</span>
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
