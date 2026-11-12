<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { Country, Operator, OperatorHistory, CreateOperatorRequest, UpdateOperatorRequest } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import VersionHistory from '$lib/components/VersionHistory.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let operators = $state<Operator[]>([]);
	let countries = $state<Country[]>([]);
	let totalCount = $state(0);
	let page = $state(1);
	let pageSize = 15;
	let search = $state('');
	let countryFilter = $state('');
	let mccFilter = $state('');
	let mncFilter = $state('');
	let technologyFilter = $state('');
	let statusFilter = $state('');
	let verificationFilter = $state('');
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	const canCreate = $derived(auth.hasPermission('operator.operator.create'));
	const canEdit = $derived(auth.hasPermission('operator.operator.update'));

	// Create/Edit modal state
	let isEditModalOpen = $state(false);
	let editingOperator = $state<Operator | null>(null);
	let formName = $state('');
	let formBrandName = $state('');
	let formParentCompany = $state('');
	let formCountryId = $state('');
	let formMcc = $state('');
	let formMnc = $state('');
	let formTech = $state<string[]>([]);
	let formDataSource = $state('');
	let formReason = $state('');
	let isSaving = $state(false);
	let formError = $state<string | null>(null);

	// History modal state
	let isHistoryModalOpen = $state(false);
	let historyTarget = $state<Operator | null>(null);
	let historyEntries = $state<OperatorHistory[]>([]);
	let isLoadingHistory = $state(false);

	const countryMap = $derived(
		new Map(countries.map((c) => [c.id, c]))
	);

	async function loadCountries() {
		try {
			const res = await apiClient.listCountries({ pageSize: 500 });
			countries = res.data.items || [];
		} catch {
			// ignore fallback
		}
	}

	async function loadOperators() {
		isLoading = true;
		errorMessage = null;

		try {
			const res = await apiClient.listOperators({
				search: search.trim() || undefined,
				country_id: countryFilter || undefined,
				mcc: mccFilter.trim() || undefined,
				mnc: mncFilter.trim() || undefined,
				technology: technologyFilter || undefined,
				status: statusFilter || undefined,
				verification_status: verificationFilter || undefined,
				page,
				pageSize
			});
			operators = res.data.items || [];
			totalCount = res.data.total_count;
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to load operators.';
			}
		} finally {
			isLoading = false;
		}
	}

	onMount(async () => {
		await loadCountries();
		await loadOperators();
	});

	function handleSearch(e: SubmitEvent) {
		e.preventDefault();
		page = 1;
		loadOperators();
	}

	function openCreateModal() {
		editingOperator = null;
		formName = '';
		formBrandName = '';
		formParentCompany = '';
		formCountryId = countries[0]?.id || '';
		formMcc = '';
		formMnc = '';
		formTech = ['4G', '5G'];
		formDataSource = 'manual';
		formReason = 'Manual creation';
		formError = null;
		isEditModalOpen = true;
	}

	function openEditModal(op: Operator) {
		editingOperator = op;
		formName = op.name;
		formBrandName = op.brand_name || '';
		formParentCompany = op.parent_company || '';
		formCountryId = op.country_id;
		formMcc = op.mcc;
		formMnc = op.mnc;
		formTech = [...(op.network_technology || [])];
		formDataSource = op.data_source;
		formReason = '';
		formError = null;
		isEditModalOpen = true;
	}

	function toggleTech(tech: string) {
		if (formTech.includes(tech)) {
			formTech = formTech.filter((t) => t !== tech);
		} else {
			formTech = [...formTech, tech];
		}
	}

	async function saveOperator(e: SubmitEvent) {
		e.preventDefault();
		if (!formReason.trim()) {
			formError = 'A reason for this change is required.';
			return;
		}
		isSaving = true;
		formError = null;

		try {
			if (editingOperator) {
				const req: UpdateOperatorRequest = {
					name: formName.trim(),
					brand_name: formBrandName.trim(),
					parent_company: formParentCompany.trim(),
					network_technology: formTech,
					data_source: formDataSource.trim(),
					reason: formReason.trim()
				};
				await apiClient.updateOperator(editingOperator.id, req);
				toast('Operator update submitted', 'success');
			} else {
				const req: CreateOperatorRequest = {
					name: formName.trim(),
					brand_name: formBrandName.trim(),
					parent_company: formParentCompany.trim(),
					country_id: formCountryId,
					mcc: formMcc.trim(),
					mnc: formMnc.trim(),
					network_technology: formTech,
					data_source: formDataSource.trim(),
					reason: formReason.trim()
				};
				await apiClient.createOperator(req);
				toast('Operator created successfully', 'success');
			}
			isEditModalOpen = false;
			await loadOperators();
		} catch (err) {
			if (err instanceof ApiClientError) {
				formError = err.message;
			} else {
				formError = 'Failed to save operator.';
			}
		} finally {
			isSaving = false;
		}
	}

	async function toggleStatus(op: Operator) {
		const newStatus = op.status === 'active' ? 'inactive' : 'active';
		const reason = prompt(`Reason for setting status to ${newStatus}:`);
		if (!reason) return;

		try {
			await apiClient.patchOperatorStatus(op.id, newStatus, reason);
			toast(`Operator status changed to ${newStatus}`, 'success');
			await loadOperators();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Status update failed', 'error');
		}
	}

	async function archiveOperator(op: Operator) {
		const reason = prompt(`Are you sure you want to archive ${op.name} (${op.mcc}-${op.mnc})? Enter reason:`);
		if (!reason) return;

		try {
			await apiClient.archiveOperator(op.id, reason);
			toast('Operator archived', 'success');
			await loadOperators();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Archive failed', 'error');
		}
	}

	async function openHistory(op: Operator) {
		historyTarget = op;
		isHistoryModalOpen = true;
		isLoadingHistory = true;
		try {
			const res = await apiClient.getOperatorHistory(op.id);
			historyEntries = res.data;
		} catch {
			toast('Failed to load version history', 'error');
		} finally {
			isLoadingHistory = false;
		}
	}

	async function handleExport(format: 'csv' | 'json') {
		try {
			const blob = await apiClient.exportOperators(format, {
				country_id: countryFilter || undefined,
				status: statusFilter || undefined,
				technology: technologyFilter || undefined
			});
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = `operators-export.${format}`;
			a.click();
			URL.revokeObjectURL(url);
		} catch {
			toast('Export failed', 'error');
		}
	}
