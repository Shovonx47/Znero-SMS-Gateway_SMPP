<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { Country, Operator, NumberPrefix, PrefixHistory, CreatePrefixRequest, UpdatePrefixRequest } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import VersionHistory from '$lib/components/VersionHistory.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let prefixes = $state<NumberPrefix[]>([]);
	let countries = $state<Country[]>([]);
	let operators = $state<Operator[]>([]);
	let totalCount = $state(0);
	let page = $state(1);
	let pageSize = 15;
	let search = $state('');
	let countryFilter = $state('');
	let operatorFilter = $state('');
	let typeFilter = $state('');
	let statusFilter = $state('');
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	const canCreate = $derived(auth.hasPermission('number.prefix.create'));
	const canEdit = $derived(auth.hasPermission('number.prefix.update'));

	// Create/Edit modal state
	let isEditModalOpen = $state(false);
	let editingPrefix = $state<NumberPrefix | null>(null);
	let formPrefix = $state('');
	let formCountryId = $state('');
	let formOperatorId = $state('');
	let formNumberType = $state<'mobile' | 'fixed' | 'other'>('mobile');
	let formPriority = $state(100);
	let formDataSource = $state('');
	let formReason = $state('');
	let isSaving = $state(false);
	let formError = $state<string | null>(null);

	// History modal state
	let isHistoryModalOpen = $state(false);
	let historyTarget = $state<NumberPrefix | null>(null);
	let historyEntries = $state<PrefixHistory[]>([]);
	let isLoadingHistory = $state(false);

	const countryMap = $derived(new Map(countries.map((c) => [c.id, c])));
	const operatorMap = $derived(new Map(operators.map((o) => [o.id, o])));

	// Filtered operators for the selected country in create/edit modal
	const modalOperators = $derived(
		formCountryId ? operators.filter((o) => o.country_id === formCountryId) : operators
	);

	async function loadReferenceData() {
		try {
			const [cRes, oRes] = await Promise.all([
				apiClient.listCountries({ pageSize: 500 }),
				apiClient.listOperators({ pageSize: 500 })
			]);
			countries = cRes.data.items || [];
			operators = oRes.data.items || [];
		} catch {
			// ignore fallback
		}
	}

	async function loadPrefixes() {
		isLoading = true;
		errorMessage = null;

		try {
			const res = await apiClient.listPrefixes({
				prefix: search.trim() || undefined,
				country_id: countryFilter || undefined,
				operator_id: operatorFilter || undefined,
				number_type: typeFilter || undefined,
				status: statusFilter || undefined,
				page,
				pageSize
			});
			prefixes = res.data.items || [];
			totalCount = res.data.total_count;
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to load number prefixes.';
			}
		} finally {
			isLoading = false;
		}
	}

	onMount(async () => {
		await loadReferenceData();
		await loadPrefixes();
	});

	function handleSearch(e: SubmitEvent) {
		e.preventDefault();
		page = 1;
		loadPrefixes();
	}

	function openCreateModal() {
		editingPrefix = null;
		formPrefix = '';
		formCountryId = countries[0]?.id || '';
		formOperatorId = '';
		formNumberType = 'mobile';
		formPriority = 100;
		formDataSource = 'manual';
		formReason = 'Manual creation';
		formError = null;
		isEditModalOpen = true;
	}

	function openEditModal(p: NumberPrefix) {
		editingPrefix = p;
		formPrefix = p.prefix;
		formCountryId = p.country_id;
		formOperatorId = p.operator_id || '';
		formNumberType = p.number_type;
		formPriority = p.priority;
		formDataSource = p.data_source;
		formReason = '';
		formError = null;
		isEditModalOpen = true;
	}

	async function savePrefix(e: SubmitEvent) {
		e.preventDefault();
		if (!formReason.trim()) {
			formError = 'A reason for this change is required.';
			return;
		}
		isSaving = true;
		formError = null;

		try {
			if (editingPrefix) {
				const req: UpdatePrefixRequest = {
					prefix: formPrefix.trim(),
					operator_id: formOperatorId || undefined,
					number_type: formNumberType,
					priority: Number(formPriority),
					data_source: formDataSource.trim(),
					reason: formReason.trim()
				};
				await apiClient.updatePrefix(editingPrefix.id, req);
				toast('Prefix update submitted', 'success');
			} else {
				const req: CreatePrefixRequest = {
					prefix: formPrefix.trim(),
					country_id: formCountryId,
					operator_id: formOperatorId || undefined,
					number_type: formNumberType,
					priority: Number(formPriority),
					data_source: formDataSource.trim(),
					reason: formReason.trim()
				};
				await apiClient.createPrefix(req);
				toast('Prefix created successfully', 'success');
			}
			isEditModalOpen = false;
			await loadPrefixes();
		} catch (err) {
			if (err instanceof ApiClientError) {
				formError = err.message;
			} else {
				formError = 'Failed to save prefix.';
			}
		} finally {
			isSaving = false;
		}
	}

	async function toggleStatus(p: NumberPrefix) {
		const newStatus = p.status === 'active' ? 'inactive' : 'active';
		const reason = prompt(`Reason for setting status to ${newStatus}:`);
		if (!reason) return;

		try {
			await apiClient.patchPrefixStatus(p.id, newStatus, reason);
			toast(`Prefix status changed to ${newStatus}`, 'success');
			await loadPrefixes();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Status update failed', 'error');
		}
	}

	async function archivePrefix(p: NumberPrefix) {
		const reason = prompt(`Are you sure you want to archive prefix ${p.prefix}? Enter reason:`);
		if (!reason) return;

		try {
			await apiClient.archivePrefix(p.id, reason);
			toast('Prefix archived', 'success');
			await loadPrefixes();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Archive failed', 'error');
		}
	}

	async function openHistory(p: NumberPrefix) {
		historyTarget = p;
		isHistoryModalOpen = true;
		isLoadingHistory = true;
		try {
			const res = await apiClient.getPrefixHistory(p.id);
			historyEntries = res.data;
		} catch {
			toast('Failed to load version history', 'error');
		} finally {
			isLoadingHistory = false;
		}
	}

	async function handleExport(format: 'csv' | 'json') {
		try {
			const blob = await apiClient.exportPrefixes(format, {
				country_id: countryFilter || undefined,
				status: statusFilter || undefined,
				number_type: typeFilter || undefined
			});
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = `prefixes-export.${format}`;
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
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Number Prefixes</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Routing prefixes mapped to country and operator with type classification and priority ({totalCount} total).
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
					<span>Add Prefix</span>
				</button>
			{/if}
		</div>
	</div>

	<!-- Filters & Search -->
	<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
		<form onsubmit={handleSearch} class="flex flex-col gap-3">
			<div class="grid grid-cols-1 sm:grid-cols-2 md:grid-cols-5 gap-3">
				<div>
					<input
						type="text"
						bind:value={search}
						placeholder="Search prefix (e.g. 88017)..."
						class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div>
					<select aria-label="Filter by country"
						bind:value={countryFilter}
						onchange={() => { page = 1; loadPrefixes(); }}
						class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						<option value="">All Countries</option>
						{#each countries as c}
							<option value={c.id}>{c.name} ({c.iso2})</option>
						{/each}
					</select>
				</div>

				<div>
					<select aria-label="Filter by type"
						bind:value={typeFilter}
						onchange={() => { page = 1; loadPrefixes(); }}
						class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						<option value="">All Types</option>
						<option value="mobile">Mobile</option>
						<option value="fixed">Fixed</option>
						<option value="other">Other</option>
					</select>
				</div>

				<div>
					<select aria-label="Filter by status"
						bind:value={statusFilter}
						onchange={() => { page = 1; loadPrefixes(); }}
						class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						<option value="">All Statuses</option>
						<option value="active">Active</option>
						<option value="inactive">Inactive</option>
						<option value="archived">Archived</option>
					</select>
				</div>

				<div>
					<button
						type="submit"
						class="w-full rounded-lg bg-slate-100 px-3 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700 transition"
					>
						Filter
					</button>
				</div>
			</div>
		</form>
	</div>

	<!-- Content -->
	{#if isLoading}
		<div class="flex justify-center py-16">
			<Loading message="Loading prefixes..." />
		</div>
	{:else if errorMessage}
		<ErrorState message={errorMessage} retry={loadPrefixes} />
	{:else if prefixes.length === 0}
		<Empty
			title="No number prefixes found"
			description="No prefix records matched your search query."
		/>
	{:else}
		<div class="overflow-x-auto rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-left text-xs">
				<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 dark:text-slate-400 uppercase font-semibold">
					<tr>
						<th class="px-4 py-3">Prefix</th>
						<th class="px-4 py-3">Country</th>
						<th class="px-4 py-3">Operator</th>
						<th class="px-4 py-3">Type</th>
						<th class="px-4 py-3">Priority</th>
						<th class="px-4 py-3">Data Source</th>
						<th class="px-4 py-3">Status</th>
						<th class="px-4 py-3 text-right">Actions</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-200 dark:divide-slate-800 text-slate-700 dark:text-slate-300">
					{#each prefixes as p}
						{@const country = countryMap.get(p.country_id)}
						{@const op = p.operator_id ? operatorMap.get(p.operator_id) : null}
						<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40 transition">
							<td class="px-4 py-3 font-mono font-bold text-indigo-600 dark:text-indigo-400">
								+{p.prefix}
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
									<span class="text-slate-400 font-mono text-[10px]">{p.country_id.slice(0, 8)}...</span>
								{/if}
							</td>
							<td class="px-4 py-3">
								{#if op}
									<div class="font-medium text-slate-900 dark:text-white">{op.name}</div>
									<div class="text-[10px] text-slate-400 font-mono">{op.mcc}-{op.mnc}</div>
								{:else}
									<span class="text-slate-400 italic">None (National/Default)</span>
								{/if}
							</td>
							<td class="px-4 py-3">
								<span class="rounded bg-slate-100 dark:bg-slate-800 px-2 py-0.5 text-[10px] font-semibold text-slate-700 dark:text-slate-300 uppercase">
									{p.number_type}
								</span>
							</td>
							<td class="px-4 py-3 font-mono">
								{p.priority}
							</td>
							<td class="px-4 py-3 text-slate-500 text-[11px]">
								{p.data_source}
							</td>
							<td class="px-4 py-3">
								{#if p.status === 'active'}
									<span class="inline-flex items-center rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-semibold text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300">
										Active
									</span>
								{:else if p.status === 'inactive'}
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
										onclick={() => openHistory(p)}
										class="rounded p-1 text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
										title="Version History (v{p.version})"
									>
										<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z" />
										</svg>
									</button>

									{#if canEdit && p.status !== 'archived'}
										<button
											type="button"
											onclick={() => openEditModal(p)}
											class="rounded p-1 text-slate-400 hover:text-indigo-600 dark:hover:text-indigo-400"
											title="Edit Prefix"
										>
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z" />
											</svg>
										</button>

										<button
											type="button"
											onclick={() => toggleStatus(p)}
											class="rounded p-1 text-slate-400 hover:text-amber-600 dark:hover:text-amber-400"
											title={p.status === 'active' ? 'Deactivate' : 'Activate'}
										>
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636" />
											</svg>
										</button>

										<button
											type="button"
											onclick={() => archivePrefix(p)}
											class="rounded p-1 text-slate-400 hover:text-rose-600 dark:hover:text-rose-400"
											title="Archive Prefix"
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
							onclick={() => { page--; loadPrefixes(); }}
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
							onclick={() => { page++; loadPrefixes(); }}
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
					{editingPrefix ? `Edit Prefix: +${editingPrefix.prefix}` : 'Add New Prefix'}
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

			<form onsubmit={savePrefix} class="mt-4 space-y-4 text-xs">
				<div>
					<label for="prefix-field-1" class="block font-medium text-slate-700 dark:text-slate-300">Prefix String * (e.g. 88017, 1202)</label>
					<input
						id="prefix-field-1"
						type="text"
						bind:value={formPrefix}
						required
						placeholder="Digits without leading +"
						class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				{#if !editingPrefix}
					<div>
						<label for="prefix-field-2" class="block font-medium text-slate-700 dark:text-slate-300">Country *</label>
						<select
							id="prefix-field-2"
							bind:value={formCountryId}
							required
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						>
							{#each countries as c}
								<option value={c.id}>{c.name} ({c.iso2})</option>
							{/each}
						</select>
					</div>
				{/if}

				<div>
					<label for="prefix-field-3" class="block font-medium text-slate-700 dark:text-slate-300">Operator (Optional)</label>
					<select
						id="prefix-field-3"
						bind:value={formOperatorId}
						class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						<option value="">None (Country-wide / Default)</option>
						{#each modalOperators as op}
							<option value={op.id}>{op.name} ({op.mcc}-{op.mnc})</option>
						{/each}
					</select>
				</div>

				<div class="grid grid-cols-2 gap-4">
					<div>
						<label for="prefix-field-4" class="block font-medium text-slate-700 dark:text-slate-300">Number Type</label>
						<select
							id="prefix-field-4"
							bind:value={formNumberType}
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						>
							<option value="mobile">Mobile</option>
							<option value="fixed">Fixed</option>
							<option value="other">Other</option>
						</select>
					</div>

					<div>
						<label for="prefix-field-5" class="block font-medium text-slate-700 dark:text-slate-300">Priority (Higher wins)</label>
						<input
							id="prefix-field-5"
							type="number"
							bind:value={formPriority}
							min="1"
							max="1000"
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
				</div>

				<div>
					<label for="prefix-field-6" class="block font-medium text-slate-700 dark:text-slate-300">Data Source</label>
					<input
						id="prefix-field-6"
						type="text"
						bind:value={formDataSource}
						class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div>
					<label for="prefix-field-7" class="block font-medium text-slate-700 dark:text-slate-300">Audit Change Reason *</label>
					<input
						id="prefix-field-7"
						type="text"
						bind:value={formReason}
						required
						placeholder="Mandatory reason for audit..."
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
						{isSaving ? 'Saving...' : editingPrefix ? 'Save Changes' : 'Create Prefix'}
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
						Version History: Prefix +{historyTarget.prefix}
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
						entityType="prefix"
						entityId={historyTarget.id}
						history={historyEntries}
						onRollback={() => {
							isHistoryModalOpen = false;
							loadPrefixes();
						}}
					/>
				{/if}
			</div>
		</div>
	</div>
{/if}
