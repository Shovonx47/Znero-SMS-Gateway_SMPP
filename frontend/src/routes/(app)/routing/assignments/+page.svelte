<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { CustomerRouteAssignment, RoutePlan, Organization, Country } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let assignments = $state<CustomerRouteAssignment[]>([]);
	let plans = $state<RoutePlan[]>([]);
	let customers = $state<Organization[]>([]);
	let countries = $state<Country[]>([]);

	let total = $state(0);
	let page = $state(1);
	let pageSize = $state(20);
	let customerFilter = $state('');
	let planFilter = $state('');
	let loading = $state(true);
	let errorMessage = $state('');
	let actionMessage = $state('');
	let actionError = $state('');

	// Modal state
	let showModal = $state(false);
	let modalCustomerOrgId = $state('');
	let modalPlanId = $state('');
	let modalCountryId = $state('');
	let modalPriority = $state(1);
	let modalReason = $state('');
	let modalSubmitting = $state(false);
	let modalError = $state('');

	const canCreate = $derived(auth.hasPermission('routing.assignment.create') || auth.hasPermission('routing.assignment.override'));
	const canEdit = $derived(auth.hasPermission('routing.assignment.edit') || auth.hasPermission('routing.assignment.override'));

	async function loadData() {
		loading = true;
		errorMessage = '';
		try {
			const [aRes, pRes] = await Promise.all([
				apiClient.listCustomerRouteAssignments({
					customer_org_id: customerFilter || undefined,
					plan_id: planFilter || undefined,
					page,
					page_size: pageSize
				}),
				apiClient.listRoutePlans({ page_size: 100 })
			]);
			assignments = aRes.data.items || [];
			total = aRes.data.total;
			plans = pRes.data.items || [];

			// Load customer orgs and countries for modal
			try {
				const [orgRes, cntRes] = await Promise.all([
					auth.hasPermission('org.organization.view')
						? apiClient.listOrganizations({ pageSize: 100, type: 'customer' })
						: Promise.resolve({ data: { items: [] as Organization[] } }),
					auth.hasPermission('country.country.view')
						? apiClient.listCountries({ pageSize: 250 })
						: Promise.resolve({ data: { items: [] as Country[] } })
				]);
				customers = orgRes.data.items || [];
				countries = cntRes.data.items || [];
			} catch {
				// Non-fatal
			}
		} catch (err: any) {
			errorMessage = err.message || 'Failed to load route assignments';
		} finally {
			loading = false;
		}
	}

	function handleFilter() {
		page = 1;
		loadData();
	}

	function openCreateModal() {
		modalCustomerOrgId = customers[0]?.id || '';
		modalPlanId = plans[0]?.id || '';
		modalCountryId = '';
		modalPriority = 1;
		modalReason = '';
		modalError = '';
		showModal = true;
	}

	async function handleSaveAssignment() {
		if (!modalCustomerOrgId || !modalPlanId) {
			modalError = 'Customer organization and route plan are required.';
			return;
		}
		modalSubmitting = true;
		modalError = '';
		try {
			const res = await apiClient.createCustomerRouteAssignment({
				customer_org_id: modalCustomerOrgId,
				plan_id: modalPlanId,
				country_id: modalCountryId || undefined,
				priority: modalPriority,
				reason: modalReason.trim() || undefined
			});
			if (res.data.change_request) {
				actionMessage = `Assignment submitted for approval (Change Request: ${res.data.change_request.id.slice(0, 8)}...)`;
			} else {
				actionMessage = 'Route plan assigned successfully.';
			}
			showModal = false;
			loadData();
		} catch (err: any) {
			modalError = err.message || 'Failed to assign route plan';
		} finally {
			modalSubmitting = false;
		}
	}

	async function handleDelete(a: CustomerRouteAssignment) {
		if (!confirm(`Are you sure you want to remove assignment of ${a.plan_code || 'plan'} for ${a.customer_name || 'customer'}?`)) return;
		try {
			const res = await apiClient.deleteCustomerRouteAssignment(a.id, 'Assignment removed by user');
			if (res.data.change_request) {
				actionMessage = `Assignment removal submitted for approval (Change Request: ${res.data.change_request.id.slice(0, 8)}...)`;
			} else {
				actionMessage = 'Assignment removed successfully.';
			}
			loadData();
		} catch (err: any) {
			actionError = err.message || 'Failed to remove assignment';
		}
	}

	onMount(() => {
		loadData();
	});
