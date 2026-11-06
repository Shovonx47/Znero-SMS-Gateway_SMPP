<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { RateCard, RateCardHistory } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let cards = $state<RateCard[]>([]);
	let totalCount = $state(0);
	let page = $state(1);
	let pageSize = 25;
	let search = $state('');
	let statusFilter = $state('');
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	// Create modal
	let isCreateModalOpen = $state(false);
	let isSubmitting = $state(false);
	let formError = $state<string | null>(null);
	let formName = $state('');
	let formCurrency = $state('USD');
	let formChannel = $state('sms');
	let formStatus = $state<'draft' | 'active' | 'inactive'>('active');
	let formDescription = $state('');
	let formReason = $state('');

	// Archive modal
	let cardToArchive = $state<RateCard | null>(null);
	let archiveReason = $state('');
	let isArchiving = $state(false);

	// History modal
	let isHistoryModalOpen = $state(false);
	let historyCard = $state<RateCard | null>(null);
	let historyList = $state<RateCardHistory[]>([]);
	let isHistoryLoading = $state(false);
	let rollbackVersion = $state<number | null>(null);
	let rollbackReason = $state('');
	let isRollingBack = $state(false);

	const isPlatformOrReseller = $derived(
		auth.tenant?.actor_org_type === 'platform' || auth.tenant?.actor_org_type === 'reseller'
	);
	const canCreate = $derived(isPlatformOrReseller && auth.hasPermission('rate.sellcard.create'));
	const canArchive = $derived(isPlatformOrReseller && auth.hasPermission('rate.sellcard.archive'));

	async function loadData() {
		if (!isPlatformOrReseller) {
			errorMessage = '403 Forbidden: Customer Sell Cards are accessible to platform and reseller users only.';
			isLoading = false;
			return;
		}
		isLoading = true;
		errorMessage = null;
		try {
			const res = await apiClient.listRateCards({
				kind: 'customer_sell',
				status: statusFilter || undefined,
				search: search.trim() || undefined,
				page,
				pageSize
			});
			cards = res.data.items || [];
			totalCount = res.data.total || 0;
		} catch (err) {
			errorMessage = err instanceof ApiClientError ? err.message : 'Failed to load sell rate cards.';
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadData();
	});

	function openCreateModal() {
		formName = '';
		formCurrency = 'USD';
		formChannel = 'sms';
		formStatus = 'active';
		formDescription = '';
		formReason = '';
		formError = null;
		isCreateModalOpen = true;
	}

	async function handleCreate() {
		if (!formName.trim()) {
			formError = 'Card name is required';
			return;
		}
		isSubmitting = true;
		formError = null;
		try {
			const res = await apiClient.createRateCard({
				name: formName.trim(),
				kind: 'customer_sell',
				currency_code: formCurrency.toUpperCase(),
				channel: formChannel,
				status: formStatus,
				description: formDescription.trim(),
				reason: formReason.trim() || 'Create customer sell card'
			});
			if (res.data.pending_approval) {
				toast.info('Sell card change request submitted for dual-control approval');
			} else {
				toast.success('Customer sell card created successfully');
			}
			isCreateModalOpen = false;
			await loadData();
		} catch (err) {
			formError = err instanceof ApiClientError ? err.message : 'Failed to create sell card.';
		} finally {
			isSubmitting = false;
		}
	}

	function openArchiveModal(card: RateCard) {
		cardToArchive = card;
		archiveReason = '';
	}

	async function handleArchive() {
		if (!cardToArchive) return;
		isArchiving = true;
		try {
			await apiClient.archiveRateCard(cardToArchive.id, archiveReason.trim() || 'Archive sell card');
			toast.success(`Rate card ${cardToArchive.name} archived`);
			cardToArchive = null;
			await loadData();
		} catch (err) {
			toast.error(err instanceof ApiClientError ? err.message : 'Failed to archive rate card');
		} finally {
			isArchiving = false;
		}
	}

	async function openHistory(card: RateCard) {
		historyCard = card;
		isHistoryModalOpen = true;
		isHistoryLoading = true;
		rollbackVersion = null;
		rollbackReason = '';
		try {
			const res = await apiClient.getRateCardHistory(card.id);
			historyList = res.data || [];
		} catch (err) {
			toast.error(err instanceof ApiClientError ? err.message : 'Failed to load card history');
		} finally {
			isHistoryLoading = false;
		}
	}

	async function handleRollback() {
		if (!historyCard || !rollbackVersion) return;
		isRollingBack = true;
		try {
			const res = await apiClient.rollbackRateCard(historyCard.id, rollbackVersion, rollbackReason.trim() || 'Rollback version');
			if (res.data.pending_approval) {
				toast.info('Rollback submitted for dual-control approval');
			} else {
				toast.success(`Rate card rolled back to version ${rollbackVersion}`);
			}
			isHistoryModalOpen = false;
			await loadData();
		} catch (err) {
			toast.error(err instanceof ApiClientError ? err.message : 'Failed to rollback card');
		} finally {
			isRollingBack = false;
		}
	}
