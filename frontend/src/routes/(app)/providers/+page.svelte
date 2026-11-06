<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { Country, Provider } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let providers = $state<Provider[]>([]);
	let countries = $state<Country[]>([]);
	let totalCount = $state(0);
	let page = $state(1);
	let pageSize = 25;
	let search = $state('');
	let typeFilter = $state('');
	let statusFilter = $state('');
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	let isCreateModalOpen = $state(false);
	let isSubmitting = $state(false);
	let formError = $state<string | null>(null);

	let formName = $state('');
	let formCode = $state('');
	let formType = $state<'smpp' | 'http' | 'other'>('smpp');
	let formLegalEntity = $state('');
	let formTechContact = $state('');
	let formCommContact = $state('');
	let formNocContact = $state('');
	let formCountryId = $state('');
	let formTimezone = $state('UTC');
	let formCurrency = $state('USD');
	let formStatus = $state<'draft' | 'active' | 'suspended' | 'inactive'>('draft');
	let formNotes = $state('');
	let formReason = $state('');

	const isPlatformOrg = $derived(auth.tenant?.actor_org_type === 'platform');
	const canCreate = $derived(isPlatformOrg && auth.hasPermission('provider.provider.create'));

	async function loadData() {
		if (!isPlatformOrg) {
			errorMessage = '403 Forbidden: Providers are platform-only and hidden from reseller and customer organizations.';
			isLoading = false;
			return;
		}
		isLoading = true;
		errorMessage = null;
		try {
			const [provRes, cRes] = await Promise.all([
				apiClient.listProviders({
					search: search.trim() || undefined,
					provider_type: typeFilter || undefined,
					status: statusFilter || undefined,
					page,
					pageSize
				}),
				apiClient.listCountries({ pageSize: 300 })
			]);
			providers = provRes.data.items || [];
			totalCount = provRes.data.total || 0;
			countries = cRes.data.items || [];
		} catch (err) {
			errorMessage = err instanceof ApiClientError ? err.message : 'Failed to load providers.';
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadData();
	});

	function openCreateModal() {
		formName = '';
		formCode = '';
		formType = 'smpp';
		formLegalEntity = '';
		formTechContact = '';
		formCommContact = '';
		formNocContact = '';
		formCountryId = '';
		formTimezone = 'UTC';
		formCurrency = 'USD';
		formStatus = 'draft';
		formNotes = '';
		formReason = '';
		formError = null;
		isCreateModalOpen = true;
	}

	async function handleCreate() {
		isSubmitting = true;
		formError = null;
		try {
			const res = await apiClient.createProvider({
				name: formName.trim(),
				code: formCode.trim().toUpperCase(),
				provider_type: formType,
				legal_entity_name: formLegalEntity.trim(),
				contact_technical: formTechContact.trim(),
				contact_commercial: formCommContact.trim(),
				contact_noc: formNocContact.trim(),
				country_id: formCountryId || null,
				timezone: formTimezone.trim() || 'UTC',
				currency_code: formCurrency.trim().toUpperCase() || 'USD',
				status: formStatus,
				notes: formNotes.trim(),
				reason: formReason.trim() || 'Created provider via UI'
			});
			isCreateModalOpen = false;
			if (res.data.pending_approval) {
				toast('Provider creation submitted to Maker-Checker approval queue', 'info');
			} else {
				toast('Provider created', 'success');
			}
			await loadData();
		} catch (err) {
			formError = err instanceof ApiClientError ? err.message : 'Failed to create provider';
		} finally {
			isSubmitting = false;
		}
	}

	async function handleExport(format: 'csv' | 'json') {
		try {
			const blob = await apiClient.exportProviders(format);
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = `providers.${format}`;
			a.click();
			URL.revokeObjectURL(url);
		} catch {
			toast('Failed to export providers', 'error');
		}
	}
</script>

