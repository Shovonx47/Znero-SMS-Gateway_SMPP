<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { RateCard, RateCardHistory, Provider } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let cards = $state<RateCard[]>([]);
	let providers = $state<Provider[]>([]);
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
	let formProviderId = $state('');
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

	const isPlatformOrg = $derived(auth.tenant?.actor_org_type === 'platform');
	const canCreate = $derived(isPlatformOrg && auth.hasPermission('rate.buycard.create'));
	const canArchive = $derived(isPlatformOrg && auth.hasPermission('rate.buycard.archive'));

	async function loadData() {
		if (!isPlatformOrg) {
			errorMessage = '403 Forbidden: Provider Buy Cards are accessible to platform users only.';
			isLoading = false;
			return;
		}
		isLoading = true;
		errorMessage = null;
		try {
			const [cardsRes, provRes] = await Promise.all([
				apiClient.listRateCards({
					kind: 'provider_buy',
					status: statusFilter || undefined,
					search: search.trim() || undefined,
					page,
					pageSize
				}),
				apiClient.listProviders({ pageSize: 300 })
			]);
			cards = cardsRes.data.items || [];
			totalCount = cardsRes.data.total || 0;
			providers = provRes.data.items || [];
		} catch (err) {
			errorMessage = err instanceof ApiClientError ? err.message : 'Failed to load buy rate cards.';
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadData();
	});

	function providerName(provId?: string): string {
		if (!provId) return '—';
		const p = providers.find((x) => x.id === provId);
		return p ? `${p.name} (${p.code})` : provId;
	}

	function openCreateModal() {
		formName = '';
		formProviderId = providers[0]?.id || '';
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
		if (!formProviderId) {
			formError = 'Provider is required for buy cards';
			return;
		}
		isSubmitting = true;
		formError = null;
		try {
			const res = await apiClient.createRateCard({
				name: formName.trim(),
				kind: 'provider_buy',
				provider_id: formProviderId,
				currency_code: formCurrency.toUpperCase(),
				channel: formChannel,
				status: formStatus,
				description: formDescription.trim(),
				reason: formReason.trim() || 'Create provider buy card'
			});
			if (res.data.pending_approval) {
				toast.info('Buy card change request submitted for dual-control approval');
			} else {
				toast.success('Provider buy card created successfully');
			}
			isCreateModalOpen = false;
			await loadData();
		} catch (err) {
			formError = err instanceof ApiClientError ? err.message : 'Failed to create buy card.';
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
			await apiClient.archiveRateCard(cardToArchive.id, archiveReason.trim() || 'Archive buy card');
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
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Provider Buy Cards</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Manage wholesale SMS buy rate sheets agreed with upstream suppliers.
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
				New Buy Card
			</button>
		{/if}
	</div>

	{#if errorMessage}
		<ErrorState message={errorMessage} retry={loadData} />
	{:else}
		<!-- Filter bar -->
		<div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900 sm:flex-row sm:items-center">
			<div class="relative flex-1">
				<input aria-label="Select search"
					type="search"
					bind:value={search}
					oninput={() => { page = 1; loadData(); }}
					placeholder="Search buy cards by name or description..."
					class="w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
				/>
			</div>
			<select aria-label="Filter by status"
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
			<Loading message="Loading provider buy cards..." />
		{:else if cards.length === 0}
			<Empty
				title="No provider buy cards found"
				description="Create a buy rate card to begin managing supplier rates."
			/>
		{:else}
			<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<table class="w-full text-left text-xs text-slate-600 dark:text-slate-300">
					<thead class="border-b border-slate-200 bg-slate-50 text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-400">
						<tr>
							<th class="px-4 py-3">Card Name</th>
							<th class="px-4 py-3">Provider</th>
							<th class="px-4 py-3">Currency</th>
							<th class="px-4 py-3">Channel</th>
							<th class="px-4 py-3">Status</th>
							<th class="px-4 py-3">Version</th>
							<th class="px-4 py-3 text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
						{#each cards as card}
							<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/40">
								<td class="px-4 py-3 font-semibold text-slate-900 dark:text-white">
									<a href="/rates/buy-cards/{card.id}" class="text-indigo-600 hover:underline dark:text-indigo-400">
										{card.name}
									</a>
									{#if card.description}
										<div class="text-[11px] font-normal text-slate-400">{card.description}</div>
									{/if}
								</td>
								<td class="px-4 py-3 text-slate-700 dark:text-slate-300">
									{providerName(card.provider_id)}
								</td>
								<td class="px-4 py-3 font-mono font-medium">
									{card.currency_code}
								</td>
								<td class="px-4 py-3 uppercase">
									{card.channel}
								</td>
								<td class="px-4 py-3">
									<span class="inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-semibold {card.status === 'active' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300' : 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300'}">
										{card.status}
									</span>
								</td>
								<td class="px-4 py-3 font-mono text-[11px]">
									v{card.version}
								</td>
								<td class="px-4 py-3 text-right">
									<div class="inline-flex items-center gap-2">
										<a
											href="/rates/buy-cards/{card.id}"
											class="rounded px-2 py-1 text-[11px] font-medium text-indigo-600 hover:bg-indigo-50 dark:text-indigo-400 dark:hover:bg-slate-800"
										>
											Rates
										</a>
										<button
											type="button"
											onclick={() => openHistory(card)}
											class="rounded px-2 py-1 text-[11px] font-medium text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
										>
											History
										</button>
										{#if canArchive && !card.archived}
											<button
												type="button"
												onclick={() => openArchiveModal(card)}
												class="rounded px-2 py-1 text-[11px] font-medium text-rose-600 hover:bg-rose-50 dark:text-rose-400 dark:hover:bg-slate-800"
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
		{/if}
	{/if}
</div>

<!-- Create Modal -->
{#if isCreateModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-lg rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-lg font-bold text-slate-900 dark:text-white">Create Provider Buy Card</h2>
			<p class="mt-1 text-xs text-slate-500">Add a new supplier buy rate card container.</p>

			{#if formError}
				<div class="mt-4 rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300">
					{formError}
				</div>
			{/if}

			<div class="mt-4 space-y-4 text-xs">
				<div>
					<label class="block font-semibold text-slate-700 dark:text-slate-300" for="card-name">Card Name *</label>
					<input
						id="card-name"
						type="text"
						bind:value={formName}
						placeholder="e.g. Alpha Telecom Global Buy Card"
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-slate-900 dark:border-slate-700 dark:text-white"
					/>
				</div>

				<div>
					<label class="block font-semibold text-slate-700 dark:text-slate-300" for="card-provider">Supplier Provider *</label>
					<select
						id="card-provider"
						bind:value={formProviderId}
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-slate-900 dark:border-slate-700 dark:text-white"
					>
						{#each providers as prov}
							<option value={prov.id}>{prov.name} ({prov.code})</option>
						{/each}
					</select>
				</div>

				<div class="grid grid-cols-2 gap-4">
					<div>
						<label class="block font-semibold text-slate-700 dark:text-slate-300" for="card-currency">Currency Code *</label>
						<input
							id="card-currency"
							type="text"
							bind:value={formCurrency}
							placeholder="USD"
							class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 font-mono uppercase text-slate-900 dark:border-slate-700 dark:text-white"
						/>
					</div>
					<div>
						<label class="block font-semibold text-slate-700 dark:text-slate-300" for="card-status">Initial Status</label>
						<select
							id="card-status"
							bind:value={formStatus}
							class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-slate-900 dark:border-slate-700 dark:text-white"
						>
							<option value="active">Active</option>
							<option value="draft">Draft</option>
							<option value="inactive">Inactive</option>
						</select>
					</div>
				</div>

				<div>
					<label class="block font-semibold text-slate-700 dark:text-slate-300" for="card-desc">Description</label>
					<textarea
						id="card-desc"
						bind:value={formDescription}
						rows="2"
						placeholder="Notes regarding agreed tiering or SLA..."
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-slate-900 dark:border-slate-700 dark:text-white"
					></textarea>
				</div>

				<div>
					<label class="block font-semibold text-slate-700 dark:text-slate-300" for="card-reason">Reason for Change (Audit Log)</label>
					<input
						id="card-reason"
						type="text"
						bind:value={formReason}
						placeholder="e.g. Q4 Carrier contract renewal"
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-slate-900 dark:border-slate-700 dark:text-white"
					/>
				</div>
			</div>

			<div class="mt-6 flex justify-end gap-3">
				<button
					type="button"
					onclick={() => { isCreateModalOpen = false; }}
					class="rounded-lg border border-slate-300 px-4 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
				>
					Cancel
				</button>
				<button
					type="button"
					onclick={handleCreate}
					disabled={isSubmitting}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50"
				>
					{isSubmitting ? 'Creating...' : 'Create Buy Card'}
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
				Are you sure you want to archive <strong>{cardToArchive.name}</strong>? All associated rates will no longer be considered active.
			</p>

			<div class="mt-4">
				<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="archive-reason">Reason *</label>
				<input
					id="archive-reason"
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
					<p class="text-xs text-slate-500">Immutable snapshot records of all past modifications.</p>
				</div>
				<button
					type="button"
					aria-label="Close modal"
					onclick={() => { isHistoryModalOpen = false; }}
					class="rounded p-1 text-slate-400 hover:text-slate-600"
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
					<div class="py-8 text-center text-xs text-slate-400">No version history records found.</div>
				{:else}
					{#each historyList as item}
						<div class="flex items-center justify-between py-3">
							<div>
								<div class="flex items-center gap-2">
									<span class="rounded bg-indigo-50 px-1.5 py-0.5 font-mono text-[11px] font-bold text-indigo-700 dark:bg-indigo-950 dark:text-indigo-300">
										v{item.version}
									</span>
									<span class="text-xs font-semibold text-slate-900 dark:text-white">{item.name}</span>
									<span class="text-[10px] text-slate-400">{item.status}</span>
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
					<p class="mt-1 text-[11px] text-amber-700 dark:text-amber-300">
						This will create version {historyCard.version + 1} restoring the snapshot from v{rollbackVersion}.
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
