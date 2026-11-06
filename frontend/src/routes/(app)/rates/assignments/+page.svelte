<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { CustomerRateAssignment, ResellerCostAssignment, RateCard, Organization } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let activeTab = $state<'customer' | 'reseller'>('customer');

	let customerAssignments = $state<CustomerRateAssignment[]>([]);
	let resellerAssignments = $state<ResellerCostAssignment[]>([]);
	let sellCards = $state<RateCard[]>([]);
	let customerOrgs = $state<Organization[]>([]);
	let resellerOrgs = $state<Organization[]>([]);

	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	// Create modal
	let isCreateModalOpen = $state(false);
	let isSubmitting = $state(false);
	let formError = $state<string | null>(null);
	let formTargetOrgId = $state('');
	let formCardId = $state('');
	let formEffectiveFrom = $state(new Date().toISOString().slice(0, 16));
	let formEffectiveTo = $state('');
	let formReason = $state('');

	// Deactivate modal
	let itemToDeactivate = $state<{ id: string; name: string; isCustomer: boolean } | null>(null);
	let deactivateReason = $state('');
	let isDeactivating = $state(false);

	const isPlatform = $derived(auth.tenant?.actor_org_type === 'platform');
	const canAssign = $derived(auth.hasPermission('rate.assignment.create'));

	async function loadData() {
		isLoading = true;
		errorMessage = null;
		try {
			const [custAssignRes, cardsRes, custOrgRes] = await Promise.all([
				apiClient.listCustomerAssignments(),
				apiClient.listRateCards({ kind: 'customer_sell', status: 'active', pageSize: 100 }),
				apiClient.listOrganizations({ type: 'customer', pageSize: 200 })
			]);
			customerAssignments = custAssignRes.data || [];
			sellCards = cardsRes.data.items || [];
			customerOrgs = custOrgRes.data.items || [];

			if (isPlatform) {
				const [resAssignRes, resOrgRes] = await Promise.all([
					apiClient.listResellerCostAssignments(),
					apiClient.listOrganizations({ type: 'reseller', pageSize: 200 })
				]);
				resellerAssignments = resAssignRes.data || [];
				resellerOrgs = resOrgRes.data.items || [];
			}
		} catch (err) {
			errorMessage = err instanceof ApiClientError ? err.message : 'Failed to load rate card assignments.';
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadData();
	});

	function openCreateModal(tab: 'customer' | 'reseller') {
		activeTab = tab;
		formTargetOrgId = tab === 'customer' ? customerOrgs[0]?.id || '' : resellerOrgs[0]?.id || '';
		formCardId = sellCards[0]?.id || '';
		formEffectiveFrom = new Date().toISOString().slice(0, 16);
		formEffectiveTo = '';
		formReason = '';
		formError = null;
		isCreateModalOpen = true;
	}

	async function handleCreateAssignment() {
		if (!formTargetOrgId) {
			formError = 'Target organization is required';
			return;
		}
		if (!formCardId) {
			formError = 'Rate card is required';
			return;
		}
		isSubmitting = true;
		formError = null;
		try {
			const payload = {
				target_org_id: formTargetOrgId,
				card_id: formCardId,
				effective_from: new Date(formEffectiveFrom).toISOString(),
				effective_to: formEffectiveTo ? new Date(formEffectiveTo).toISOString() : undefined,
				reason: formReason.trim() || `Assign rate card to ${activeTab}`
			};

			let res;
			if (activeTab === 'customer') {
				res = await apiClient.createCustomerAssignment(payload);
			} else {
				res = await apiClient.createResellerCostAssignment(payload);
			}

			if (res.data.pending_approval) {
				toast.info('Assignment change request submitted for dual-control approval');
			} else {
				toast.success(`Rate card assigned to ${activeTab} successfully`);
			}
			isCreateModalOpen = false;
			await loadData();
		} catch (err) {
			formError = err instanceof ApiClientError ? err.message : 'Failed to create assignment.';
		} finally {
			isSubmitting = false;
		}
	}

	function openDeactivateModal(id: string, name: string, isCustomer: boolean) {
		itemToDeactivate = { id, name, isCustomer };
		deactivateReason = '';
	}

	async function handleDeactivate() {
		if (!itemToDeactivate) return;
		isDeactivating = true;
		try {
			const res = await apiClient.deactivateAssignment(
				itemToDeactivate.id,
				itemToDeactivate.isCustomer,
				deactivateReason.trim() || 'Deactivate rate card assignment'
			);
			if (res.data.pending_change) {
				toast.info('Deactivation submitted for dual-control approval');
			} else {
				toast.success('Assignment deactivated');
			}
			itemToDeactivate = null;
			await loadData();
		} catch (err) {
			toast.error(err instanceof ApiClientError ? err.message : 'Failed to deactivate assignment');
		} finally {
			isDeactivating = false;
		}
	}
