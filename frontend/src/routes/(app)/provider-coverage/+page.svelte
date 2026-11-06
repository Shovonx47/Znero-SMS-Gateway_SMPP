<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { Country, Operator, Provider, ProviderCoverage, ProviderCoverageHistory } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let coverageItems = $state<ProviderCoverage[]>([]);
	let providers = $state<Provider[]>([]);
	let countries = $state<Country[]>([]);
	let operators = $state<Operator[]>([]);
	let totalCount = $state(0);
	let page = $state(1);
	let pageSize = 25;

	let filterProviderId = $state('');
	let filterCountryId = $state('');
	let filterStatus = $state('');
	let search = $state('');

	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	let isCreateModalOpen = $state(false);
	let formProviderId = $state('');
	let formCountryId = $state('');
	let formOperatorId = $state('');
	let formStatus = $state<'declared' | 'tested' | 'active' | 'suspended'>('declared');
	let formNotes = $state('');
	let formDataSource = $state('DEV SAMPLE');
	let formReason = $state('');
	let formError = $state<string | null>(null);
	let isSubmitting = $state(false);

	let histTarget = $state<ProviderCoverage | null>(null);
	let covHistory = $state<ProviderCoverageHistory[]>([]);

	const isPlatformOrg = $derived(auth.tenant?.actor_org_type === 'platform');
	const canCreate = $derived(isPlatformOrg && auth.hasPermission('provider.coverage.create'));

	async function loadData() {
		if (!isPlatformOrg) {
			errorMessage = '403 Forbidden: Provider Coverage is platform-only.';
			isLoading = false;
			return;
		}
		isLoading = true;
		errorMessage = null;
		try {
			const [covRes, provRes, cRes, opRes] = await Promise.all([
				apiClient.listProviderCoverage({
					provider_id: filterProviderId || undefined,
					country_id: filterCountryId || undefined,
					status: filterStatus || undefined,
					search: search.trim() || undefined,
					page,
					pageSize
				}),
				apiClient.listProviders({ pageSize: 100 }),
				apiClient.listCountries({ pageSize: 300 }),
				apiClient.listOperators({ pageSize: 200 })
			]);
			coverageItems = covRes.data.items || [];
			totalCount = covRes.data.total || 0;
			providers = provRes.data.items || [];
			countries = cRes.data.items || [];
			operators = opRes.data.items || [];
		} catch (err) {
			errorMessage = err instanceof ApiClientError ? err.message : 'Failed to load provider coverage.';
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadData();
	});

	async function handleCreate() {
		isSubmitting = true;
		formError = null;
		try {
			const res = await apiClient.createProviderCoverage({
				provider_id: formProviderId,
				country_id: formCountryId,
				operator_id: formOperatorId || null,
				status: formStatus,
				notes: formNotes.trim(),
				data_source: formDataSource.trim() || 'DEV SAMPLE',
				reason: formReason.trim() || 'Created coverage via global coverage matrix'
			});
			isCreateModalOpen = false;
			if (res.data.pending_approval) {
				toast('Coverage change submitted to Maker-Checker approval queue', 'info');
			} else {
				toast('Coverage row created', 'success');
			}
			await loadData();
		} catch (err) {
			formError = err instanceof ApiClientError ? err.message : 'Failed to create coverage';
		} finally {
			isSubmitting = false;
		}
	}

	async function handleExport(format: 'csv' | 'json') {
		try {
			const blob = await apiClient.exportProviderCoverage(format);
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = `provider_coverage.${format}`;
			a.click();
			URL.revokeObjectURL(url);
		} catch {
			toast('Export failed', 'error');
		}
	}

	async function viewHistory(cov: ProviderCoverage) {
		histTarget = cov;
		try {
			const res = await apiClient.getProviderCoverageHistory(cov.id);
			covHistory = res.data || [];
		} catch {
			toast('Failed to load coverage history', 'error');
		}
	}
</script>

