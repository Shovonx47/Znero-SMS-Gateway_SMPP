<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { RateCard, Rate, RateHistory, Country, Operator, MarginResult } from '$lib/types/api';
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

	// Live Margin Check State
	let isCheckingMargin = $state(false);
	let marginCheckResult = $state<MarginResult | null>(null);
	let marginCheckTimer: ReturnType<typeof setTimeout> | null = null;

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

	const isPlatformOrReseller = $derived(
		auth.tenant?.actor_org_type === 'platform' || auth.tenant?.actor_org_type === 'reseller'
	);
	const canCreateRate = $derived(isPlatformOrReseller && auth.hasPermission('rate.sellrate.create'));
	const canUpdateRate = $derived(isPlatformOrReseller && auth.hasPermission('rate.sellrate.update'));
	const hasMarginOverride = $derived(auth.hasPermission('rate.margin.override'));

	async function loadCardAndRates() {
		if (!isPlatformOrReseller) {
			errorMessage = '403 Forbidden: Customer Sell Cards are accessible to platform and reseller users only.';
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

	function debounceMarginCheck() {
		if (marginCheckTimer) clearTimeout(marginCheckTimer);
		if (!formCountryId || !formPrice || isNaN(Number(formPrice)) || Number(formPrice) <= 0) {
			marginCheckResult = null;
			return;
		}
		marginCheckTimer = setTimeout(async () => {
			isCheckingMargin = true;
			try {
				const res = await apiClient.checkMargin({
					card_id: cardId,
					country_id: formCountryId,
					operator_id: formOperatorId || undefined,
					message_type: formMsgType,
					sell_price: formPrice.trim(),
					currency: card?.currency_code || 'USD'
				});
				marginCheckResult = res.data;
			} catch {
				marginCheckResult = null;
			} finally {
				isCheckingMargin = false;
			}
		}, 300);
	}

	function openCreateRateModal() {
		formCountryId = countries[0]?.id || '';
		formOperatorId = '';
		formMsgType = 'default';
		formPrice = '';
		formEffectiveFrom = new Date().toISOString().slice(0, 16);
		formMinBillingNote = '';
		formReason = '';
		formError = null;
		marginCheckResult = null;
		isCreateRateOpen = true;
	}

	async function handleCreateRate() {
		if (!formCountryId) {
			formError = 'Country is required';
			return;
		}
		if (!formPrice.trim() || isNaN(Number(formPrice))) {
			formError = 'Valid decimal price is required';
			return;
		}
		if (marginCheckResult?.violation && !hasMarginOverride) {
			formError = 'Margin deficit violation: sell price is below upstream buy cost. Permission rate.margin.override required.';
			return;
		}
		isSubmitting = true;
		formError = null;
		try {
			const res = await apiClient.createRate({
				card_id: cardId,
				country_id: formCountryId,
				operator_id: formOperatorId || undefined,
				message_type: formMsgType,
				price: formPrice.trim(),
				effective_from: new Date(formEffectiveFrom).toISOString(),
				min_billing_note: formMinBillingNote.trim() || undefined,
				reason: formReason.trim() || 'Create sell rate'
			});
			if (res.data.pending_approval) {
				toast.info('Sell rate change request submitted for dual-control approval');
			} else {
				toast.success('Sell rate created successfully');
			}
			isCreateRateOpen = false;
			await loadCardAndRates();
		} catch (err) {
			formError = err instanceof ApiClientError ? err.message : 'Failed to save rate.';
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
			await apiClient.closeRate(rateToClose.id, closeReason.trim() || 'Close sell rate');
			toast.success('Rate closed successfully');
			rateToClose = null;
			await loadCardAndRates();
		} catch (err) {
			toast.error(err instanceof ApiClientError ? err.message : 'Failed to close rate');
		} finally {
			isClosing = false;
		}
	}

	async function openRateHistory(rate: Rate) {
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
			const res = await apiClient.rollbackRate(historyRate.id, rollbackVersion, rollbackReason.trim() || 'Rollback sell rate');
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

	async function handleExportCSV() {
		try {
			const blob = await apiClient.exportRates(cardId, 'csv');
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = `sell-rates-${card?.name.toLowerCase().replace(/[^a-z0-9]/g, '-') || cardId}.csv`;
			document.body.appendChild(a);
			a.click();
			document.body.removeChild(a);
			URL.revokeObjectURL(url);
			toast.success('Sell rates exported to CSV');
		} catch (err) {
			toast.error(err instanceof ApiClientError ? err.message : 'Failed to export CSV');
		}
	}
</script>

<div class="space-y-6">
	<!-- Top Breadcrumb & Actions -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<div class="flex items-center gap-2 text-xs text-slate-500">
				<a href="/rates/sell-cards" class="hover:text-indigo-600">Customer Sell Cards</a>
				<span>/</span>
				<span class="text-slate-900 font-medium dark:text-white">{card?.name || 'Loading...'}</span>
			</div>
			<div class="mt-1 flex items-center gap-3">
				<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
					{card?.name || 'Sell Card Rates'}
				</h1>
				{#if card}
					<span class="rounded bg-slate-100 px-2 py-0.5 font-mono text-xs font-semibold dark:bg-slate-800">
						{card.currency_code}
					</span>
					<span class="rounded bg-indigo-50 px-2 py-0.5 font-mono text-xs font-semibold text-indigo-700 dark:bg-indigo-950 dark:text-indigo-300">
						v{card.version}
					</span>
				{/if}
			</div>
			{#if card?.description}
				<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">{card.description}</p>
			{/if}
		</div>

		<div class="flex items-center gap-2">
			<button
				type="button"
				onclick={handleExportCSV}
				class="inline-flex items-center gap-1.5 rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs font-semibold text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" />
				</svg>
				Export CSV
			</button>

			<a
				href="/master-data/import"
				class="inline-flex items-center gap-1.5 rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs font-semibold text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-8l-4-4m0 0L8 8m4-4v12" />
				</svg>
				Import CSV
			</a>

			{#if canCreateRate}
				<button
					type="button"
					onclick={openCreateRateModal}
					class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
					</svg>
					New Sell Rate
				</button>
			{/if}
		</div>
	</div>

	{#if errorMessage}
		<ErrorState message={errorMessage} retry={loadCardAndRates} />
	{:else}
		<!-- Filter bar -->
		<div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900 sm:flex-row sm:items-center">
			<div class="relative flex-1">
				<input
					type="search"
					bind:value={search}
					oninput={() => { currentPage = 1; loadCardAndRates(); }}
					placeholder="Search rates by country name, ISO2, operator name or MCC/MNC..."
					class="w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
				/>
			</div>

			<select
				bind:value={countryFilter}
				onchange={() => { currentPage = 1; loadCardAndRates(); }}
				class="rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
			>
				<option value="">All Countries</option>
				{#each countries as c}
					<option value={c.id}>{c.name} ({c.iso2})</option>
				{/each}
			</select>

			<select
				bind:value={statusFilter}
				onchange={() => { currentPage = 1; loadCardAndRates(); }}
				class="rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
			>
				<option value="">All Statuses</option>
				<option value="active">Active</option>
				<option value="scheduled">Scheduled</option>
				<option value="closed">Closed</option>
			</select>
		</div>

		{#if isLoading}
			<Loading message="Loading sell rates..." />
		{:else if rates.length === 0}
			<Empty
				title="No sell rates defined"
				description="Add country or operator sell rates, or use Import CSV to upload a full rate sheet."
			/>
		{:else}
			<!-- Rates Table -->
			<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="overflow-x-auto">
					<table class="w-full text-left text-xs">
						<thead class="border-b border-slate-200 bg-slate-50 font-semibold text-slate-700 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-300">
							<tr>
								<th class="px-4 py-3">Destination</th>
								<th class="px-4 py-3">Operator</th>
								<th class="px-4 py-3">Type</th>
								<th class="px-4 py-3">Sell Price</th>
								<th class="px-4 py-3">Supplier Cost</th>
								<th class="px-4 py-3">Margin</th>
								<th class="px-4 py-3">Effective Period</th>
								<th class="px-4 py-3">Status</th>
								<th class="px-4 py-3">Ver</th>
								<th class="px-4 py-3 text-right">Actions</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
							{#each rates as rate}
								{@const isDeficit = rate.margin_amount && parseFloat(rate.margin_amount) < 0}
								<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/30 {isDeficit ? 'bg-rose-50/30 dark:bg-rose-950/10' : ''}">
									<td class="px-4 py-3">
										<div class="font-semibold text-slate-900 dark:text-white">
											{rate.country_name || '—'}
										</div>
										<span class="font-mono text-[11px] text-slate-400">
											{rate.country_iso2 || ''}
										</span>
									</td>
									<td class="px-4 py-3">
										{#if rate.operator_name}
											<div class="font-medium text-slate-800 dark:text-slate-200">{rate.operator_name}</div>
											<div class="font-mono text-[10px] text-slate-400">
												MCC:{rate.operator_mcc || '—'} MNC:{rate.operator_mnc || '—'}
											</div>
										{:else}
											<span class="inline-flex items-center rounded bg-slate-100 px-1.5 py-0.5 text-[10px] font-medium text-slate-600 dark:bg-slate-800 dark:text-slate-400">
												Country Default
											</span>
										{/if}
									</td>
									<td class="px-4 py-3 font-mono text-slate-500">
										{rate.message_type}
									</td>
									<td class="px-4 py-3 font-mono font-bold text-slate-900 dark:text-white">
										{rate.price} <span class="text-[10px] font-normal text-slate-400">{card?.currency_code}</span>
									</td>
									<td class="px-4 py-3 font-mono text-slate-600 dark:text-slate-400">
										{#if rate.lowest_buy_cost}
											{rate.lowest_buy_cost}
										{:else}
											<span class="text-slate-400">—</span>
										{/if}
									</td>
									<td class="px-4 py-3">
										{#if rate.margin_amount}
											{#if isDeficit}
												<div class="inline-flex flex-col">
													<span class="inline-flex items-center gap-1 rounded bg-rose-100 px-1.5 py-0.5 font-mono text-[10px] font-bold text-rose-700 dark:bg-rose-950 dark:text-rose-300">
														<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
															<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" />
														</svg>
														{rate.margin_amount} ({rate.margin_percent}%)
													</span>
													<span class="text-[9px] font-bold text-rose-600 uppercase tracking-tight">Deficit</span>
												</div>
											{:else}
												<span class="inline-flex items-center rounded bg-emerald-50 px-1.5 py-0.5 font-mono text-[10px] font-bold text-emerald-700 dark:bg-emerald-950/60 dark:text-emerald-300">
													+{rate.margin_amount} (+{rate.margin_percent}%)
												</span>
											{/if}
										{:else}
											<span class="text-slate-400">—</span>
										{/if}
									</td>
									<td class="px-4 py-3 text-[11px] text-slate-500">
										<div>From: {new Date(rate.effective_from).toLocaleDateString()}</div>
										{#if rate.effective_to}
											<div>To: {new Date(rate.effective_to).toLocaleDateString()}</div>
										{:else}
											<div class="text-emerald-600 dark:text-emerald-400">Open-ended</div>
										{/if}
									</td>
									<td class="px-4 py-3">
										{#if rate.status === 'active'}
											<span class="inline-flex items-center rounded-full bg-emerald-50 px-2 py-0.5 text-[10px] font-semibold text-emerald-700 dark:bg-emerald-950/60 dark:text-emerald-300">
												Active
											</span>
										{:else if rate.status === 'scheduled'}
											<span class="inline-flex items-center rounded-full bg-blue-50 px-2 py-0.5 text-[10px] font-semibold text-blue-700 dark:bg-blue-950/60 dark:text-blue-300">
												Scheduled
											</span>
										{:else}
											<span class="inline-flex items-center rounded-full bg-slate-100 px-2 py-0.5 text-[10px] font-semibold text-slate-600 dark:bg-slate-800 dark:text-slate-400">
												{rate.status}
											</span>
										{/if}
									</td>
									<td class="px-4 py-3 font-mono text-[11px] text-slate-500">
										v{rate.version}
									</td>
									<td class="px-4 py-3 text-right">
										<div class="flex items-center justify-end gap-2">
											<button
												type="button"
												onclick={() => openRateHistory(rate)}
												class="rounded px-2 py-1 text-[11px] font-semibold text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800"
											>
												History
											</button>
											{#if canUpdateRate && rate.status !== 'closed'}
												<button
													type="button"
													onclick={() => openCloseModal(rate)}
													class="rounded px-2 py-1 text-[11px] font-semibold text-rose-600 hover:bg-rose-50 dark:text-rose-400 dark:hover:bg-rose-950/50"
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

				<!-- Pagination footer -->
				{#if totalRates > pageSize}
					<div class="flex items-center justify-between border-t border-slate-200 px-4 py-3 dark:border-slate-800">
						<span class="text-xs text-slate-500">
							Showing {(currentPage - 1) * pageSize + 1} to {Math.min(currentPage * pageSize, totalRates)} of {totalRates} rates
						</span>
						<div class="flex gap-2">
							<button
								type="button"
								disabled={currentPage <= 1}
								onclick={() => { currentPage--; loadCardAndRates(); }}
								class="rounded border border-slate-300 px-3 py-1 text-xs font-semibold text-slate-700 hover:bg-slate-50 disabled:opacity-50 dark:border-slate-700 dark:text-slate-300"
							>
								Previous
							</button>
							<button
								type="button"
								disabled={currentPage * pageSize >= totalRates}
								onclick={() => { currentPage++; loadCardAndRates(); }}
								class="rounded border border-slate-300 px-3 py-1 text-xs font-semibold text-slate-700 hover:bg-slate-50 disabled:opacity-50 dark:border-slate-700 dark:text-slate-300"
							>
								Next
							</button>
						</div>
					</div>
				{/if}
			</div>
		{/if}
	{/if}
</div>

<!-- Create Sell Rate Modal with Live Margin Check -->
{#if isCreateRateOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-lg rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<div class="flex items-center justify-between">
				<h2 class="text-lg font-bold text-slate-900 dark:text-white">New Sell Rate</h2>
				<button
					type="button"
					onclick={() => { isCreateRateOpen = false; }}
					class="rounded p-1 text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
					aria-label="Close modal"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			{#if formError}
				<div class="mt-4 rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300">
					{formError}
				</div>
			{/if}

			<div class="mt-4 space-y-4">
				<div>
					<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="sr-country">Country *</label>
					<select
						id="sr-country"
						bind:value={formCountryId}
						onchange={() => { formOperatorId = ''; debounceMarginCheck(); }}
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
					>
						<option value="">Select country...</option>
						{#each countries as c}
							<option value={c.id}>{c.name} ({c.iso2} / +{c.calling_code})</option>
						{/each}
					</select>
				</div>

				<div>
					<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="sr-op">Operator</label>
					<select
						id="sr-op"
						bind:value={formOperatorId}
						onchange={debounceMarginCheck}
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
					>
						<option value="">Country Default (All Operators)</option>
						{#each filteredOperators as op}
							<option value={op.id}>{op.name} (MCC:{op.mcc || '—'} MNC:{op.mnc || '—'})</option>
						{/each}
					</select>
				</div>

				<div class="grid grid-cols-2 gap-4">
					<div>
						<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="sr-price">
							Sell Price ({card?.currency_code}) *
						</label>
						<input
							id="sr-price"
							type="text"
							bind:value={formPrice}
							oninput={debounceMarginCheck}
							placeholder="0.015000"
							class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs font-mono text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
						/>
					</div>
					<div>
						<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="sr-msgtype">Message Type</label>
						<select
							id="sr-msgtype"
							bind:value={formMsgType}
							class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
						>
							<option value="default">Default</option>
							<option value="promotional">Promotional</option>
							<option value="transactional">Transactional</option>
							<option value="otp">OTP</option>
						</select>
					</div>
				</div>

				<!-- Live Margin Check Indicator -->
				{#if isCheckingMargin}
					<div class="rounded-lg border border-slate-200 bg-slate-50 p-2 text-center text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-800/50">
						Calculating upstream margin floor...
					</div>
				{:else if marginCheckResult}
					{#if marginCheckResult.violation}
						<div class="rounded-xl border border-rose-300 bg-rose-50 p-3 dark:border-rose-900/60 dark:bg-rose-950/40">
							<div class="flex items-start gap-2">
								<svg class="h-4 w-4 mt-0.5 text-rose-600 dark:text-rose-400 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" />
								</svg>
								<div class="text-xs">
									<strong class="text-rose-800 dark:text-rose-200">Margin Deficit Alert!</strong>
									<div class="mt-0.5 text-rose-700 dark:text-rose-300">
										Sell price ({marginCheckResult.currency} {marginCheckResult.sell_price}) is lower than upstream cost ({marginCheckResult.currency} {marginCheckResult.lowest_buy_cost}).
										Negative margin: <strong>{marginCheckResult.margin_amount} ({marginCheckResult.margin_percent}%)</strong>.
									</div>
									{#if hasMarginOverride}
										<div class="mt-1 text-[11px] font-semibold text-emerald-700 dark:text-emerald-300">
											✓ You hold override privilege (rate.margin.override). You may proceed to submit.
										</div>
									{:else}
										<div class="mt-1 text-[11px] font-semibold text-rose-600 dark:text-rose-400">
											✕ Submission blocked: You require rate.margin.override to save deficit rates.
										</div>
									{/if}
								</div>
							</div>
						</div>
					{:else if marginCheckResult.comparable}
						<div class="rounded-xl border border-emerald-300 bg-emerald-50 p-3 dark:border-emerald-900/60 dark:bg-emerald-950/40">
							<div class="flex items-start gap-2">
								<svg class="h-4 w-4 mt-0.5 text-emerald-600 dark:text-emerald-400 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7" />
								</svg>
								<div class="text-xs text-emerald-800 dark:text-emerald-200">
									<strong>Healthy Margin:</strong> Supplier cost {marginCheckResult.currency} {marginCheckResult.lowest_buy_cost} &rarr; Sell {marginCheckResult.currency} {marginCheckResult.sell_price}
									<div class="mt-0.5 font-mono text-[11px] text-emerald-700 dark:text-emerald-300">
										Margin: +{marginCheckResult.margin_amount} (+{marginCheckResult.margin_percent}%)
									</div>
								</div>
							</div>
						</div>
					{:else}
						<div class="rounded-xl border border-slate-200 bg-slate-50 p-2.5 text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-800/40">
							No upstream provider buy rates found for comparison in {card?.currency_code}.
						</div>
					{/if}
				{/if}

				<div class="grid grid-cols-2 gap-4">
					<div>
						<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="sr-eff">Effective From *</label>
						<input
							id="sr-eff"
							type="datetime-local"
							bind:value={formEffectiveFrom}
							class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
						/>
					</div>
					<div>
						<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="sr-note">Billing Note</label>
						<input
							id="sr-note"
							type="text"
							bind:value={formMinBillingNote}
							placeholder="e.g. 1/1 sec or per SMS"
							class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
						/>
					</div>
				</div>

				<div>
					<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="sr-reason">Audit Reason</label>
					<input
						id="sr-reason"
						type="text"
						bind:value={formReason}
						placeholder="Audit reason for creating rate..."
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
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
					disabled={isSubmitting || (marginCheckResult?.violation && !hasMarginOverride)}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50"
				>
					{isSubmitting ? 'Saving...' : 'Save Sell Rate'}
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
				Close sell rate for <strong>{rateToClose.country_name} ({rateToClose.operator_name || 'Country Default'})</strong>? The rate will cease to be effective as of now.
			</p>
			<div class="mt-4">
				<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="cr-reason">Reason *</label>
				<input
					id="cr-reason"
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
					onclick={() => { isRateHistoryOpen = false; }}
					class="rounded p-1 text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
					aria-label="Close modal"
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
