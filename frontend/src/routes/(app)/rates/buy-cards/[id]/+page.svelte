<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { RateCard, Rate, RateHistory, Country, Operator } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	const cardId = $derived($page.params.id);

	let card = $state<RateCard | null>(null);
	let rates = $state<Rate[]>([]);
	let totalRates = $state(0);
	let currentPage = $state(1);
	let pageSize = 50;

	let countries = $state<Country[]>([]);
	let operators = $state<Operator[]>([]);

	let search = $state('');
	let countryFilter = $state('');
	let statusFilter = $state('');
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	// Create rate modal
	let isCreateRateOpen = $state(false);
	let isSubmitting = $state(false);
	let formError = $state<string | null>(null);
	let formCountryId = $state('');
	let formOperatorId = $state('');
	let formMsgType = $state('default');
	let formPrice = $state('');
	let formEffectiveFrom = $state(new Date().toISOString().slice(0, 16));
	let formMinBillingNote = $state('');
	let formReason = $state('');

	// Close rate modal
	let rateToClose = $state<Rate | null>(null);
	let closeReason = $state('');
	let isClosing = $state(false);

	// Rate history modal
	let isRateHistoryOpen = $state(false);
	let historyRate = $state<Rate | null>(null);
	let rateHistoryList = $state<RateHistory[]>([]);
	let isHistoryLoading = $state(false);
	let rollbackVersion = $state<number | null>(null);
	let rollbackReason = $state('');
	let isRollingBack = $state(false);

	const isPlatformOrg = $derived(auth.tenant?.actor_org_type === 'platform');
	const canCreateRate = $derived(isPlatformOrg && auth.hasPermission('rate.buyrate.create'));
	const canUpdateRate = $derived(isPlatformOrg && auth.hasPermission('rate.buyrate.update'));

	async function loadCardAndRates() {
		if (!isPlatformOrg) {
			errorMessage = '403 Forbidden: Provider Buy Cards are platform-only.';
			isLoading = false;
			return;
		}
		isLoading = true;
		errorMessage = null;
		try {
			const [cardRes, ratesRes, cRes, opRes] = await Promise.all([
				apiClient.getRateCard(cardId),
				apiClient.listRates({
					card_id: cardId,
					country_id: countryFilter || undefined,
					status: statusFilter || undefined,
					search: search.trim() || undefined,
					page: currentPage,
					pageSize
				}),
				apiClient.listCountries({ pageSize: 300 }),
				apiClient.listOperators({ pageSize: 500 })
			]);
			card = cardRes.data;
			rates = ratesRes.data.items || [];
			totalRates = ratesRes.data.total || 0;
			countries = cRes.data.items || [];
			operators = opRes.data.items || [];
		} catch (err) {
			errorMessage = err instanceof ApiClientError ? err.message : 'Failed to load rate card.';
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadCardAndRates();
	});

	const filteredOperators = $derived(
		formCountryId ? operators.filter((o) => o.country_id === formCountryId) : operators
	);

	function openCreateRateModal() {
		formCountryId = countries[0]?.id || '';
		formOperatorId = '';
		formMsgType = 'default';
		formPrice = '0.005000';
		formEffectiveFrom = new Date().toISOString().slice(0, 16);
		formMinBillingNote = '';
		formReason = '';
		formError = null;
		isCreateRateOpen = true;
	}

	async function handleCreateRate() {
		if (!formCountryId) {
			formError = 'Country destination is required';
			return;
		}
		if (!formPrice || isNaN(Number(formPrice)) || Number(formPrice) <= 0) {
			formError = 'Valid positive price is required';
			return;
		}
		isSubmitting = true;
		formError = null;
		try {
			const effDate = new Date(formEffectiveFrom).toISOString();
			const res = await apiClient.createRate({
				card_id: cardId,
				country_id: formCountryId,
				operator_id: formOperatorId || null,
				message_type: formMsgType,
				price: formPrice,
				effective_from: effDate,
				min_billing_note: formMinBillingNote.trim() || undefined,
				reason: formReason.trim() || 'Create supplier buy rate'
			});
			if (res.data.pending_approval) {
				toast.info('Rate creation submitted for maker-checker review');
			} else {
				toast.success('Buy rate saved successfully');
			}
			isCreateRateOpen = false;
			await loadCardAndRates();
		} catch (err) {
			formError = err instanceof ApiClientError ? err.message : 'Failed to save buy rate.';
		} finally {
			isSubmitting = false;
		}
	}

	function openCloseModal(rate: Rate) {
		rateToClose = rate;
		closeReason = '';
	}

	async function handleCloseRate() {
		if (!rateToClose) return;
		isClosing = true;
		try {
			await apiClient.closeRate(rateToClose.id, closeReason.trim() || 'Close rate window');
			toast.success('Rate closed as of current timestamp');
			rateToClose = null;
			await loadCardAndRates();
		} catch (err) {
			toast.error(err instanceof ApiClientError ? err.message : 'Failed to close rate');
		} finally {
			isClosing = false;
		}
	}

	async function openHistory(rate: Rate) {
		historyRate = rate;
		isRateHistoryOpen = true;
		isHistoryLoading = true;
		rollbackVersion = null;
		rollbackReason = '';
		try {
			const res = await apiClient.getRateHistory(rate.id);
			rateHistoryList = res.data || [];
		} catch (err) {
			toast.error(err instanceof ApiClientError ? err.message : 'Failed to load rate history');
		} finally {
			isHistoryLoading = false;
		}
	}

	async function handleRollbackRate() {
		if (!historyRate || !rollbackVersion) return;
		isRollingBack = true;
		try {
			const res = await apiClient.rollbackRate(historyRate.id, rollbackVersion, rollbackReason.trim() || 'Rollback rate');
			if (res.data.pending_approval) {
				toast.info('Rate rollback submitted for dual-control approval');
			} else {
				toast.success(`Rate rolled back to version ${rollbackVersion}`);
			}
			isRateHistoryOpen = false;
			await loadCardAndRates();
		} catch (err) {
			toast.error(err instanceof ApiClientError ? err.message : 'Failed to rollback rate');
		} finally {
			isRollingBack = false;
		}
	}

	async function handleExport(format: 'csv' | 'json') {
		try {
			const blob = await apiClient.exportRates(cardId, format);
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = `buy_rates_${card?.name.replace(/[^a-zA-Z0-9]/g, '_')}_${Date.now()}.${format}`;
			document.body.appendChild(a);
			a.click();
			document.body.removeChild(a);
			URL.revokeObjectURL(url);
			toast.success(`Exported ${format.toUpperCase()} rate sheet`);
		} catch {
			toast.error('Failed to export rate sheet');
		}
	}