<div class="space-y-6">
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Provider Coverage Matrix</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Global country and operator reach across upstream suppliers (declared, tested, active, suspended).
			</p>
		</div>

		{#if isPlatformOrg}
			<div class="flex flex-wrap items-center gap-2">
				<a
					href="/master-data/import"
					class="rounded-lg border border-indigo-300 bg-indigo-50 px-3 py-2 text-xs font-semibold text-indigo-700 hover:bg-indigo-100"
				>
					Batch Import Coverage
				</a>
				<button
					type="button"
					onclick={() => handleExport('csv')}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-50"
				>
					Export CSV
				</button>
				<button
					type="button"
					onclick={() => handleExport('json')}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-50"
				>
					Export JSON
				</button>
				{#if canCreate}
					<button
						type="button"
						onclick={() => { formProviderId = ''; formCountryId = ''; formOperatorId = ''; formNotes = ''; formReason = ''; formError = null; isCreateModalOpen = true; }}
						class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white hover:bg-indigo-500"
					>
						+ Add Coverage
					</button>
				{/if}
			</div>
		{/if}
	</div>

	{#if !isPlatformOrg}
		<div class="rounded-xl border border-rose-200 bg-rose-50 p-6 text-sm text-rose-800">
			<strong>403 Forbidden:</strong> Provider coverage is restricted to Platform Administrators.
		</div>
	{:else}
		<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="flex flex-wrap items-center gap-3">
				<input
					type="text"
					aria-label="Search coverage"
					placeholder="Search provider, country, operator..."
					bind:value={search}
					onkeydown={(e) => e.key === 'Enter' && loadData()}
					class="w-60 rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				/>
				<select
					aria-label="Filter by provider"
					bind:value={filterProviderId}
					onchange={() => { page = 1; loadData(); }}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="">All Providers</option>
					{#each providers as p}
						<option value={p.id}>{p.code} — {p.name}</option>
					{/each}
				</select>
				<select
					aria-label="Filter by country"
					bind:value={filterCountryId}
					onchange={() => { page = 1; loadData(); }}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="">All Countries</option>
					{#each countries as c}
						<option value={c.id}>{c.iso2} — {c.name}</option>
					{/each}
				</select>
				<select
					aria-label="Filter by status"
					bind:value={filterStatus}
					onchange={() => { page = 1; loadData(); }}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="">All Statuses</option>
					<option value="declared">Declared</option>
					<option value="tested">Tested</option>
					<option value="active">Active</option>
					<option value="suspended">Suspended</option>
				</select>
			</div>
		</div>

		{#if isLoading}
			<div class="flex justify-center py-16">
				<Loading message="Loading coverage matrix..." />
			</div>
		{:else if errorMessage}
			<ErrorState message={errorMessage} retry={loadData} />
		{:else if coverageItems.length === 0}
			<Empty title="No coverage records found" description="Add provider coverage manually or via Batch Import." />
		{:else}
			<div class="overflow-x-auto rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-left text-xs">
					<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 uppercase font-semibold">
						<tr>
							<th class="px-4 py-3">Provider</th>
							<th class="px-4 py-3">Country</th>
							<th class="px-4 py-3">Operator (MCC/MNC)</th>
							<th class="px-4 py-3">Status</th>
							<th class="px-4 py-3">Source</th>
							<th class="px-4 py-3">Version</th>
							<th class="px-4 py-3 text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
						{#each coverageItems as cov}
							<tr>
								<td class="px-4 py-3 font-mono font-bold text-indigo-600">
									<a href="/providers/{cov.provider_id}" class="hover:underline">{cov.provider_code}</a>
									<span class="block font-sans font-normal text-slate-500">{cov.provider_name}</span>
								</td>
								<td class="px-4 py-3 font-semibold">{cov.country_iso2} — {cov.country_name}</td>
								<td class="px-4 py-3 font-mono">
									{cov.operator_name ? `${cov.operator_name} (${cov.operator_mcc}/${cov.operator_mnc})` : 'Country-wide'}
								</td>
								<td class="px-4 py-3 uppercase font-mono font-semibold">{cov.status}</td>
								<td class="px-4 py-3">{cov.data_source}</td>
								<td class="px-4 py-3 font-mono">v{cov.version}</td>
								<td class="px-4 py-3 text-right">
									<button
										type="button"
										onclick={() => viewHistory(cov)}
										class="rounded px-2 py-1 text-xs font-semibold text-slate-600 hover:bg-slate-100"
									>
										History
									</button>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	{/if}
</div>

{#if isCreateModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-lg rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Add Provider Coverage</h2>
			{#if formError}
				<div class="mt-2 rounded bg-rose-50 p-2 text-xs text-rose-700">{formError}</div>
			{/if}
			<div class="mt-4 space-y-3 text-xs">
				<div>
					<label for="gcov-prov" class="block font-medium text-slate-700 mb-1">Provider *</label>
					<select id="gcov-prov" bind:value={formProviderId} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="">Select Provider</option>
						{#each providers as p}
							<option value={p.id}>{p.code} — {p.name}</option>
						{/each}
					</select>
				</div>
				<div>
					<label for="gcov-country" class="block font-medium text-slate-700 mb-1">Country *</label>
					<select id="gcov-country" bind:value={formCountryId} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="">Select Country</option>
						{#each countries as c}
							<option value={c.id}>{c.iso2} — {c.name}</option>
						{/each}
					</select>
				</div>
				<div>
					<label for="gcov-op" class="block font-medium text-slate-700 mb-1">Operator (Optional)</label>
					<select id="gcov-op" bind:value={formOperatorId} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="">Country-wide (All Operators)</option>
						{#each operators.filter((o) => !formCountryId || o.country_id === formCountryId) as op}
							<option value={op.id}>{op.name} ({op.mcc}/{op.mnc})</option>
						{/each}
					</select>
				</div>
				<div>
					<label for="gcov-status" class="block font-medium text-slate-700 mb-1">Status</label>
					<select id="gcov-status" bind:value={formStatus} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="declared">Declared</option>
						<option value="tested">Tested</option>
						<option value="active">Active</option>
						<option value="suspended">Suspended</option>
					</select>
				</div>
				<div>
					<label for="gcov-notes" class="block font-medium text-slate-700 mb-1">Notes</label>
					<input id="gcov-notes" type="text" bind:value={formNotes} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="gcov-reason" class="block font-medium text-slate-700 mb-1">Reason *</label>
					<input id="gcov-reason" type="text" bind:value={formReason} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
			</div>
			<div class="mt-5 flex justify-end gap-2">
				<button type="button" onclick={() => isCreateModalOpen = false} class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700">Cancel</button>
				<button
					type="button"
					disabled={isSubmitting || !formProviderId || !formCountryId}
					onclick={handleCreate}
					class="rounded-lg bg-indigo-600 px-4 py-1.5 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					Create Coverage
				</button>
			</div>
		</div>
	</div>
{/if}

{#if histTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-xl rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
			<div class="flex items-center justify-between border-b border-slate-200 pb-3">
				<h2 class="text-base font-bold text-slate-900 dark:text-white">Coverage History</h2>
				<button type="button" aria-label="Close modal" onclick={() => histTarget = null} class="text-slate-400">✕</button>
			</div>
			<div class="mt-4 max-h-80 overflow-y-auto">
				<table class="min-w-full divide-y divide-slate-200 text-xs">
					<thead>
						<tr>
							<th class="px-3 py-2 text-left">Ver</th>
							<th class="px-3 py-2 text-left">When</th>
							<th class="px-3 py-2 text-left">Reason</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200">
						{#each covHistory as h}
							<tr>
								<td class="px-3 py-2 font-mono font-bold">v{h.version}</td>
								<td class="px-3 py-2">{new Date(h.changed_at).toLocaleString()}</td>
								<td class="px-3 py-2">{h.reason}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>
	</div>
{/if}