</script>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Customer Sell Cards</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Manage SMS selling rate sheets assigned to customers and sub-resellers.
			</p>
		</div>

		{#if canCreate}
			<button
				type="button"
				onclick={openCreateModal}
				class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2.5 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
				</svg>
				New Sell Card
			</button>
		{/if}
	</div>

	{#if errorMessage}
		<ErrorState message={errorMessage} retry={loadData} />
	{:else}
		<!-- Filter bar -->
		<div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900 sm:flex-row sm:items-center">
			<div class="relative flex-1">
				<input
					type="search"
					bind:value={search}
					oninput={() => { page = 1; loadData(); }}
					placeholder="Search sell cards by name or description..."
					class="w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
				/>
			</div>
			<select
				bind:value={statusFilter}
				onchange={() => { page = 1; loadData(); }}
				class="rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
			>
				<option value="">All Statuses</option>
				<option value="active">Active</option>
				<option value="draft">Draft</option>
				<option value="inactive">Inactive</option>
			</select>
		</div>

		{#if isLoading}
			<Loading message="Loading customer sell cards..." />
		{:else if cards.length === 0}
			<Empty
				title="No customer sell cards found"
				description="Create a sell rate card to begin managing pricing for your customers."
			/>
		{:else}
			<!-- Table -->
			<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="overflow-x-auto">
					<table class="w-full text-left text-xs">
						<thead class="border-b border-slate-200 bg-slate-50 font-semibold text-slate-700 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-300">
							<tr>
								<th class="px-4 py-3">Card Name</th>
								<th class="px-4 py-3">Currency</th>
								<th class="px-4 py-3">Channel</th>
								<th class="px-4 py-3">Status</th>
								<th class="px-4 py-3">Rates Count</th>
								<th class="px-4 py-3">Version</th>
								<th class="px-4 py-3">Updated At</th>
								<th class="px-4 py-3 text-right">Actions</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
							{#each cards as card}
								<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/30">
									<td class="px-4 py-3">
										<a
											href="/rates/sell-cards/{card.id}"
											class="font-semibold text-indigo-600 hover:text-indigo-800 dark:text-indigo-400 dark:hover:text-indigo-300"
										>
											{card.name}
										</a>
										{#if card.description}
											<div class="mt-0.5 text-[11px] text-slate-400 truncate max-w-xs">{card.description}</div>
										{/if}
									</td>
									<td class="px-4 py-3 font-mono font-medium">
										<span class="rounded bg-slate-100 px-2 py-0.5 text-[11px] dark:bg-slate-800">
											{card.currency_code}
										</span>
									</td>
									<td class="px-4 py-3 uppercase tracking-wider text-slate-500 font-mono text-[11px]">
										{card.channel}
									</td>
									<td class="px-4 py-3">
										{#if card.status === 'active'}
											<span class="inline-flex items-center rounded-full bg-emerald-50 px-2 py-0.5 text-[11px] font-semibold text-emerald-700 dark:bg-emerald-950/60 dark:text-emerald-300">
												Active
											</span>
										{:else if card.status === 'draft'}
											<span class="inline-flex items-center rounded-full bg-amber-50 px-2 py-0.5 text-[11px] font-semibold text-amber-700 dark:bg-amber-950/60 dark:text-amber-300">
												Draft
											</span>
										{:else}
											<span class="inline-flex items-center rounded-full bg-slate-100 px-2 py-0.5 text-[11px] font-semibold text-slate-600 dark:bg-slate-800 dark:text-slate-400">
												{card.status}
											</span>
										{/if}
									</td>
									<td class="px-4 py-3 font-mono">
										{card.rate_count ?? '—'}
									</td>
									<td class="px-4 py-3">
										<span class="rounded bg-indigo-50 px-1.5 py-0.5 font-mono text-[11px] font-semibold text-indigo-700 dark:bg-indigo-950 dark:text-indigo-300">
											v{card.version}
										</span>
									</td>
									<td class="px-4 py-3 text-slate-500 text-[11px]">
										{new Date(card.updated_at).toLocaleDateString()}
									</td>
									<td class="px-4 py-3 text-right">
										<div class="flex items-center justify-end gap-2">
											<a
												href="/rates/sell-cards/{card.id}"
												class="rounded px-2 py-1 text-[11px] font-semibold text-indigo-600 hover:bg-indigo-50 dark:text-indigo-400 dark:hover:bg-indigo-950/50"
											>
												View Rates
											</a>
											<button
												type="button"
												onclick={() => openHistory(card)}
												class="rounded px-2 py-1 text-[11px] font-semibold text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800"
											>
												History
											</button>
											{#if canArchive && card.status !== 'archived'}
												<button
													type="button"
													onclick={() => openArchiveModal(card)}
													class="rounded px-2 py-1 text-[11px] font-semibold text-rose-600 hover:bg-rose-50 dark:text-rose-400 dark:hover:bg-rose-950/50"
												>
													Archive
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
				{#if totalCount > pageSize}
					<div class="flex items-center justify-between border-t border-slate-200 px-4 py-3 dark:border-slate-800">
						<span class="text-xs text-slate-500">
							Showing {(page - 1) * pageSize + 1} to {Math.min(page * pageSize, totalCount)} of {totalCount} cards
						</span>
						<div class="flex gap-2">
							<button
								type="button"
								disabled={page <= 1}
								onclick={() => { page--; loadData(); }}
								class="rounded border border-slate-300 px-3 py-1 text-xs font-semibold text-slate-700 hover:bg-slate-50 disabled:opacity-50 dark:border-slate-700 dark:text-slate-300"
							>
								Previous
							</button>
							<button
								type="button"
								disabled={page * pageSize >= totalCount}
								onclick={() => { page++; loadData(); }}
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

<!-- Create Modal -->
{#if isCreateModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-lg rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<div class="flex items-center justify-between">
				<h2 class="text-lg font-bold text-slate-900 dark:text-white">New Customer Sell Card</h2>
				<button
					type="button"
					aria-label="Close modal"
					onclick={() => { isCreateModalOpen = false; }}
					class="rounded p-1 text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
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
					<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="sc-name">Card Name *</label>
					<input
						id="sc-name"
						type="text"
						bind:value={formName}
						placeholder="e.g. Standard Customer Sell Card 2026"
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
					/>
				</div>

				<div class="grid grid-cols-2 gap-4">
					<div>
						<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="sc-curr">Currency *</label>
						<select
							id="sc-curr"
							bind:value={formCurrency}
							class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
						>
							<option value="USD">USD ($)</option>
							<option value="EUR">EUR (€)</option>
							<option value="GBP">GBP (£)</option>
							<option value="BDT">BDT (৳)</option>
							<option value="INR">INR (₹)</option>
							<option value="AED">AED (د.إ)</option>
						</select>
					</div>
					<div>
						<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="sc-status">Initial Status</label>
						<select
							id="sc-status"
							bind:value={formStatus}
							class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
						>
							<option value="active">Active</option>
							<option value="draft">Draft</option>
							<option value="inactive">Inactive</option>
						</select>
					</div>
				</div>

				<div>
					<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="sc-desc">Description</label>
					<textarea
						id="sc-desc"
						bind:value={formDescription}
						rows="2"
						placeholder="Optional notes or rate card details..."
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
					></textarea>
				</div>

				<div>
					<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="sc-reason">Change Reason</label>
					<input
						id="sc-reason"
						type="text"
						bind:value={formReason}
						placeholder="Audit reason for creating rate card..."
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
					/>
				</div>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					type="button"
					onclick={() => { isCreateModalOpen = false; }}
					class="rounded-lg border border-slate-300 px-4 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
				>
					Cancel
				</button>
				<button
					type="button"
					onclick={handleCreate}
					disabled={isSubmitting}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50"
				>
					{isSubmitting ? 'Creating...' : 'Create Sell Card'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Archive Modal -->
{#if cardToArchive}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-lg font-bold text-rose-600">Archive Rate Card</h2>
			<p class="mt-2 text-xs text-slate-600 dark:text-slate-300">
				Are you sure you want to archive <strong>{cardToArchive.name}</strong>? Assigned customers will not be able to send SMS using an archived card.
			</p>
			<div class="mt-4">
				<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="arch-reason">Reason *</label>
				<input
					id="arch-reason"
					type="text"
					bind:value={archiveReason}
					placeholder="Reason for archiving..."
					class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 dark:border-slate-700 dark:text-white"
				/>
			</div>
			<div class="mt-6 flex justify-end gap-3">
				<button
					type="button"
					onclick={() => { cardToArchive = null; }}
					class="rounded-lg border border-slate-300 px-4 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
				>
					Cancel
				</button>
				<button
					type="button"
					onclick={handleArchive}
					disabled={isArchiving}
					class="rounded-lg bg-rose-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-rose-500 disabled:opacity-50"
				>
					{isArchiving ? 'Archiving...' : 'Confirm Archive'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- History & Rollback Modal -->
{#if isHistoryModalOpen && historyCard}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-2xl rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<div class="flex items-center justify-between">
				<div>
					<h2 class="text-lg font-bold text-slate-900 dark:text-white">Version History: {historyCard.name}</h2>
					<p class="text-xs text-slate-500">Audit trail of past versions and dual-control changes.</p>
				</div>
				<button
					type="button"
					aria-label="Close modal"
					onclick={() => { isHistoryModalOpen = false; }}
					class="rounded p-1 text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			<div class="mt-4 max-h-80 overflow-y-auto divide-y divide-slate-100 dark:divide-slate-800">
				{#if isHistoryLoading}
					<Loading message="Loading version history..." />
				{:else if historyList.length === 0}
					<div class="py-8 text-center text-xs text-slate-400">No history records found.</div>
				{:else}
					{#each historyList as item}
						<div class="flex items-center justify-between py-3">
							<div>
								<div class="flex items-center gap-2">
									<span class="rounded bg-indigo-50 px-1.5 py-0.5 font-mono text-[11px] font-bold text-indigo-700 dark:bg-indigo-950 dark:text-indigo-300">
										v{item.version}
									</span>
									<span class="font-semibold text-slate-900 dark:text-white">{item.name}</span>
									<span class="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[10px] uppercase text-slate-600 dark:bg-slate-800 dark:text-slate-400">
										{item.status}
									</span>
								</div>
								<div class="mt-1 text-[11px] text-slate-500">
									Changed at {new Date(item.changed_at).toLocaleString()} &bull; Reason: {item.change_reason || '—'}
								</div>
							</div>
							{#if item.version < historyCard.version}
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
					<h3 class="text-xs font-bold text-amber-900 dark:text-amber-200">Rollback to Version {rollbackVersion}</h3>
					<p class="mt-1 text-[11px] text-amber-700 dark:text-amber-400">
						Rolling back will restore the rate card metadata to version {rollbackVersion}.
					</p>
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
							onclick={handleRollback}
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