<div class="space-y-6">
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Upstream Providers</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Platform-owned upstream SMS suppliers, connection configurations, and coverage targets.
			</p>
		</div>

		{#if isPlatformOrg}
			<div class="flex flex-wrap items-center gap-2">
				<button
					type="button"
					onclick={() => handleExport('csv')}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300"
				>
					Export CSV
				</button>
				<button
					type="button"
					onclick={() => handleExport('json')}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300"
				>
					Export JSON
				</button>
				{#if canCreate}
					<button
						type="button"
						onclick={openCreateModal}
						class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500"
					>
						+ New Provider
					</button>
				{/if}
			</div>
		{/if}
	</div>

	{#if !isPlatformOrg}
		<div class="rounded-xl border border-rose-200 bg-rose-50 p-6 text-sm text-rose-800 dark:border-rose-800 dark:bg-rose-950/40 dark:text-rose-200">
			<strong>403 Forbidden — Platform-Only Resource:</strong> Upstream providers are strictly restricted to the Platform organization. Reseller and customer organizations cannot view or manage upstream providers.
		</div>
	{:else}
		<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="flex flex-wrap items-center gap-3">
				<input
					type="text"
					aria-label="Search providers"
					placeholder="Search name, code, legal entity..."
					bind:value={search}
					onkeydown={(e) => e.key === 'Enter' && loadData()}
					class="w-64 rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				/>
				<select
					aria-label="Filter by provider type"
					bind:value={typeFilter}
					onchange={() => { page = 1; loadData(); }}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="">All Types</option>
					<option value="smpp">SMPP</option>
					<option value="http">HTTP</option>
					<option value="other">Other</option>
				</select>
				<select
					aria-label="Filter by status"
					bind:value={statusFilter}
					onchange={() => { page = 1; loadData(); }}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="">All Statuses</option>
					<option value="active">Active</option>
					<option value="draft">Draft</option>
					<option value="suspended">Suspended</option>
					<option value="inactive">Inactive</option>
				</select>
				<button
					type="button"
					onclick={() => { page = 1; loadData(); }}
					class="rounded-lg bg-slate-900 px-3 py-2 text-xs font-semibold text-white hover:bg-slate-700 dark:bg-indigo-600"
				>
					Search
				</button>
			</div>
		</div>

		{#if isLoading}
			<div class="flex justify-center py-16">
				<Loading message="Loading providers..." />
			</div>
		{:else if errorMessage}
			<ErrorState message={errorMessage} retry={loadData} />
		{:else if providers.length === 0}
			<Empty title="No providers found" description="Create a new upstream provider to configure connections and coverage." />
		{:else}
			<div class="overflow-x-auto rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-left text-xs">
					<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 uppercase font-semibold">
						<tr>
							<th class="px-4 py-3">Code</th>
							<th class="px-4 py-3">Provider Name</th>
							<th class="px-4 py-3">Type</th>
							<th class="px-4 py-3">Country</th>
							<th class="px-4 py-3">Currency / TZ</th>
							<th class="px-4 py-3">Status</th>
							<th class="px-4 py-3">Version</th>
							<th class="px-4 py-3 text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800 text-slate-700 dark:text-slate-300">
						{#each providers as p}
							<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40 transition">
								<td class="px-4 py-3 font-mono font-bold text-indigo-600 dark:text-indigo-400">
									<a href="/providers/{p.id}" class="hover:underline">{p.code}</a>
								</td>
								<td class="px-4 py-3 font-semibold text-slate-900 dark:text-white">
									<a href="/providers/{p.id}" class="hover:underline">{p.name}</a>
									{#if p.legal_entity_name}
										<div class="text-[11px] font-normal text-slate-400">{p.legal_entity_name}</div>
									{/if}
								</td>
								<td class="px-4 py-3 uppercase font-mono">{p.provider_type}</td>
								<td class="px-4 py-3">{p.country_iso2 ? `${p.country_iso2} — ${p.country_name}` : '—'}</td>
								<td class="px-4 py-3 font-mono">{p.currency_code} • {p.timezone}</td>
								<td class="px-4 py-3">
									<span class="inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase {p.status === 'active'
										? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
										: p.status === 'draft'
											? 'bg-amber-100 text-amber-800 dark:bg-amber-950/60 dark:text-amber-300'
											: 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-300'}">
										{p.status}
									</span>
								</td>
								<td class="px-4 py-3 font-mono">v{p.version}</td>
								<td class="px-4 py-3 text-right">
									<a
										href="/providers/{p.id}"
										class="rounded-lg bg-indigo-50 px-3 py-1.5 text-xs font-semibold text-indigo-700 hover:bg-indigo-100 dark:bg-indigo-950/50 dark:text-indigo-300"
									>
										Manage Connections & Coverage
									</a>
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
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm overflow-y-auto">
		<div class="w-full max-w-2xl rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900 my-8">
			<div class="flex items-center justify-between border-b border-slate-200 pb-3 dark:border-slate-800">
				<h2 class="text-base font-bold text-slate-900 dark:text-white">Create Upstream Provider</h2>
				<button
					type="button"
					aria-label="Close modal"
					onclick={() => isCreateModalOpen = false}
					class="rounded p-1 text-slate-400 hover:text-slate-600"
				>
					✕
				</button>
			</div>

			{#if formError}
				<div class="mt-3 rounded-lg bg-rose-50 p-3 text-xs text-rose-700 border border-rose-200">
					{formError}
				</div>
			{/if}

			<div class="mt-4 grid grid-cols-1 sm:grid-cols-2 gap-4 text-xs">
				<div>
					<label for="prov-name" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Provider Name *</label>
					<input id="prov-name" type="text" bind:value={formName} placeholder="e.g. DEV SAMPLE Carrier" class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="prov-code" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Unique Code *</label>
					<input id="prov-code" type="text" bind:value={formCode} placeholder="e.g. DEV_CARRIER_1" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono uppercase dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="prov-type" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Primary Protocol *</label>
					<select id="prov-type" bind:value={formType} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="smpp">SMPP</option>
						<option value="http">HTTP</option>
						<option value="other">Other</option>
					</select>
				</div>
				<div>
					<label for="prov-status" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Initial Status</label>
					<select id="prov-status" bind:value={formStatus} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="draft">Draft</option>
						<option value="active">Active</option>
						<option value="suspended">Suspended</option>
						<option value="inactive">Inactive</option>
					</select>
				</div>
				<div>
					<label for="prov-legal" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Legal Entity Name</label>
					<input id="prov-legal" type="text" bind:value={formLegalEntity} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="prov-country" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Country</label>
					<select id="prov-country" bind:value={formCountryId} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="">— Optional —</option>
						{#each countries as c}
							<option value={c.id}>{c.iso2} — {c.name}</option>
						{/each}
					</select>
				</div>
				<div>
					<label for="prov-curr" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Currency (ISO 4217)</label>
					<input id="prov-curr" type="text" maxlength="3" bind:value={formCurrency} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono uppercase dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="prov-tz" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Timezone</label>
					<input id="prov-tz" type="text" bind:value={formTimezone} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="prov-tech" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Technical Contact</label>
					<input id="prov-tech" type="text" bind:value={formTechContact} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="prov-noc" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">NOC Contact</label>
					<input id="prov-noc" type="text" bind:value={formNocContact} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div class="sm:col-span-2">
					<label for="prov-notes" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Notes</label>
					<textarea id="prov-notes" rows="2" bind:value={formNotes} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"></textarea>
				</div>
				<div class="sm:col-span-2">
					<label for="prov-reason" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Audit Reason *</label>
					<input id="prov-reason" type="text" bind:value={formReason} placeholder="Reason for creating this provider" class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
			</div>

			<div class="mt-6 flex items-center justify-end gap-2">
				<button type="button" onclick={() => isCreateModalOpen = false} class="rounded-lg border border-slate-300 px-4 py-2 text-xs font-semibold text-slate-700">
					Cancel
				</button>
				<button
					type="button"
					disabled={isSubmitting || !formName.trim() || !formCode.trim()}
					onclick={handleCreate}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					{isSubmitting ? 'Saving...' : 'Create Provider'}
				</button>
			</div>
		</div>
	</div>
{/if}