</script>

<div class="space-y-6">
	<!-- Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Operators</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Mobile and fixed network operators with MCC/MNC pairs and technology profiles ({totalCount} total).
			</p>
		</div>

		<div class="flex items-center gap-2">
			<div class="flex rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800 p-0.5">
				<button
					type="button"
					onclick={() => handleExport('csv')}
					class="px-2.5 py-1 text-xs font-semibold text-slate-600 dark:text-slate-300 hover:text-indigo-600 dark:hover:text-indigo-400"
				>
					Export CSV
				</button>
				<button
					type="button"
					onclick={() => handleExport('json')}
					class="px-2.5 py-1 text-xs font-semibold text-slate-600 dark:text-slate-300 hover:text-indigo-600 dark:hover:text-indigo-400 border-l border-slate-200 dark:border-slate-700"
				>
					Export JSON
				</button>
			</div>

			{#if canCreate}
				<button
					type="button"
					onclick={openCreateModal}
					class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-600 focus:ring-offset-2 transition"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
					</svg>
					<span>Add Operator</span>
				</button>
			{/if}
		</div>
	</div>

	<!-- Filters & Search -->
	<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
		<form onsubmit={handleSearch} class="flex flex-col gap-3">
			<div class="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-5 gap-3">
				<div class="md:col-span-2">
					<input
						type="text"
						bind:value={search}
						placeholder="Search by name, brand, or parent company..."
						class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div>
					<select aria-label="Filter by country"
						bind:value={countryFilter}
						onchange={() => { page = 1; loadOperators(); }}
						class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						<option value="">All Countries</option>
						{#each countries as c}
							<option value={c.id}>{c.name} ({c.iso2})</option>
						{/each}
					</select>
				</div>

				<div class="flex gap-2">
					<input
						type="text"
						bind:value={mccFilter}
						placeholder="MCC"
						class="w-1/2 rounded-lg border border-slate-300 bg-white px-2 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
					<input
						type="text"
						bind:value={mncFilter}
						placeholder="MNC"
						class="w-1/2 rounded-lg border border-slate-300 bg-white px-2 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div class="flex gap-2">
					<select aria-label="Filter by technology"
						bind:value={technologyFilter}
						onchange={() => { page = 1; loadOperators(); }}
						class="w-1/2 rounded-lg border border-slate-300 bg-white px-2 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						<option value="">All Tech</option>
						<option value="2G">2G</option>
						<option value="3G">3G</option>
						<option value="4G">4G</option>
						<option value="5G">5G</option>
					</select>

					<button
						type="submit"
						class="w-1/2 rounded-lg bg-slate-100 px-3 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700 transition"
					>
						Search
					</button>
				</div>
			</div>
		</form>
	</div>

	<!-- Content -->
	{#if isLoading}
		<div class="flex justify-center py-16">
			<Loading message="Loading operators..." />
		</div>
	{:else if errorMessage}
		<ErrorState message={errorMessage} retry={loadOperators} />
	{:else if operators.length === 0}
		<Empty
			title="No operators found"
			description="No operator records matched your search parameters."
		/>
	{:else}
		<div class="overflow-x-auto rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-left text-xs">
				<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 dark:text-slate-400 uppercase font-semibold">
					<tr>
						<th class="px-4 py-3">Operator</th>
						<th class="px-4 py-3">Country</th>
						<th class="px-4 py-3">MCC / MNC</th>
						<th class="px-4 py-3">Technology</th>
						<th class="px-4 py-3">Data Source</th>
						<th class="px-4 py-3">Verification</th>
						<th class="px-4 py-3">Status</th>
						<th class="px-4 py-3 text-right">Actions</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-200 dark:divide-slate-800 text-slate-700 dark:text-slate-300">
					{#each operators as op}
						{@const country = countryMap.get(op.country_id)}
						<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40 transition">
							<td class="px-4 py-3">
								<div class="font-semibold text-slate-900 dark:text-white">{op.name}</div>
								{#if op.brand_name && op.brand_name !== op.name}
									<div class="text-[10px] text-slate-400">Brand: {op.brand_name}</div>
								{/if}
							</td>
							<td class="px-4 py-3">
								{#if country}
									<span class="inline-flex items-center gap-1.5 font-medium">
										<span class="rounded bg-slate-100 dark:bg-slate-800 px-1 py-0.5 text-[10px] font-mono text-slate-600 dark:text-slate-300">
											{country.iso2}
										</span>
										<span>{country.name}</span>
									</span>
								{:else}
									<span class="text-slate-400 font-mono text-[10px]">{op.country_id.slice(0, 8)}...</span>
								{/if}
							</td>
							<td class="px-4 py-3 font-mono font-bold text-indigo-600 dark:text-indigo-400">
								{op.mcc} - {op.mnc}
							</td>
							<td class="px-4 py-3">
								<div class="flex flex-wrap gap-1">
									{#each op.network_technology || [] as tech}
										<span class="rounded bg-indigo-50 dark:bg-indigo-950/50 px-1.5 py-0.5 text-[10px] font-bold text-indigo-700 dark:text-indigo-300 border border-indigo-200/50 dark:border-indigo-800/50">
											{tech}
										</span>
									{/each}
								</div>
							</td>
							<td class="px-4 py-3 text-slate-500 text-[11px]">
								{op.data_source}
							</td>
							<td class="px-4 py-3">
								{#if op.verification_status === 'verified'}
									<span class="inline-flex items-center rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-semibold text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300">
										Verified
									</span>
								{:else}
									<span class="inline-flex items-center rounded-full bg-amber-100 px-2 py-0.5 text-[10px] font-semibold text-amber-800 dark:bg-amber-950/60 dark:text-amber-300">
										Unverified
									</span>
								{/if}
							</td>
							<td class="px-4 py-3">
								{#if op.status === 'active'}
									<span class="inline-flex items-center rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-semibold text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300">
										Active
									</span>
								{:else if op.status === 'inactive'}
									<span class="inline-flex items-center rounded-full bg-slate-100 px-2 py-0.5 text-[10px] font-semibold text-slate-700 dark:bg-slate-800 dark:text-slate-300">
										Inactive
									</span>
								{:else}
									<span class="inline-flex items-center rounded-full bg-rose-100 px-2 py-0.5 text-[10px] font-semibold text-rose-800 dark:bg-rose-950/60 dark:text-rose-300">
										Archived
									</span>
								{/if}
							</td>
							<td class="px-4 py-3 text-right">
								<div class="flex items-center justify-end gap-1">
									<button
										type="button"
										onclick={() => openHistory(op)}
										class="rounded p-1 text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
										title="Version History (v{op.version})"
									>
										<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z" />
										</svg>
									</button>

									{#if canEdit && op.status !== 'archived'}
										<button
											type="button"
											onclick={() => openEditModal(op)}
											class="rounded p-1 text-slate-400 hover:text-indigo-600 dark:hover:text-indigo-400"
											title="Edit Operator"
										>
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z" />
											</svg>
										</button>

										<button
											type="button"
											onclick={() => toggleStatus(op)}
											class="rounded p-1 text-slate-400 hover:text-amber-600 dark:hover:text-amber-400"
											title={op.status === 'active' ? 'Deactivate' : 'Activate'}
										>
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636" />
											</svg>
										</button>

										<button
											type="button"
											onclick={() => archiveOperator(op)}
											class="rounded p-1 text-slate-400 hover:text-rose-600 dark:hover:text-rose-400"
											title="Archive Operator"
										>
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
											</svg>
										</button>
									{/if}
								</div>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>

			<!-- Pagination -->
			{#if totalCount > pageSize}
				<div class="flex items-center justify-between border-t border-slate-200 px-4 py-3 dark:border-slate-800">
					<div class="text-xs text-slate-500">
						Showing {(page - 1) * pageSize + 1} to {Math.min(page * pageSize, totalCount)} of {totalCount}
					</div>
					<div class="flex items-center gap-2">
						<button
							type="button"
							disabled={page === 1}
							onclick={() => { page--; loadOperators(); }}
							class="rounded-lg border border-slate-300 px-3 py-1 text-xs font-semibold text-slate-700 disabled:opacity-50 dark:border-slate-700 dark:text-slate-300"
						>
							Previous
						</button>
						<span class="text-xs font-semibold text-slate-700 dark:text-slate-300">
							Page {page} of {Math.ceil(totalCount / pageSize)}
						</span>
						<button
							type="button"
							disabled={page * pageSize >= totalCount}
							onclick={() => { page++; loadOperators(); }}
							class="rounded-lg border border-slate-300 px-3 py-1 text-xs font-semibold text-slate-700 disabled:opacity-50 dark:border-slate-700 dark:text-slate-300"
						>
							Next
						</button>
					</div>
				</div>
			{/if}
		</div>
	{/if}
</div>

<!-- Create / Edit Modal -->
{#if isEditModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm overflow-y-auto">
		<div class="w-full max-w-lg rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900 my-8">
			<div class="flex items-center justify-between border-b border-slate-200 pb-4 dark:border-slate-800">
				<h2 class="text-lg font-bold text-slate-900 dark:text-white">
					{editingOperator ? `Edit Operator: ${editingOperator.name}` : 'Add New Operator'}
				</h2>
				<button
					type="button"
					aria-label="Close dialog"
					onclick={() => isEditModalOpen = false}
					class="rounded p-1 text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			{#if formError}
				<div class="mt-4 rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300 border border-rose-200 dark:border-rose-800">
					{formError}
				</div>
			{/if}

			<form onsubmit={saveOperator} class="mt-4 space-y-4 text-xs">
				<div>
					<label for="operator-field-1" class="block font-medium text-slate-700 dark:text-slate-300">Operator Name *</label>
					<input
						id="operator-field-1"
						type="text"
						bind:value={formName}
						required
						class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div class="grid grid-cols-2 gap-4">
					<div>
						<label for="operator-field-2" class="block font-medium text-slate-700 dark:text-slate-300">Brand Name</label>
						<input
							id="operator-field-2"
							type="text"
							bind:value={formBrandName}
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>

					<div>
						<label for="operator-field-3" class="block font-medium text-slate-700 dark:text-slate-300">Parent Company</label>
						<input
							id="operator-field-3"
							type="text"
							bind:value={formParentCompany}
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
				</div>

				{#if !editingOperator}
					<div>
						<label for="operator-field-4" class="block font-medium text-slate-700 dark:text-slate-300">Country *</label>
						<select
							id="operator-field-4"
							bind:value={formCountryId}
							required
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						>
							{#each countries as c}
								<option value={c.id}>{c.name} ({c.iso2})</option>
							{/each}
						</select>
					</div>

					<div class="grid grid-cols-2 gap-4">
						<div>
							<label for="operator-field-5" class="block font-medium text-slate-700 dark:text-slate-300">MCC * (3 digits)</label>
							<input
								id="operator-field-5"
								type="text"
								maxlength="3"
								bind:value={formMcc}
								required
								placeholder="e.g. 310"
								class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							/>
						</div>

						<div>
							<label for="operator-field-6" class="block font-medium text-slate-700 dark:text-slate-300">MNC * (2-3 digits)</label>
							<input
								id="operator-field-6"
								type="text"
								maxlength="3"
								bind:value={formMnc}
								required
								placeholder="e.g. 410"
								class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							/>
						</div>
					</div>
				{/if}

				<div>
					<label for="operator-field-7" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Network Technologies</label>
					<div class="flex items-center gap-3">
						{#each ['2G', '3G', '4G', '5G'] as t}
							<label class="inline-flex items-center gap-1.5 cursor-pointer">
								<input
									id="operator-field-7"
									type="checkbox"
									checked={formTech.includes(t)}
									onchange={() => toggleTech(t)}
									class="rounded border-slate-300 text-indigo-600 focus:ring-indigo-500"
								/>
								<span class="font-medium text-slate-700 dark:text-slate-300">{t}</span>
							</label>
						{/each}
					</div>
				</div>

				<div>
					<label for="operator-field-8" class="block font-medium text-slate-700 dark:text-slate-300">Data Source</label>
					<input
						id="operator-field-8"
						type="text"
						bind:value={formDataSource}
						class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div>
					<label for="operator-field-9" class="block font-medium text-slate-700 dark:text-slate-300">Audit Change Reason *</label>
					<input
						id="operator-field-9"
						type="text"
						bind:value={formReason}
						required
						placeholder="Mandatory reason for audit trail..."
						class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div class="flex items-center justify-end gap-3 pt-4 border-t border-slate-200 dark:border-slate-800">
					<button
						type="button"
						onclick={() => isEditModalOpen = false}
						class="rounded-lg border border-slate-300 px-4 py-2 font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={isSaving}
						class="rounded-lg bg-indigo-600 px-4 py-2 font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
					>
						{isSaving ? 'Saving...' : editingOperator ? 'Save Changes' : 'Create Operator'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}

<!-- Version History Modal -->
{#if isHistoryModalOpen && historyTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm overflow-y-auto">
		<div class="w-full max-w-2xl rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900 my-8">
			<div class="flex items-center justify-between border-b border-slate-200 pb-4 dark:border-slate-800">
				<div>
					<h2 class="text-lg font-bold text-slate-900 dark:text-white">
						Version History: {historyTarget.name} ({historyTarget.mcc}-{historyTarget.mnc})
					</h2>
					<p class="text-xs text-slate-500">Current version: v{historyTarget.version}</p>
				</div>
				<button
					type="button"
					aria-label="Close dialog"
					onclick={() => isHistoryModalOpen = false}
					class="rounded p-1 text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			<div class="mt-4">
				{#if isLoadingHistory}
					<div class="flex justify-center py-8">
						<Loading message="Loading version history..." />
					</div>
				{:else}
					<VersionHistory
						entityType="operator"
						entityId={historyTarget.id}
						history={historyEntries}
						onRollback={() => {
							isHistoryModalOpen = false;
							loadOperators();
						}}
					/>
				{/if}
			</div>
		</div>
	</div>
{/if}