</script>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Rate Card Assignments</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Bind customer sell cards and reseller wholesale cost cards to tenant organizations.
			</p>
		</div>

		{#if canAssign}
			<button
				type="button"
				onclick={() => openCreateModal(activeTab)}
				class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2.5 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
				</svg>
				Assign {activeTab === 'customer' ? 'Customer' : 'Reseller Cost'} Card
			</button>
		{/if}
	</div>

	<!-- Tabs -->
	<div class="border-b border-slate-200 dark:border-slate-800">
		<nav class="-mb-px flex space-x-6 text-xs font-semibold">
			<button
				type="button"
				onclick={() => { activeTab = 'customer'; }}
				class="pb-3 border-b-2 transition {activeTab === 'customer'
					? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400'
					: 'border-transparent text-slate-500 hover:border-slate-300 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-300'}"
			>
				Customer Rate Assignments ({customerAssignments.length})
			</button>

			{#if isPlatform}
				<button
					type="button"
					onclick={() => { activeTab = 'reseller'; }}
					class="pb-3 border-b-2 transition {activeTab === 'reseller'
						? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400'
						: 'border-transparent text-slate-500 hover:border-slate-300 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-300'}"
				>
					Reseller Cost Cards ({resellerAssignments.length})
				</button>
			{/if}
		</nav>
	</div>

	{#if errorMessage}
		<ErrorState message={errorMessage} retry={loadData} />
	{:else if isLoading}
		<Loading message="Loading assignments..." />
	{:else}
		<!-- Tab 1: Customer Rate Assignments -->
		{#if activeTab === 'customer'}
			{#if customerAssignments.length === 0}
				<Empty
					title="No customer rate assignments"
					description="Assign an active sell card to a customer organization so they can send traffic."
				/>
			{:else}
				<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
					<div class="overflow-x-auto">
						<table class="w-full text-left text-xs">
							<thead class="border-b border-slate-200 bg-slate-50 font-semibold text-slate-700 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-300">
								<tr>
									<th class="px-4 py-3">Customer Organization</th>
									<th class="px-4 py-3">Assigned Sell Card</th>
									<th class="px-4 py-3">Currency</th>
									<th class="px-4 py-3">Effective Period</th>
									<th class="px-4 py-3">Status</th>
									<th class="px-4 py-3">Version</th>
									<th class="px-4 py-3 text-right">Actions</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
								{#each customerAssignments as a}
									<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/30">
										<td class="px-4 py-3 font-semibold text-slate-900 dark:text-white">
											{a.customer_org_name || a.customer_org_id}
										</td>
										<td class="px-4 py-3">
											<a
												href="/rates/sell-cards/{a.card_id}"
												class="font-medium text-indigo-600 hover:text-indigo-800 dark:text-indigo-400"
											>
												{a.card_name || a.card_id}
											</a>
										</td>
										<td class="px-4 py-3 font-mono font-medium">
											<span class="rounded bg-slate-100 px-2 py-0.5 text-[11px] dark:bg-slate-800">
												{a.currency_code || 'USD'}
											</span>
										</td>
										<td class="px-4 py-3 text-[11px] text-slate-500">
											<div>From: {new Date(a.effective_from).toLocaleDateString()}</div>
											{#if a.effective_to}
												<div>To: {new Date(a.effective_to).toLocaleDateString()}</div>
											{:else}
												<div class="text-emerald-600 dark:text-emerald-400">Open-ended</div>
											{/if}
										</td>
										<td class="px-4 py-3">
											{#if a.status === 'active'}
												<span class="inline-flex items-center rounded-full bg-emerald-50 px-2 py-0.5 text-[10px] font-semibold text-emerald-700 dark:bg-emerald-950/60 dark:text-emerald-300">
													Active
												</span>
											{:else}
												<span class="inline-flex items-center rounded-full bg-slate-100 px-2 py-0.5 text-[10px] font-semibold text-slate-600 dark:bg-slate-800 dark:text-slate-400">
													{a.status}
												</span>
											{/if}
										</td>
										<td class="px-4 py-3 font-mono text-[11px] text-slate-500">
											v{a.version}
										</td>
										<td class="px-4 py-3 text-right">
											{#if canAssign && a.status === 'active'}
												<button
													type="button"
													onclick={() => openDeactivateModal(a.id, a.customer_org_name || a.customer_org_id, true)}
													class="rounded px-2 py-1 text-[11px] font-semibold text-rose-600 hover:bg-rose-50 dark:text-rose-400 dark:hover:bg-rose-950/50"
												>
													Deactivate
												</button>
											{/if}
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				</div>
			{/if}
		{:else if activeTab === 'reseller'}
			<!-- Tab 2: Reseller Cost Card Assignments -->
			{#if resellerAssignments.length === 0}
				<Empty
					title="No reseller cost assignments"
					description="Assign wholesale cost cards to define profit floors and margin boundaries for reseller partners."
				/>
			{:else}
				<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
					<div class="overflow-x-auto">
						<table class="w-full text-left text-xs">
							<thead class="border-b border-slate-200 bg-slate-50 font-semibold text-slate-700 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-300">
								<tr>
									<th class="px-4 py-3">Reseller Organization</th>
									<th class="px-4 py-3">Assigned Cost Card</th>
									<th class="px-4 py-3">Currency</th>
									<th class="px-4 py-3">Effective Period</th>
									<th class="px-4 py-3">Status</th>
									<th class="px-4 py-3">Version</th>
									<th class="px-4 py-3 text-right">Actions</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
								{#each resellerAssignments as r}
									<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/30">
										<td class="px-4 py-3 font-semibold text-slate-900 dark:text-white">
											{r.reseller_org_name || r.reseller_org_id}
										</td>
										<td class="px-4 py-3">
											<a
												href="/rates/sell-cards/{r.card_id}"
												class="font-medium text-indigo-600 hover:text-indigo-800 dark:text-indigo-400"
											>
												{r.card_name || r.card_id}
											</a>
										</td>
										<td class="px-4 py-3 font-mono font-medium">
											<span class="rounded bg-slate-100 px-2 py-0.5 text-[11px] dark:bg-slate-800">
												{r.currency_code || 'USD'}
											</span>
										</td>
										<td class="px-4 py-3 text-[11px] text-slate-500">
											<div>From: {new Date(r.effective_from).toLocaleDateString()}</div>
											{#if r.effective_to}
												<div>To: {new Date(r.effective_to).toLocaleDateString()}</div>
											{:else}
												<div class="text-emerald-600 dark:text-emerald-400">Open-ended</div>
											{/if}
										</td>
										<td class="px-4 py-3">
											{#if r.status === 'active'}
												<span class="inline-flex items-center rounded-full bg-emerald-50 px-2 py-0.5 text-[10px] font-semibold text-emerald-700 dark:bg-emerald-950/60 dark:text-emerald-300">
													Active
												</span>
											{:else}
												<span class="inline-flex items-center rounded-full bg-slate-100 px-2 py-0.5 text-[10px] font-semibold text-slate-600 dark:bg-slate-800 dark:text-slate-400">
													{r.status}
												</span>
											{/if}
										</td>
										<td class="px-4 py-3 font-mono text-[11px] text-slate-500">
											v{r.version}
										</td>
										<td class="px-4 py-3 text-right">
											{#if canAssign && r.status === 'active'}
												<button
													type="button"
													onclick={() => openDeactivateModal(r.id, r.reseller_org_name || r.reseller_org_id, false)}
													class="rounded px-2 py-1 text-[11px] font-semibold text-rose-600 hover:bg-rose-50 dark:text-rose-400 dark:hover:bg-rose-950/50"
												>
													Deactivate
												</button>
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
	{/if}
</div>

<!-- Create Assignment Modal -->
{#if isCreateModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-lg rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<div class="flex items-center justify-between">
				<h2 class="text-lg font-bold text-slate-900 dark:text-white">
					Assign {activeTab === 'customer' ? 'Customer Rate Card' : 'Reseller Cost Card'}
				</h2>
				<button
					type="button"
					onclick={() => { isCreateModalOpen = false; }}
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
					<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="as-org">
						{activeTab === 'customer' ? 'Customer Organization *' : 'Reseller Organization *'}
					</label>
					<select
						id="as-org"
						bind:value={formTargetOrgId}
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
					>
						{#if activeTab === 'customer'}
							{#each customerOrgs as org}
								<option value={org.id}>{org.name}</option>
							{/each}
						{:else}
							{#each resellerOrgs as org}
								<option value={org.id}>{org.name}</option>
							{/each}
						{/if}
					</select>
				</div>

				<div>
					<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="as-card">
						Rate Card to Assign *
					</label>
					<select
						id="as-card"
						bind:value={formCardId}
						class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
					>
						{#each sellCards as c}
							<option value={c.id}>{c.name} ({c.currency_code} - {c.rate_count ?? 0} rates)</option>
						{/each}
					</select>
				</div>

				<div class="grid grid-cols-2 gap-4">
					<div>
						<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="as-eff-from">
							Effective From *
						</label>
						<input
							id="as-eff-from"
							type="datetime-local"
							bind:value={formEffectiveFrom}
							class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
						/>
					</div>
					<div>
						<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="as-eff-to">
							Effective To (Optional)
						</label>
						<input
							id="as-eff-to"
							type="datetime-local"
							bind:value={formEffectiveTo}
							class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
						/>
					</div>
				</div>

				<div>
					<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="as-reason">Audit Reason</label>
					<input
						id="as-reason"
						type="text"
						bind:value={formReason}
						placeholder="Audit reason for assignment..."
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
					onclick={handleCreateAssignment}
					disabled={isSubmitting}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50"
				>
					{isSubmitting ? 'Saving...' : 'Save Assignment'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Deactivate Modal -->
{#if itemToDeactivate}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-lg font-bold text-rose-600">Deactivate Assignment</h2>
			<p class="mt-2 text-xs text-slate-600 dark:text-slate-300">
				Deactivate rate card assignment for <strong>{itemToDeactivate.name}</strong>?
				The organization will no longer be rated with this rate card.
			</p>
			<div class="mt-4">
				<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="dea-reason">Reason *</label>
				<input
					id="dea-reason"
					type="text"
					bind:value={deactivateReason}
					placeholder="Reason for deactivation..."
					class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 dark:border-slate-700 dark:text-white"
				/>
			</div>
			<div class="mt-6 flex justify-end gap-3">
				<button
					type="button"
					onclick={() => { itemToDeactivate = null; }}
					class="rounded-lg border border-slate-300 px-4 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
				>
					Cancel
				</button>
				<button
					type="button"
					onclick={handleDeactivate}
					disabled={isDeactivating}
					class="rounded-lg bg-rose-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-rose-500 disabled:opacity-50"
				>
					{isDeactivating ? 'Deactivating...' : 'Confirm Deactivation'}
				</button>
			</div>
		</div>
	</div>
{/if}