</script>

<div class="space-y-6">
	<!-- Page Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Customer Route Assignments</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Assign default route plans and destination-specific country overrides to customer organizations.
			</p>
		</div>

		{#if canCreate}
			<button
				type="button"
				onclick={openCreateModal}
				class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white shadow-sm hover:bg-indigo-500 transition"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
				</svg>
				<span>Assign Route Plan</span>
			</button>
		{/if}
	</div>

	<!-- Alerts -->
	{#if actionMessage}
		<div class="flex items-center justify-between rounded-xl bg-emerald-50 p-4 text-xs font-medium text-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-300">
			<span>{actionMessage}</span>
			<button type="button" onclick={() => actionMessage = ''} class="text-emerald-600 hover:text-emerald-800">Dismiss</button>
		</div>
	{/if}
	{#if actionError}
		<div class="flex items-center justify-between rounded-xl bg-rose-50 p-4 text-xs font-medium text-rose-800 dark:bg-rose-950/50 dark:text-rose-300">
			<span>{actionError}</span>
			<button type="button" onclick={() => actionError = ''} class="text-rose-600 hover:text-rose-800">Dismiss</button>
		</div>
	{/if}

	<!-- Filters -->
	<div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-4 shadow-xs sm:flex-row sm:items-center sm:justify-between dark:border-slate-800 dark:bg-slate-900">
		<div class="flex flex-1 flex-col gap-3 sm:flex-row sm:items-center">
			<select aria-label="Filter by customer"
				bind:value={customerFilter}
				onchange={handleFilter}
				class="rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
			>
				<option value="">All Customers</option>
				{#each customers as cust}
					<option value={cust.id}>{cust.name}</option>
				{/each}
			</select>

			<select aria-label="Filter by route plan"
				bind:value={planFilter}
				onchange={handleFilter}
				class="rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
			>
				<option value="">All Route Plans</option>
				{#each plans as p}
					<option value={p.id}>{p.code} - {p.name}</option>
				{/each}
			</select>
		</div>

		<button
			type="button"
			onclick={handleFilter}
			class="rounded-lg border border-slate-200 px-3 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
		>
			Refresh
		</button>
	</div>

	<!-- Content -->
	{#if loading}
		<Loading message="Loading route assignments..." />
	{:else if errorMessage}
		<ErrorState title="Error Loading Assignments" message={errorMessage} retry={loadData} />
	{:else if assignments.length === 0}
		<Empty title="No route assignments found" description="No customer organizations currently match the filter criteria." />
	{:else}
		<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-xs dark:border-slate-800 dark:bg-slate-900">
			<table class="w-full text-left text-sm text-slate-600 dark:text-slate-400">
				<thead class="border-b border-slate-200 bg-slate-50 text-xs font-semibold uppercase text-slate-500 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-400">
					<tr>
						<th class="px-4 py-3">Customer</th>
						<th class="px-4 py-3">Route Plan</th>
						<th class="px-4 py-3">Destination Scope</th>
						<th class="px-4 py-3">Priority</th>
						<th class="px-4 py-3">Status</th>
						<th class="px-4 py-3">Assigned Date</th>
						<th class="px-4 py-3 text-right">Actions</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
					{#each assignments as a}
						<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/40">
							<td class="px-4 py-3 font-semibold text-slate-900 dark:text-white">
								{a.customer_name || a.customer_org_id}
							</td>
							<td class="px-4 py-3">
								<a href={`/routing/plans/${a.plan_id}`} class="font-mono text-xs font-bold text-indigo-600 hover:underline dark:text-indigo-400">
									{a.plan_code || 'PLAN'}
								</a>
								<div class="text-xs text-slate-500">{a.plan_name || ''}</div>
							</td>
							<td class="px-4 py-3">
								{#if a.country_name}
									<span class="inline-flex items-center gap-1.5 rounded bg-blue-50 px-2 py-0.5 text-xs font-medium text-blue-700 dark:bg-blue-950/50 dark:text-blue-300">
										<span>{a.country_name}</span>
										<span class="font-mono text-[10px]">({a.country_iso2})</span>
									</span>
								{:else}
									<span class="inline-flex rounded bg-slate-100 px-2 py-0.5 text-xs font-medium text-slate-700 dark:bg-slate-800 dark:text-slate-300">
										Global Default
									</span>
								{/if}
							</td>
							<td class="px-4 py-3 font-mono text-xs">
								#{a.priority}
							</td>
							<td class="px-4 py-3">
								<span class="inline-flex rounded-full px-2 py-0.5 text-[11px] font-semibold {a.status === 'active' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300' : 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300'}">
									{a.status.toUpperCase()}
								</span>
							</td>
							<td class="px-4 py-3 text-xs text-slate-500">
								{new Date(a.created_at).toLocaleDateString()}
							</td>
							<td class="px-4 py-3 text-right">
								{#if canEdit}
									<button
										type="button"
										onclick={() => handleDelete(a)}
										class="rounded px-2.5 py-1 text-xs font-semibold text-rose-600 hover:bg-rose-50 dark:text-rose-400 dark:hover:bg-rose-950/50"
									>
										Remove
									</button>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>

<!-- Modal -->
{#if showModal}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/60 backdrop-blur-xs p-4">
		<div class="w-full max-w-lg rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900 space-y-4">
			<div class="flex items-center justify-between">
				<h2 class="text-lg font-bold text-slate-900 dark:text-white">Assign Route Plan to Customer</h2>
				<button type="button" onclick={() => showModal = false} aria-label="Close modal" class="rounded p-1 text-slate-400 hover:text-slate-600">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			{#if modalError}
				<div class="rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300">
					{modalError}
				</div>
			{/if}

			<div class="space-y-3">
				<div>
					<label for="assign-cust" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Customer Organization *</label>
					<select
						id="assign-cust"
						bind:value={modalCustomerOrgId}
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					>
						{#each customers as cust}
							<option value={cust.id}>{cust.name}</option>
						{/each}
					</select>
				</div>

				<div>
					<label for="assign-plan" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Route Plan *</label>
					<select
						id="assign-plan"
						bind:value={modalPlanId}
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					>
						{#each plans as p}
							<option value={p.id}>{p.code} - {p.name} ({p.strategy.toUpperCase()})</option>
						{/each}
					</select>
				</div>

				<div>
					<label for="assign-scope" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Destination Scope</label>
					<select
						id="assign-scope"
						bind:value={modalCountryId}
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					>
						<option value="">Global Default (Used when no country-specific plan is matched)</option>
						{#each countries as cnt}
							<option value={cnt.id}>{cnt.name} ({cnt.iso2})</option>
						{/each}
					</select>
				</div>

				<div>
					<label for="assign-priority" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Priority Order *</label>
					<input
						id="assign-priority"
						type="number"
						min="1"
						bind:value={modalPriority}
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					/>
				</div>

				<div>
					<label for="assign-reason" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Change Reason / Audit Note</label>
					<input
						id="assign-reason"
						type="text"
						bind:value={modalReason}
						placeholder="Optional reason for maker-checker review..."
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					/>
				</div>
			</div>

			<div class="flex justify-end gap-2 pt-2">
				<button
					type="button"
					onclick={() => showModal = false}
					class="rounded-lg border border-slate-200 px-4 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
				>
					Cancel
				</button>
				<button
					type="button"
					disabled={modalSubmitting}
					onclick={handleSaveAssignment}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					{modalSubmitting ? 'Saving...' : 'Assign Plan'}
				</button>
			</div>
		</div>
	</div>
{/if}