</script>

<div class="space-y-6">
	<!-- Breadcrumbs -->
	<nav class="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
		<a href="/rates/buy-cards" class="hover:text-indigo-600 dark:hover:text-indigo-400">Provider Buy Cards</a>
		<span>/</span>
		<span class="font-semibold text-slate-900 dark:text-white">{card?.name || 'Card Details'}</span>
	</nav>

	{#if errorMessage}
		<ErrorState message={errorMessage} retry={loadCardAndRates} />
	{:else if card}
		<!-- Card Header Card -->
		<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
				<div>
					<div class="flex items-center gap-3">
						<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">{card.name}</h1>
						<span class="rounded-full px-2.5 py-0.5 text-xs font-semibold {card.status === 'active' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300' : 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300'}">
							{card.status}
						</span>
						<span class="font-mono text-xs text-slate-400">v{card.version}</span>
					</div>
					{#if card.description}
						<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">{card.description}</p>
					{/if}
					<div class="mt-3 flex flex-wrap gap-4 text-xs text-slate-500">
						<div>Currency: <strong class="text-slate-900 dark:text-white">{card.currency_code}</strong></div>
						<div>Channel: <strong class="uppercase text-slate-900 dark:text-white">{card.channel}</strong></div>
						<div>Total Active Rates: <strong class="text-slate-900 dark:text-white">{totalRates}</strong></div>
					</div>
				</div>

				<div class="flex flex-wrap items-center gap-2">
					<button
						type="button"
						onclick={() => handleExport('csv')}
						class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs font-semibold text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300"
					>
						Export CSV
					</button>
					<button
						type="button"
						onclick={() => handleExport('json')}
						class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs font-semibold text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300"
					>
						Export JSON
					</button>
					{#if canCreateRate}
						<button
							type="button"
							onclick={openCreateRateModal}
							class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500"
						>
							Add Buy Rate
						</button>
					{/if}
				</div>
			</div>
		</div>

		<!-- Filter Bar -->
		<div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900 sm:flex-row sm:items-center">
			<div class="relative flex-1">
				<input aria-label="Select search"
					type="search"
					bind:value={search}
					oninput={() => { currentPage = 1; loadCardAndRates(); }}
					placeholder="Search rates by country, operator, MCC/MNC..."
					class="w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
				/>
			</div>
			<select aria-label="Filter by country"
				bind:value={countryFilter}
				onchange={() => { currentPage = 1; loadCardAndRates(); }}
				class="rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
			>
				<option value="">All Countries</option>
				{#each countries as c}
					<option value={c.id}>{c.name} ({c.iso2})</option>
				{/each}
			</select>
			<select aria-label="Filter by status"
				bind:value={statusFilter}
				onchange={() => { currentPage = 1; loadCardAndRates(); }}
				class="rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
			>
				<option value="">All Statuses</option>
				<option value="active">Active</option>
				<option value="closed">Closed</option>
			</select>
		</div>

		{#if isLoading}
			<Loading message="Loading rates..." />
		{:else if rates.length === 0}
			<Empty
				title="No rates found on this buy card"
				description="Click 'Add Buy Rate' or use the Master Data Import Center to upload a supplier rate sheet."
			/>
		{:else}
			<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<table class="w-full text-left text-xs text-slate-600 dark:text-slate-300">
					<thead class="border-b border-slate-200 bg-slate-50 text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-400">
						<tr>
							<th class="px-4 py-3">Destination</th>
							<th class="px-4 py-3">Operator</th>
							<th class="px-4 py-3">Type</th>
							<th class="px-4 py-3">Price ({card.currency_code})</th>
							<th class="px-4 py-3">Effective Interval</th>
							<th class="px-4 py-3">Status</th>
							<th class="px-4 py-3 text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
						{#each rates as rate}
							<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/40">
								<td class="px-4 py-3 font-semibold text-slate-900 dark:text-white">
									<div class="flex items-center gap-2">
										<span class="font-mono text-indigo-600 dark:text-indigo-400">{rate.country_iso2}</span>
										<span>{rate.country_name}</span>
									</div>
								</td>
								<td class="px-4 py-3">
									{#if rate.operator_name}
										<span>{rate.operator_name}</span>
										<span class="text-[10px] text-slate-400 font-mono">({rate.operator_mcc}-{rate.operator_mnc})</span>
									{:else}
										<span class="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] text-slate-500 dark:bg-slate-800">Country Default</span>
									{/if}
								</td>
								<td class="px-4 py-3 uppercase text-[10px] text-slate-500">
									{rate.message_type}
								</td>
								<td class="px-4 py-3 font-mono font-bold text-slate-900 dark:text-white">
									{rate.price}
								</td>
								<td class="px-4 py-3 text-[11px] text-slate-500">
									<div>From: {new Date(rate.effective_from).toLocaleDateString()}</div>
									{#if rate.effective_to}
										<div class="text-amber-600 dark:text-amber-400">To: {new Date(rate.effective_to).toLocaleDateString()}</div>
									{:else}
										<div class="text-emerald-600 dark:text-emerald-400">Open-ended</div>
									{/if}
								</td>
								<td class="px-4 py-3">
									<span class="inline-flex rounded-full px-2 py-0.5 text-[10px] font-semibold {rate.status === 'active' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300' : 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300'}">
										{rate.status}
									</span>
								</td>
								<td class="px-4 py-3 text-right">
									<div class="inline-flex items-center gap-2">
										<button
											type="button"
											onclick={() => openHistory(rate)}
											class="rounded px-2 py-1 text-[11px] font-medium text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
										>
											History
										</button>
										{#if canUpdateRate && rate.status === 'active' && !rate.effective_to}
											<button
												type="button"
												onclick={() => openCloseModal(rate)}
												class="rounded px-2 py-1 text-[11px] font-medium text-rose-600 hover:bg-rose-50 dark:text-rose-400 dark:hover:bg-slate-800"
											>
												Close
											</button>
										{/if}
									</div>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	{/if}
</div>

<!-- Add Buy Rate Modal -->
{#if isCreateRateOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-lg rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-lg font-bold text-slate-900 dark:text-white">Add Buy Rate</h2>
			<p class="mt-1 text-xs text-slate-500">Assign a supplier rate to a destination and operator.</p>

			{#if formError}
				<div class="mt-4 rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300">
					{formError}
				</div>
			{/if}

			<div class="mt-4 space-y-4 text-xs">
				<div>
					<label class="block font-semibold text-slate-700 dark:text-slate-300" for="country-select">Country Destination *</label>
					<select
						id="country-select"
						bind:value={formCountryId}
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-slate-900 dark:border-slate-700 dark:text-white"
					>
						{#each countries as c}
							<option value={c.id}>{c.name} ({c.iso2})</option>
						{/each}
					</select>
				</div>

				<div>
					<label class="block font-semibold text-slate-700 dark:text-slate-300" for="operator-select">Operator (Optional - leave empty for Country Default)</label>
					<select
						id="operator-select"
						bind:value={formOperatorId}
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-slate-900 dark:border-slate-700 dark:text-white"
					>
						<option value="">Country Default (All Networks)</option>
						{#each filteredOperators as op}
							<option value={op.id}>{op.name} ({op.mcc}-{op.mnc})</option>
						{/each}
					</select>
				</div>

				<div class="grid grid-cols-2 gap-4">
					<div>
						<label class="block font-semibold text-slate-700 dark:text-slate-300" for="buy-price">Price ({card?.currency_code}) *</label>
						<input
							id="buy-price"
							type="text"
							bind:value={formPrice}
							placeholder="0.005000"
							class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 font-mono font-bold text-slate-900 dark:border-slate-700 dark:text-white"
						/>
					</div>
					<div>
						<label class="block font-semibold text-slate-700 dark:text-slate-300" for="effective-from">Effective From *</label>
						<input
							id="effective-from"
							type="datetime-local"
							bind:value={formEffectiveFrom}
							class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-slate-900 dark:border-slate-700 dark:text-white"
						/>
					</div>
				</div>

				<div>
					<label class="block font-semibold text-slate-700 dark:text-slate-300" for="min-billing">Billing Note / Minimum</label>
					<input
						id="min-billing"
						type="text"
						bind:value={formMinBillingNote}
						placeholder="e.g. 1/1 billing increment"
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-slate-900 dark:border-slate-700 dark:text-white"
					/>
				</div>

				<div>
					<label class="block font-semibold text-slate-700 dark:text-slate-300" for="rate-reason">Reason for Audit Log</label>
					<input
						id="rate-reason"
						type="text"
						bind:value={formReason}
						placeholder="e.g. Price update per supplier notice"
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-slate-900 dark:border-slate-700 dark:text-white"
					/>
				</div>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					type="button"
					onclick={() => { isCreateRateOpen = false; }}
					class="rounded-lg border border-slate-300 px-4 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
				>
					Cancel
				</button>
				<button
					type="button"
					onclick={handleCreateRate}
					disabled={isSubmitting}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50"
				>
					{isSubmitting ? 'Saving...' : 'Save Buy Rate'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Close Rate Modal -->
{#if rateToClose}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-lg font-bold text-rose-600">Close Rate</h2>
			<p class="mt-2 text-xs text-slate-600 dark:text-slate-300">
				Close rate for <strong>{rateToClose.country_name} ({rateToClose.operator_name || 'Country Default'})</strong>? The rate will cease to be effective as of now.
			</p>
			<div class="mt-4">
				<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="close-reason">Reason *</label>
				<input
					id="close-reason"
					type="text"
					bind:value={closeReason}
					placeholder="Reason for closing rate..."
					class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 dark:border-slate-700 dark:text-white"
				/>
			</div>
			<div class="mt-6 flex justify-end gap-3">
				<button
					type="button"
					onclick={() => { rateToClose = null; }}
					class="rounded-lg border border-slate-300 px-4 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
				>
					Cancel
				</button>
				<button
					type="button"
					onclick={handleCloseRate}
					disabled={isClosing}
					class="rounded-lg bg-rose-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-rose-500 disabled:opacity-50"
				>
					{isClosing ? 'Closing...' : 'Confirm Close'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Rate History & Rollback Modal -->
{#if isRateHistoryOpen && historyRate}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-2xl rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<div class="flex items-center justify-between">
				<div>
					<h2 class="text-lg font-bold text-slate-900 dark:text-white">
						Rate History: {historyRate.country_name} ({historyRate.operator_name || 'Country Default'})
					</h2>
					<p class="text-xs text-slate-500">Past version snapshots for this destination rate.</p>
				</div>
				<button
					type="button"
					aria-label="Close modal"
					onclick={() => { isRateHistoryOpen = false; }}
					class="rounded p-1 text-slate-400 hover:text-slate-600"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			<div class="mt-4 max-h-80 overflow-y-auto divide-y divide-slate-100 dark:divide-slate-800">
				{#if isHistoryLoading}
					<Loading message="Loading rate history..." />
				{:else if rateHistoryList.length === 0}
					<div class="py-8 text-center text-xs text-slate-400">No history records found.</div>
				{:else}
					{#each rateHistoryList as item}
						<div class="flex items-center justify-between py-3">
							<div>
								<div class="flex items-center gap-2">
									<span class="rounded bg-indigo-50 px-1.5 py-0.5 font-mono text-[11px] font-bold text-indigo-700 dark:bg-indigo-950 dark:text-indigo-300">
										v{item.version}
									</span>
									<span class="font-mono font-bold text-slate-900 dark:text-white">{item.price} {card?.currency_code}</span>
								</div>
								<div class="mt-1 text-[11px] text-slate-500">
									Changed at {new Date(item.changed_at).toLocaleString()} &bull; Reason: {item.change_reason || '—'}
								</div>
							</div>
							{#if item.version < historyRate.version}
								<button
									type="button"
									onclick={() => { rollbackVersion = item.version; }}
									class="rounded border border-indigo-200 px-2.5 py-1 text-[11px] font-semibold text-indigo-600 hover:bg-indigo-50 dark:border-indigo-800 dark:text-indigo-400"
								>
									Rollback to v{item.version}
								</button>
							{/if}
						</div>
					{/each}
				{/if}
			</div>

			{#if rollbackVersion}
				<div class="mt-4 rounded-xl border border-amber-200 bg-amber-50 p-4 dark:border-amber-900/50 dark:bg-amber-950/30">
					<h3 class="text-xs font-bold text-amber-900 dark:text-amber-200">Rollback Rate to Version {rollbackVersion}</h3>
					<div class="mt-2">
						<input
							type="text"
							bind:value={rollbackReason}
							placeholder="Reason for rolling back..."
							class="w-full rounded-lg border border-amber-300 bg-white px-3 py-1.5 text-xs text-slate-900 dark:border-amber-800 dark:bg-slate-900 dark:text-white"
						/>
					</div>
					<div class="mt-3 flex justify-end gap-2">
						<button
							type="button"
							onclick={() => { rollbackVersion = null; }}
							class="rounded px-3 py-1 text-xs text-slate-600 dark:text-slate-400"
						>
							Cancel
						</button>
						<button
							type="button"
							onclick={handleRollbackRate}
							disabled={isRollingBack}
							class="rounded bg-amber-600 px-3 py-1 text-xs font-semibold text-white shadow-sm hover:bg-amber-500"
						>
							{isRollingBack ? 'Applying...' : 'Confirm Rollback'}
						</button>
					</div>
				</div>
			{/if}
		</div>
	</div>
{/if}
