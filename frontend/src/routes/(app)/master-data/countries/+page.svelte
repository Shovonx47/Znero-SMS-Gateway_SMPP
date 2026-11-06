<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { Country, CountryHistory, CreateCountryRequest, UpdateCountryRequest } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import VersionHistory from '$lib/components/VersionHistory.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let countries = $state<Country[]>([]);
	let totalCount = $state(0);
	let page = $state(1);
	let pageSize = 15;
	let search = $state('');
	let statusFilter = $state('');
	let continentFilter = $state('');
	let verificationFilter = $state('');
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	// Permissions
	const canCreate = $derived(auth.hasPermission('country.country.create'));
	const canEdit = $derived(auth.hasPermission('country.country.update'));
	const canApprove = $derived(auth.hasPermission('country.country.approve'));

	// Create/Edit modal state
	let isEditModalOpen = $state(false);
	let editingCountry = $state<Country | null>(null);
	let formName = $state('');
	let formIso2 = $state('');
	let formIso3 = $state('');
	let formNumericCode = $state('');
	let formCallingCode = $state('');
	let formContinent = $state('');
	let formRegion = $state('');
	let formCurrencyCode = $state('');
	let formTimezones = $state('');
	let formMinLength = $state(4);
	let formMaxLength = $state(15);
	let formSenderNotes = $state('');
	let formDndNotes = $state('');
	let formConsentNotes = $state('');
	let formDataSource = $state('');
	let formReason = $state('');
	let isSaving = $state(false);
	let formError = $state<string | null>(null);

	// History modal state
	let isHistoryModalOpen = $state(false);
	let historyTarget = $state<Country | null>(null);
	let historyEntries = $state<CountryHistory[]>([]);
	let isLoadingHistory = $state(false);

	async function loadCountries() {
		isLoading = true;
		errorMessage = null;

		try {
			const res = await apiClient.listCountries({
				search: search.trim() || undefined,
				status: statusFilter || undefined,
				continent: continentFilter || undefined,
				verification_status: verificationFilter || undefined,
				page,
				pageSize
			});
			countries = res.data.items;
			totalCount = res.data.total_count;
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to load countries.';
			}
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadCountries();
	});

	function handleSearch(e: SubmitEvent) {
		e.preventDefault();
		page = 1;
		loadCountries();
	}

	function openCreateModal() {
		editingCountry = null;
		formName = '';
		formIso2 = '';
		formIso3 = '';
		formNumericCode = '';
		formCallingCode = '';
		formContinent = '';
		formRegion = '';
		formCurrencyCode = '';
		formTimezones = '';
		formMinLength = 4;
		formMaxLength = 15;
		formSenderNotes = '';
		formDndNotes = '';
		formConsentNotes = '';
		formDataSource = 'manual';
		formReason = 'Manual creation';
		formError = null;
		isEditModalOpen = true;
	}

	function openEditModal(c: Country) {
		editingCountry = c;
		formName = c.name;
		formIso2 = c.iso2;
		formIso3 = c.iso3;
		formNumericCode = c.numeric_code;
		formCallingCode = c.calling_code;
		formContinent = c.continent;
		formRegion = c.region;
		formCurrencyCode = c.currency_code;
		formTimezones = c.timezones?.join(', ') || '';
		formMinLength = c.min_national_length;
		formMaxLength = c.max_national_length;
		formSenderNotes = c.sender_id_policy_notes || '';
		formDndNotes = c.dnd_policy_notes || '';
		formConsentNotes = c.consent_requirement_notes || '';
		formDataSource = c.data_source;
		formReason = '';
		formError = null;
		isEditModalOpen = true;
	}

	async function saveCountry(e: SubmitEvent) {
		e.preventDefault();
		if (!formReason.trim()) {
			formError = 'A reason for this change is mandatory for auditing.';
			return;
		}
		isSaving = true;
		formError = null;

		const tzList = formTimezones
			.split(',')
			.map((s) => s.trim())
			.filter(Boolean);

		try {
			if (editingCountry) {
				const req: UpdateCountryRequest = {
					name: formName.trim(),
					calling_code: formCallingCode.trim(),
					continent: formContinent.trim(),
					region: formRegion.trim(),
					currency_code: formCurrencyCode.trim().toUpperCase(),
					timezones: tzList,
					min_national_length: Number(formMinLength),
					max_national_length: Number(formMaxLength),
					sender_id_policy_notes: formSenderNotes.trim(),
					dnd_policy_notes: formDndNotes.trim(),
					consent_requirement_notes: formConsentNotes.trim(),
					data_source: formDataSource.trim(),
					reason: formReason.trim()
				};
				await apiClient.updateCountry(editingCountry.id, req);
				toast('Country update submitted', 'success');
			} else {
				const req: CreateCountryRequest = {
					name: formName.trim(),
					iso2: formIso2.trim().toUpperCase(),
					iso3: formIso3.trim().toUpperCase(),
					numeric_code: formNumericCode.trim(),
					calling_code: formCallingCode.trim(),
					continent: formContinent.trim(),
					region: formRegion.trim(),
					currency_code: formCurrencyCode.trim().toUpperCase(),
					timezones: tzList,
					min_national_length: Number(formMinLength),
					max_national_length: Number(formMaxLength),
					sender_id_policy_notes: formSenderNotes.trim(),
					dnd_policy_notes: formDndNotes.trim(),
					consent_requirement_notes: formConsentNotes.trim(),
					data_source: formDataSource.trim(),
					reason: formReason.trim()
				};
				await apiClient.createCountry(req);
				toast('Country created successfully', 'success');
			}
			isEditModalOpen = false;
			await loadCountries();
		} catch (err) {
			if (err instanceof ApiClientError) {
				formError = err.message;
			} else {
				formError = 'Failed to save country.';
			}
		} finally {
			isSaving = false;
		}
	}

	async function toggleStatus(c: Country) {
		const newStatus = c.status === 'active' ? 'inactive' : 'active';
		const reason = prompt(`Reason for changing status to ${newStatus}:`);
		if (!reason) return;

		try {
			await apiClient.patchCountryStatus(c.id, newStatus, reason);
			toast(`Country status changed to ${newStatus}`, 'success');
			await loadCountries();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Status update failed', 'error');
		}
	}

	async function archiveCountry(c: Country) {
		const reason = prompt(`Are you sure you want to archive ${c.name} (${c.iso2})? This cannot be undone. Enter reason:`);
		if (!reason) return;

		try {
			await apiClient.archiveCountry(c.id, reason);
			toast('Country archived', 'success');
			await loadCountries();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Archive failed', 'error');
		}
	}

	async function openHistory(c: Country) {
		historyTarget = c;
		isHistoryModalOpen = true;
		isLoadingHistory = true;
		try {
			const res = await apiClient.getCountryHistory(c.id);
			historyEntries = res.data;
		} catch (err) {
			toast('Failed to load version history', 'error');
		} finally {
			isLoadingHistory = false;
		}
	}

	async function handleExport(format: 'csv' | 'json') {
		try {
			const blob = await apiClient.exportCountries(format, {
				status: statusFilter || undefined,
				continent: continentFilter || undefined
			});
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = `countries-export.${format}`;
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
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Countries</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				ISO 3166-1 reference data, dial codes, currency, and regulatory number policies ({totalCount} total).
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
					<span>Add Country</span>
				</button>
			{/if}
		</div>
	</div>

	<!-- Filters & Search -->
	<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
		<form onsubmit={handleSearch} class="flex flex-col gap-3 md:flex-row md:items-center">
			<div class="relative flex-1">
				<input
					type="text"
					bind:value={search}
					placeholder="Search by name, ISO2, ISO3, or calling code..."
					class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				/>
			</div>

			<div class="flex flex-wrap items-center gap-2">
				<select
					bind:value={continentFilter}
					onchange={() => { page = 1; loadCountries(); }}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="">All Continents</option>
					<option value="Africa">Africa</option>
					<option value="Asia">Asia</option>
					<option value="Europe">Europe</option>
					<option value="North America">North America</option>
					<option value="Oceania">Oceania</option>
					<option value="South America">South America</option>
				</select>

				<select
					bind:value={verificationFilter}
					onchange={() => { page = 1; loadCountries(); }}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="">All Verification</option>
					<option value="verified">Verified</option>
					<option value="unverified">Unverified</option>
				</select>

				<select
					bind:value={statusFilter}
					onchange={() => { page = 1; loadCountries(); }}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="">All Statuses</option>
					<option value="active">Active</option>
					<option value="inactive">Inactive</option>
					<option value="archived">Archived</option>
				</select>

				<button
					type="submit"
					class="rounded-lg bg-slate-100 px-4 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700 transition"
				>
					Filter
				</button>
			</div>
		</form>
	</div>

	<!-- Content -->
	{#if isLoading}
		<div class="flex justify-center py-16">
			<Loading message="Loading countries..." />
		</div>
	{:else if errorMessage}
		<ErrorState message={errorMessage} retry={loadCountries} />
	{:else if countries.length === 0}
		<Empty
			title="No countries found"
			description="No country records matched your search filters."
		/>
	{:else}
		<div class="overflow-x-auto rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-left text-xs">
				<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 dark:text-slate-400 uppercase font-semibold">
					<tr>
						<th class="px-4 py-3">Country</th>
						<th class="px-4 py-3">ISO Codes</th>
						<th class="px-4 py-3">Calling Code</th>
						<th class="px-4 py-3">Region</th>
						<th class="px-4 py-3">Currency</th>
						<th class="px-4 py-3">Length</th>
						<th class="px-4 py-3">Verification</th>
						<th class="px-4 py-3">Status</th>
						<th class="px-4 py-3 text-right">Actions</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-200 dark:divide-slate-800 text-slate-700 dark:text-slate-300">
					{#each countries as c}
						<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40 transition">
							<td class="px-4 py-3 font-semibold text-slate-900 dark:text-white">
								<div class="flex items-center gap-2">
									<span class="inline-flex h-5 w-7 items-center justify-center rounded bg-slate-100 dark:bg-slate-800 font-mono text-[11px] font-bold text-slate-600 dark:text-slate-300 border border-slate-200 dark:border-slate-700">
										{c.iso2}
									</span>
									<span>{c.name}</span>
								</div>
							</td>
							<td class="px-4 py-3 font-mono text-slate-600 dark:text-slate-400">
								{c.iso3} / {c.numeric_code}
							</td>
							<td class="px-4 py-3 font-mono font-medium text-indigo-600 dark:text-indigo-400">
								+{c.calling_code}
							</td>
							<td class="px-4 py-3">
								<div>{c.continent}</div>
								<div class="text-[10px] text-slate-400">{c.region}</div>
							</td>
							<td class="px-4 py-3 font-mono">
								{c.currency_code || '—'}
							</td>
							<td class="px-4 py-3 font-mono text-slate-500">
								{c.min_national_length}–{c.max_national_length}
							</td>
							<td class="px-4 py-3">
								{#if c.verification_status === 'verified'}
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
								{#if c.status === 'active'}
									<span class="inline-flex items-center rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-semibold text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300">
										Active
									</span>
								{:else if c.status === 'inactive'}
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
										onclick={() => openHistory(c)}
										class="rounded p-1 text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
										title="Version History (v{c.version})"
									>
										<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z" />
										</svg>
									</button>

									{#if canEdit && c.status !== 'archived'}
										<button
											type="button"
											onclick={() => openEditModal(c)}
											class="rounded p-1 text-slate-400 hover:text-indigo-600 dark:hover:text-indigo-400"
											title="Edit Country"
										>
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z" />
											</svg>
										</button>

										<button
											type="button"
											onclick={() => toggleStatus(c)}
											class="rounded p-1 text-slate-400 hover:text-amber-600 dark:hover:text-amber-400"
											title={c.status === 'active' ? 'Deactivate' : 'Activate'}
										>
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636" />
											</svg>
										</button>

										<button
											type="button"
											onclick={() => archiveCountry(c)}
											class="rounded p-1 text-slate-400 hover:text-rose-600 dark:hover:text-rose-400"
											title="Archive Country"
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
							onclick={() => { page--; loadCountries(); }}
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
							onclick={() => { page++; loadCountries(); }}
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
		<div class="w-full max-w-2xl rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900 my-8">
			<div class="flex items-center justify-between border-b border-slate-200 pb-4 dark:border-slate-800">
				<h2 class="text-lg font-bold text-slate-900 dark:text-white">
					{editingCountry ? `Edit Country: ${editingCountry.name} (${editingCountry.iso2})` : 'Add New Country'}
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

			<form onsubmit={saveCountry} class="mt-4 space-y-4 text-xs">
				<div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
					<div>
						<label for="country-field-1" class="block font-medium text-slate-700 dark:text-slate-300">Country Name *</label>
						<input
							id="country-field-1"
							type="text"
							bind:value={formName}
							required
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>

					<div>
						<label for="country-field-2" class="block font-medium text-slate-700 dark:text-slate-300">Calling Code * (e.g. 1, 44, 880)</label>
						<input
							id="country-field-2"
							type="text"
							bind:value={formCallingCode}
							required
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>

					{#if !editingCountry}
						<div>
							<label for="country-field-3" class="block font-medium text-slate-700 dark:text-slate-300">ISO-2 Code * (2 letters)</label>
							<input
								id="country-field-3"
								type="text"
								maxlength="2"
								bind:value={formIso2}
								required
								class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 uppercase dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							/>
						</div>

						<div>
							<label for="country-field-4" class="block font-medium text-slate-700 dark:text-slate-300">ISO-3 Code * (3 letters)</label>
							<input
								id="country-field-4"
								type="text"
								maxlength="3"
								bind:value={formIso3}
								required
								class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 uppercase dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							/>
						</div>

						<div>
							<label for="country-field-5" class="block font-medium text-slate-700 dark:text-slate-300">Numeric Code * (3 digits)</label>
							<input
								id="country-field-5"
								type="text"
								maxlength="3"
								bind:value={formNumericCode}
								required
								class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							/>
						</div>
					{/if}

					<div>
						<label for="country-field-6" class="block font-medium text-slate-700 dark:text-slate-300">Continent</label>
						<input
							id="country-field-6"
							type="text"
							bind:value={formContinent}
							placeholder="e.g. Europe, Asia"
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>

					<div>
						<label for="country-field-7" class="block font-medium text-slate-700 dark:text-slate-300">Region</label>
						<input
							id="country-field-7"
							type="text"
							bind:value={formRegion}
							placeholder="e.g. Western Europe"
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>

					<div>
						<label for="country-field-8" class="block font-medium text-slate-700 dark:text-slate-300">Currency Code (3 letters)</label>
						<input
							id="country-field-8"
							type="text"
							maxlength="3"
							bind:value={formCurrencyCode}
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 uppercase dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>

					<div>
						<label for="country-field-9" class="block font-medium text-slate-700 dark:text-slate-300">Min / Max National Length</label>
						<div class="flex items-center gap-2 mt-1">
							<input
								id="country-field-9"
								type="number"
								min="2"
								max="15"
								bind:value={formMinLength}
								class="w-1/2 rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							/>
							<span class="text-slate-400">to</span>
							<input
								type="number"
								min="2"
								max="15"
								aria-label="Maximum national length"
								bind:value={formMaxLength}
								class="w-1/2 rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							/>
						</div>
					</div>

					<div>
						<label for="country-field-10" class="block font-medium text-slate-700 dark:text-slate-300">Data Source</label>
						<input
							id="country-field-10"
							type="text"
							bind:value={formDataSource}
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
				</div>

				<div>
					<label for="country-field-11" class="block font-medium text-slate-700 dark:text-slate-300">Timezones (comma-separated)</label>
					<input
						id="country-field-11"
						type="text"
						bind:value={formTimezones}
						placeholder="e.g. Europe/London, America/New_York"
						class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div class="grid grid-cols-1 sm:grid-cols-3 gap-4">
					<div>
						<label for="country-field-12" class="block font-medium text-slate-700 dark:text-slate-300">Sender ID Policy Notes</label>
						<textarea
							id="country-field-12"
							rows="2"
							bind:value={formSenderNotes}
							placeholder="e.g. Alphanumeric allowed, pre-registration required"
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						></textarea>
					</div>

					<div>
						<label for="country-field-13" class="block font-medium text-slate-700 dark:text-slate-300">DND Policy Notes</label>
						<textarea
							id="country-field-13"
							rows="2"
							bind:value={formDndNotes}
							placeholder="e.g. National registry scrub required"
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						></textarea>
					</div>

					<div>
						<label for="country-field-14" class="block font-medium text-slate-700 dark:text-slate-300">Consent Requirement Notes</label>
						<textarea
							id="country-field-14"
							rows="2"
							bind:value={formConsentNotes}
							placeholder="e.g. Explicit opt-in required for promotional"
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						></textarea>
					</div>
				</div>

				<div>
					<label for="country-field-15" class="block font-medium text-slate-700 dark:text-slate-300">Audit Change Reason *</label>
					<input
						id="country-field-15"
						type="text"
						bind:value={formReason}
						required
						placeholder="Describe why this country record is being created or modified..."
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
						{isSaving ? 'Saving...' : editingCountry ? 'Save Changes' : 'Create Country'}
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
						Version History: {historyTarget.name} ({historyTarget.iso2})
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
						entityType="country"
						entityId={historyTarget.id}
						history={historyEntries}
						onRollback={() => {
							isHistoryModalOpen = false;
							loadCountries();
						}}
					/>
				{/if}
			</div>
		</div>
	</div>
{/if}
