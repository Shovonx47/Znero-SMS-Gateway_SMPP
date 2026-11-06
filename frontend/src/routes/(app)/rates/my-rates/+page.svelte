<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { RateCard, Rate } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let card = $state<RateCard | null>(null);
	let rates = $state<Rate[]>([]);
	let search = $state('');
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	const isCustomer = $derived(auth.tenant?.actor_org_type === 'customer');

	async function loadCustomerRates() {
		if (!isCustomer) {
			errorMessage = '403 Forbidden: Customer SMS Rate Sheet is accessible to customer accounts only.';
			isLoading = false;
			return;
		}
		isLoading = true;
		errorMessage = null;
		try {
			const res = await apiClient.getMyRates();
			card = res.data.card;
			rates = res.data.rates || [];
		} catch (err) {
			errorMessage = err instanceof ApiClientError ? err.message : 'Failed to load SMS rate sheet.';
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadCustomerRates();
	});

	const filteredRates = $derived(
		rates.filter((r) => {
			if (!search.trim()) return true;
			const q = search.toLowerCase();
			return (
				(r.country_name && r.country_name.toLowerCase().includes(q)) ||
				(r.country_iso2 && r.country_iso2.toLowerCase().includes(q)) ||
				(r.operator_name && r.operator_name.toLowerCase().includes(q))
			);
		})
	);

	function exportCSV() {
		if (filteredRates.length === 0) return;
		const headers = ['Country', 'ISO2', 'Operator', 'MCC', 'MNC', 'Message Type', 'Price per SMS', 'Currency'];
		const rows = filteredRates.map((r) => [
			`"${r.country_name || ''}"`,
			r.country_iso2 || '',
			`"${r.operator_name || 'Country Default'}"`,
			r.operator_mcc || '',
			r.operator_mnc || '',
			r.message_type,
			r.price,
			card?.currency_code || 'USD'
		]);
		const csvContent = 'data:text/csv;charset=utf-8,' + [headers.join(','), ...rows.map((row) => row.join(','))].join('\n');
		const encodedUri = encodeURI(csvContent);
		const link = document.createElement('a');
		link.setAttribute('href', encodedUri);
		link.setAttribute('download', `sms-rates-${card?.name.toLowerCase().replace(/[^a-z0-9]/g, '-') || 'card'}.csv`);
		document.body.appendChild(link);
		link.click();
		document.body.removeChild(link);
		toast.success('SMS rates exported to CSV');
	}
</script>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">My SMS Rates</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Your active pricing sheet for outbound SMS messaging worldwide.
			</p>
		</div>

		{#if card && filteredRates.length > 0}
			<button
				type="button"
				onclick={exportCSV}
				class="inline-flex items-center gap-1.5 rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs font-semibold text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" />
				</svg>
				Export CSV
			</button>
		{/if}
	</div>

	{#if errorMessage}
		<ErrorState message={errorMessage} retry={loadCustomerRates} />
	{:else if isLoading}
		<Loading message="Loading your SMS rate sheet..." />
	{:else if !card}
		<Empty
			title="No rate card assigned"
			description="Your account does not currently have an active rate card assigned. Please contact your account manager."
		/>
	{:else}
		<!-- Card Meta -->
		<div class="rounded-2xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
				<div>
					<div class="flex items-center gap-2">
						<span class="rounded bg-indigo-50 px-2 py-0.5 font-mono text-xs font-bold text-indigo-700 dark:bg-indigo-950 dark:text-indigo-300">
							{card.currency_code}
						</span>
						<h2 class="text-lg font-bold text-slate-900 dark:text-white">{card.name}</h2>
					</div>
					{#if card.description}
						<p class="mt-1 text-xs text-slate-500">{card.description}</p>
					{/if}
				</div>

				<div class="text-right text-xs text-slate-500">
					<div>Available Destinations: <strong class="text-slate-900 dark:text-white">{rates.length}</strong></div>
					<div>Currency: <strong class="text-slate-900 dark:text-white">{card.currency_code}</strong></div>
				</div>
			</div>
		</div>

		<!-- Search Bar -->
		<div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900 sm:flex-row sm:items-center">
			<div class="relative flex-1">
				<input
					type="search"
					bind:value={search}
					placeholder="Search destinations by country name, ISO2 or operator..."
					class="w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
				/>
			</div>
		</div>

		<!-- Table -->
		<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50 font-semibold text-slate-700 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-300">
						<tr>
							<th class="px-4 py-3">Destination</th>
							<th class="px-4 py-3">Operator</th>
							<th class="px-4 py-3">Type</th>
							<th class="px-4 py-3">Rate per SMS</th>
							<th class="px-4 py-3">Effective Period</th>
							<th class="px-4 py-3 text-right">Status</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
						{#each filteredRates as r}
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
								<td class="px-4 py-3 font-mono text-slate-500">
									{r.message_type}
								</td>
								<td class="px-4 py-3 font-mono font-bold text-indigo-600 dark:text-indigo-400">
									{r.price} <span class="text-[10px] font-normal text-slate-400">{card.currency_code}</span>
								</td>
								<td class="px-4 py-3 text-[11px] text-slate-500">
									<div>From: {new Date(r.effective_from).toLocaleDateString()}</div>
									{#if r.effective_to}
										<div>To: {new Date(r.effective_to).toLocaleDateString()}</div>
									{:else}
										<div class="text-emerald-600 dark:text-emerald-400">Open-ended</div>
									{/if}
								</td>
								<td class="px-4 py-3 text-right">
									<span class="inline-flex items-center rounded-full bg-emerald-50 px-2 py-0.5 text-[10px] font-semibold text-emerald-700 dark:bg-emerald-950/60 dark:text-emerald-300">
										Active
									</span>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>
	{/if}
</div>
