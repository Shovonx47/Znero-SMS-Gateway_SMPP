<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { apiClient } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { RoutePlan, RoutePlanEntry, RoutePlanHistory, CustomerRouteAssignment, ProviderConnection, Country, Operator } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	const planId = $page.params.id;

	let plan = $state<RoutePlan | null>(null);
	let entries = $state<RoutePlanEntry[]>([]);
	let history = $state<RoutePlanHistory[]>([]);
	let assignments = $state<CustomerRouteAssignment[]>([]);
	let availableConnections = $state<ProviderConnection[]>([]);
	let availableCountries = $state<Country[]>([]);
	let availableOperators = $state<Operator[]>([]);

	let activeTab = $state<'entries' | 'assignments' | 'validation' | 'history'>('entries');
	let loading = $state(true);
	let errorMessage = $state('');
	let actionMessage = $state('');
	let actionError = $state('');

	// Entry Modal state
	let showEntryModal = $state(false);
	let editingEntry = $state<RoutePlanEntry | null>(null);
	let entryConnectionId = $state('');
	let entryScopeType = $state<'global' | 'country' | 'operator' | 'prefix'>('global');
	let entryCountryId = $state('');
	let entryOperatorId = $state('');
	let entryPrefix = $state('');
	let entryPriority = $state(1);
	let entryWeight = $state(100);
	let entryMinMargin = $state('');
	let entrySenderIdTypes = $state<string[]>(['alphanumeric', 'numeric']);
	let entryAllowTested = $state(false);
	let entryReason = $state('');
	let entrySubmitting = $state(false);
	let entryError = $state('');

	// Reason prompt modal for Plan Activate / Deactivate
	let showReasonModal = $state(false);
	let pendingAction = $state<'activate' | 'deactivate' | 'archive' | null>(null);
	let actionReason = $state('');
	let actionSubmitting = $state(false);

	const canEdit = $derived(auth.hasPermission('routing.plan.edit') || auth.hasPermission('routing.plan.override'));
	const canActivate = $derived(auth.hasPermission('routing.plan.activate') || auth.hasPermission('routing.plan.override'));
	const canDeactivate = $derived(auth.hasPermission('routing.plan.deactivate') || auth.hasPermission('routing.plan.override'));
	const canEntryCreate = $derived(auth.hasPermission('routing.entry.create') || auth.hasPermission('routing.entry.override'));

	async function loadAll() {
		loading = true;
		errorMessage = '';
		try {
			const [pRes, eRes, hRes, aRes] = await Promise.all([
				apiClient.getRoutePlan(planId),
				apiClient.listRoutePlanEntries(planId),
				apiClient.getRoutePlanHistory(planId),
				apiClient.listCustomerRouteAssignments({ plan_id: planId })
			]);
			plan = pRes.data;
			entries = eRes.data || [];
			history = hRes.data || [];
			assignments = aRes.data.items || [];

			// Load connections and master data for form dropdowns
			try {
				const [connRes, cntRes, opRes] = await Promise.all([
					apiClient.listAllConnections(),
					apiClient.listCountries({ pageSize: 250 }),
					apiClient.listOperators({ pageSize: 250 })
				]);
				availableConnections = connRes.data || [];
				availableCountries = cntRes.data.items || [];
				availableOperators = opRes.data.items || [];
			} catch {
				// Non-fatal if master data fetch fails
			}
		} catch (err: any) {
			errorMessage = err.message || 'Failed to load route plan details';
		} finally {
			loading = false;
		}
	}

	function openAddEntryModal() {
		editingEntry = null;
		const activeConn = availableConnections.find((c) => c.status === 'active') || availableConnections[0];
		entryConnectionId = activeConn?.id || '';
		entryScopeType = 'global';
		entryCountryId = '';
		entryOperatorId = '';
		entryPrefix = '';
		entryPriority = entries.length + 1;
		entryWeight = 100;
		entryMinMargin = '';
		entrySenderIdTypes = ['alphanumeric', 'numeric'];
		entryAllowTested = false;
		entryReason = '';
		entryError = '';
		showEntryModal = true;
	}

	function openEditEntryModal(e: RoutePlanEntry) {
		editingEntry = e;
		entryConnectionId = e.connection_id;
		entryScopeType = e.scope_type;
		entryCountryId = e.scope_country_id || '';
		entryOperatorId = e.scope_operator_id || '';
		entryPrefix = e.scope_prefix || '';
		entryPriority = e.priority;
		entryWeight = e.weight;
		entryMinMargin = e.min_margin_override || '';
		entrySenderIdTypes = e.allowed_sender_id_types || ['alphanumeric', 'numeric'];
		entryAllowTested = e.allow_tested_coverage;
		entryReason = '';
		entryError = '';
		showEntryModal = true;
	}

	async function handleSaveEntry() {
		if (!entryConnectionId) {
			entryError = 'Egress connection is required.';
			return;
		}
		entrySubmitting = true;
		entryError = '';
		try {
			if (editingEntry) {
				const res = await apiClient.updateRoutePlanEntry(planId, editingEntry.id, {
					priority: entryPriority,
					weight: entryWeight,
					min_margin_override: entryMinMargin.trim() || undefined,
					allowed_sender_id_types: entrySenderIdTypes,
					allow_tested_coverage: entryAllowTested,
					reason: entryReason.trim() || undefined
				});
				if (res.data.change_request) {
					actionMessage = `Entry update submitted for approval (CR: ${res.data.change_request.id.slice(0, 8)}...)`;
				} else {
					actionMessage = 'Entry updated successfully.';
				}
			} else {
				const res = await apiClient.createRoutePlanEntry(planId, {
					connection_id: entryConnectionId,
					scope_type: entryScopeType,
					scope_country_id: entryScopeType === 'country' ? entryCountryId || undefined : undefined,
					scope_operator_id: entryScopeType === 'operator' ? entryOperatorId || undefined : undefined,
					scope_prefix: entryScopeType === 'prefix' ? entryPrefix.trim() || undefined : undefined,
					priority: entryPriority,
					weight: entryWeight,
					min_margin_override: entryMinMargin.trim() || undefined,
					allowed_sender_id_types: entrySenderIdTypes,
					allow_tested_coverage: entryAllowTested,
					reason: entryReason.trim() || undefined
				});
				if (res.data.change_request) {
					actionMessage = `Entry creation submitted for approval (CR: ${res.data.change_request.id.slice(0, 8)}...)`;
				} else {
					actionMessage = 'Entry created successfully.';
				}
			}
			showEntryModal = false;
			const eRes = await apiClient.listRoutePlanEntries(planId);
			entries = eRes.data || [];
		} catch (err: any) {
			entryError = err.message || 'Failed to save route plan entry';
		} finally {
			entrySubmitting = false;
		}
	}

	async function handleDeleteEntry(e: RoutePlanEntry) {
		if (!confirm(`Are you sure you want to remove entry #${e.priority}?`)) return;
		try {
			const res = await apiClient.deleteRoutePlanEntry(planId, e.id, 'Entry deleted by user');
			if (res.data.change_request) {
				actionMessage = `Entry deletion submitted for approval (CR: ${res.data.change_request.id.slice(0, 8)}...)`;
			} else {
				actionMessage = 'Entry removed successfully.';
			}
			const eRes = await apiClient.listRoutePlanEntries(planId);
			entries = eRes.data || [];
		} catch (err: any) {
			actionError = err.message || 'Failed to delete entry';
		}
	}

	async function handleExecuteAction() {
		if (!pendingAction) return;
		actionSubmitting = true;
		actionError = '';
		actionMessage = '';
		try {
			if (pendingAction === 'activate') {
				const res = await apiClient.activateRoutePlan(planId, actionReason.trim() || undefined);
				if (res.data.change_request) {
					actionMessage = `Plan activation submitted for approval (Change Request: ${res.data.change_request.id.slice(0, 8)}...)`;
				} else {
					actionMessage = 'Route plan activated successfully.';
				}
			} else if (pendingAction === 'deactivate') {
				const res = await apiClient.deactivateRoutePlan(planId, actionReason.trim() || undefined);
				if (res.data.change_request) {
					actionMessage = `Plan deactivation submitted for approval (Change Request: ${res.data.change_request.id.slice(0, 8)}...)`;
				} else {
					actionMessage = 'Route plan deactivated successfully.';
				}
			} else if (pendingAction === 'archive') {
				await apiClient.archiveRoutePlan(planId);
				actionMessage = 'Route plan archived successfully.';
			}
			showReasonModal = false;
			actionReason = '';
			loadAll();
		} catch (err: any) {
			actionError = err.message || 'Failed to execute action';
		} finally {
			actionSubmitting = false;
		}
	}

	async function handleRollback(historyId: string) {
		const reason = prompt('Please enter a mandatory audit reason for rollback:');
		if (!reason || !reason.trim()) return;
		try {
			await apiClient.rollbackRoutePlan(planId, historyId, reason.trim());
			actionMessage = 'Route plan successfully rolled back.';
			loadAll();
		} catch (err: any) {
			actionError = err.message || 'Failed to rollback route plan';
		}
	}

	function validatePlan(): { valid: boolean; errors: string[]; warnings: string[] } {
		const errors: string[] = [];
		const warnings: string[] = [];
		if (entries.length === 0) {
			errors.push('Route plan has no entries. At least 1 active connection entry is required for activation.');
		}
		const activeEntries = entries.filter((e) => e.status === 'active');
		if (entries.length > 0 && activeEntries.length === 0) {
			errors.push('All entries in this route plan are disabled.');
		}
		if (plan?.strategy === 'weighted') {
			const totalWeight = activeEntries.reduce((sum, e) => sum + e.weight, 0);
			if (totalWeight !== 100) {
				errors.push(`Weighted strategy requires active entries to sum to exactly 100% (currently ${totalWeight}%).`);
			}
		}
		const priorities = new Set<number>();
		for (const e of activeEntries) {
			if (priorities.has(e.priority)) {
				warnings.push(`Duplicate priority #${e.priority} detected. Deterministic tie-breaker (connection UUID) will be used.`);
			}
			priorities.add(e.priority);
		}
		return { valid: errors.length === 0, errors, warnings };
	}

	const validation = $derived(validatePlan());

	onMount(() => {
		loadAll();
	});
</script>

<div class="space-y-6">
	<!-- Breadcrumb & Back -->
	<div class="flex items-center gap-2 text-xs text-slate-500">
		<a href="/routing/plans" class="hover:text-indigo-600 dark:hover:text-indigo-400">Route Plans</a>
		<span>/</span>
		<span class="font-mono text-slate-700 dark:text-slate-300">{plan?.code || planId.slice(0, 8)}</span>
	</div>

	{#if loading}
		<Loading message="Loading route plan details..." />
	{:else if errorMessage || !plan}
		<ErrorState title="Error Loading Plan" message={errorMessage || 'Plan not found'} retry={loadAll} />
	{:else}
		<!-- Action Feedback Alerts -->
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

		<!-- Plan Header Card -->
		<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900 space-y-4">
			<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
				<div>
					<div class="flex items-center gap-3">
						<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">{plan.name}</h1>
						<span class="font-mono rounded bg-slate-100 px-2 py-0.5 text-xs font-bold text-slate-700 dark:bg-slate-800 dark:text-slate-300">
							{plan.code}
						</span>
						<span class="inline-flex rounded-full px-2.5 py-0.5 text-xs font-semibold uppercase {plan.status === 'active' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300' : 'bg-slate-100 text-slate-800 dark:bg-slate-800 dark:text-slate-300'}">
							{plan.status}
						</span>
					</div>
					{#if plan.description}
						<p class="mt-1 text-sm text-slate-500 dark:text-slate-400">{plan.description}</p>
					{/if}
				</div>

				<!-- Lifecycle Actions -->
				<div class="flex items-center gap-2">
					{#if plan.status === 'draft' || plan.status === 'deactivated'}
						{#if canActivate}
							<button
								type="button"
								onclick={() => { pendingAction = 'activate'; actionReason = ''; showReasonModal = true; }}
								class="rounded-lg bg-emerald-600 px-3.5 py-1.5 text-xs font-semibold text-white shadow-sm hover:bg-emerald-500 transition"
							>
								Activate Plan
							</button>
						{/if}
					{:else if plan.status === 'active'}
						{#if canDeactivate}
							<button
								type="button"
								onclick={() => { pendingAction = 'deactivate'; actionReason = ''; showReasonModal = true; }}
								class="rounded-lg border border-amber-300 bg-amber-50 px-3.5 py-1.5 text-xs font-semibold text-amber-800 hover:bg-amber-100 transition dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-300"
							>
								Deactivate Plan
							</button>
						{/if}
					{/if}

					{#if plan.status !== 'archived'}
						<button
							type="button"
							onclick={() => { pendingAction = 'archive'; actionReason = ''; showReasonModal = true; }}
							class="rounded-lg border border-slate-200 px-3 py-1.5 text-xs font-semibold text-slate-600 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-400 dark:hover:bg-slate-800"
						>
							Archive
						</button>
					{/if}
				</div>
			</div>

			<!-- Meta chips -->
			<div class="flex flex-wrap items-center gap-6 border-t border-slate-100 pt-4 text-xs text-slate-500 dark:border-slate-800 dark:text-slate-400">
				<div>
					<span class="font-semibold text-slate-700 dark:text-slate-300">Strategy:</span>
					<span class="ml-1 uppercase font-bold text-indigo-600 dark:text-indigo-400">{plan.strategy}</span>
				</div>
				<div>
					<span class="font-semibold text-slate-700 dark:text-slate-300">Version:</span>
					<span class="ml-1 font-mono">v{plan.version}</span>
				</div>
				<div>
					<span class="font-semibold text-slate-700 dark:text-slate-300">Active Entries:</span>
					<span class="ml-1 font-bold">{entries.filter((e) => e.status === 'active').length}</span> of {entries.length}
				</div>
				<div>
					<span class="font-semibold text-slate-700 dark:text-slate-300">Assigned Customers:</span>
					<span class="ml-1 font-bold">{assignments.length}</span>
				</div>
				<div>
					<span class="font-semibold text-slate-700 dark:text-slate-300">Last Modified:</span>
					<span class="ml-1">{new Date(plan.updated_at).toLocaleString()}</span>
				</div>
			</div>
		</div>

		<!-- Tabs Bar -->
		<div class="border-b border-slate-200 dark:border-slate-800">
			<nav class="flex gap-6 text-sm font-semibold">
				<button
					type="button"
					onclick={() => activeTab = 'entries'}
					class="border-b-2 pb-3 transition {activeTab === 'entries' ? 'border-indigo-600 text-indigo-600 dark:text-indigo-400' : 'border-transparent text-slate-500 hover:text-slate-700 dark:hover:text-slate-300'}"
				>
					Routing Entries ({entries.length})
				</button>
				<button
					type="button"
					onclick={() => activeTab = 'assignments'}
					class="border-b-2 pb-3 transition {activeTab === 'assignments' ? 'border-indigo-600 text-indigo-600 dark:text-indigo-400' : 'border-transparent text-slate-500 hover:text-slate-700 dark:hover:text-slate-300'}"
				>
					Customer Assignments ({assignments.length})
				</button>
				<button
					type="button"
					onclick={() => activeTab = 'validation'}
					class="border-b-2 pb-3 transition flex items-center gap-1.5 {activeTab === 'validation' ? 'border-indigo-600 text-indigo-600 dark:text-indigo-400' : 'border-transparent text-slate-500 hover:text-slate-700 dark:hover:text-slate-300'}"
				>
					<span>Validation</span>
					{#if !validation.valid}
						<span class="h-2 w-2 rounded-full bg-rose-500"></span>
					{:else if validation.warnings.length > 0}
						<span class="h-2 w-2 rounded-full bg-amber-500"></span>
					{/if}
				</button>
				<button
					type="button"
					onclick={() => activeTab = 'history'}
					class="border-b-2 pb-3 transition {activeTab === 'history' ? 'border-indigo-600 text-indigo-600 dark:text-indigo-400' : 'border-transparent text-slate-500 hover:text-slate-700 dark:hover:text-slate-300'}"
				>
					Version History ({history.length})
				</button>
			</nav>
		</div>

		<!-- Tab 1: Entries -->
		{#if activeTab === 'entries'}
			<div class="space-y-4">
				<div class="flex items-center justify-between">
					<p class="text-xs text-slate-500">
						Egress connections ordered by priority or evaluated by least cost / weight according to plan strategy.
					</p>
					{#if canEntryCreate}
						<button
							type="button"
							onclick={openAddEntryModal}
							class="inline-flex items-center gap-1.5 rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-semibold text-white shadow-xs hover:bg-indigo-500 transition"
						>
							<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
							</svg>
							<span>Add Egress Entry</span>
						</button>
					{/if}
				</div>

				{#if entries.length === 0}
					<Empty title="No entries configured" description="Add at least one egress connection entry to this route plan." />
				{:else}
					<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-xs dark:border-slate-800 dark:bg-slate-900">
						<table class="w-full text-left text-sm text-slate-600 dark:text-slate-400">
							<thead class="border-b border-slate-200 bg-slate-50 text-xs font-semibold uppercase text-slate-500 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-400">
								<tr>
									<th class="px-4 py-3">Priority</th>
									<th class="px-4 py-3">Scope</th>
									<th class="px-4 py-3">Connection</th>
									<th class="px-4 py-3">Weight</th>
									<th class="px-4 py-3">Min Margin</th>
									<th class="px-4 py-3">Sender ID Types</th>
									<th class="px-4 py-3">Status</th>
									<th class="px-4 py-3 text-right">Actions</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
								{#each entries as entry}
									<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/40">
										<td class="px-4 py-3 font-mono text-xs font-bold text-slate-900 dark:text-white">
											#{entry.priority}
										</td>
										<td class="px-4 py-3">
											<span class="inline-flex rounded bg-slate-100 px-2 py-0.5 text-xs font-medium uppercase text-slate-700 dark:bg-slate-800 dark:text-slate-300">
												{entry.scope_type}
											</span>
											{#if entry.scope_country_name}
												<div class="text-[11px] text-slate-500 mt-0.5">{entry.scope_country_name}</div>
											{/if}
											{#if entry.scope_prefix}
												<div class="font-mono text-[11px] text-slate-500 mt-0.5">Prefix: {entry.scope_prefix}</div>
											{/if}
										</td>
										<td class="px-4 py-3">
											<div class="font-semibold text-xs text-slate-900 dark:text-white">
												{entry.connection_name || entry.connection_id.slice(0, 8) + '...'}
											</div>
											<div class="text-[11px] text-slate-500">{entry.provider_name || 'Provider'}</div>
										</td>
										<td class="px-4 py-3 font-mono text-xs">
											{entry.weight}%
										</td>
										<td class="px-4 py-3 font-mono text-xs">
											{entry.min_margin_override ? `${entry.min_margin_override}%` : 'Default'}
										</td>
										<td class="px-4 py-3 text-xs">
											<span class="capitalize">{entry.allowed_sender_id_types.join(', ')}</span>
										</td>
										<td class="px-4 py-3">
											<span class="inline-flex rounded-full px-2 py-0.5 text-[11px] font-semibold {entry.status === 'active' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300' : 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300'}">
												{entry.status.toUpperCase()}
											</span>
										</td>
										<td class="px-4 py-3 text-right space-x-1">
											{#if canEdit}
												<button
													type="button"
													onclick={() => openEditEntryModal(entry)}
													class="rounded px-2 py-1 text-xs font-semibold text-indigo-600 hover:bg-indigo-50 dark:text-indigo-400 dark:hover:bg-indigo-950/50"
												>
													Edit
												</button>
												<button
													type="button"
													onclick={() => handleDeleteEntry(entry)}
													class="rounded px-2 py-1 text-xs font-semibold text-rose-600 hover:bg-rose-50 dark:text-rose-400 dark:hover:bg-rose-950/50"
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
		{/if}

		<!-- Tab 2: Assignments -->
		{#if activeTab === 'assignments'}
			<div class="space-y-4">
				<p class="text-xs text-slate-500">
					Customers currently assigned to use this route plan for SMS dispatch.
				</p>
				{#if assignments.length === 0}
					<Empty title="No customer assignments" description="This route plan has not been assigned to any customers yet." />
				{:else}
					<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-xs dark:border-slate-800 dark:bg-slate-900">
						<table class="w-full text-left text-sm text-slate-600 dark:text-slate-400">
							<thead class="border-b border-slate-200 bg-slate-50 text-xs font-semibold uppercase text-slate-500 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-400">
								<tr>
									<th class="px-4 py-3">Customer</th>
									<th class="px-4 py-3">Destination Scope</th>
									<th class="px-4 py-3">Priority</th>
									<th class="px-4 py-3">Status</th>
									<th class="px-4 py-3">Assigned Date</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
								{#each assignments as a}
									<tr>
										<td class="px-4 py-3 font-semibold text-slate-900 dark:text-white">
											{a.customer_name || a.customer_org_id}
										</td>
										<td class="px-4 py-3">
											{a.country_name ? `${a.country_name} (${a.country_iso2})` : 'Global (Default)'}
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
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/if}
			</div>
		{/if}

		<!-- Tab 3: Validation -->
		{#if activeTab === 'validation'}
			<div class="space-y-4">
				<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900 space-y-4">
					<h3 class="text-sm font-bold text-slate-900 dark:text-white">Route Plan Activation Health Check</h3>
					<p class="text-xs text-slate-500">
						Automated sanity verification checking entry weights, scope boundaries, and connection reachability.
					</p>

					{#if validation.errors.length > 0}
						<div class="rounded-xl bg-rose-50 p-4 dark:bg-rose-950/50 space-y-2">
							<div class="text-xs font-bold text-rose-800 dark:text-rose-300">Blocking Errors (Activation Blocked):</div>
							<ul class="list-disc pl-5 text-xs text-rose-700 dark:text-rose-400 space-y-1">
								{#each validation.errors as err}
									<li>{err}</li>
								{/each}
							</ul>
						</div>
					{:else}
						<div class="rounded-xl bg-emerald-50 p-4 text-xs font-semibold text-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-300">
							All activation requirements satisfied. This plan is eligible to be activated for production traffic.
						</div>
					{/if}

					{#if validation.warnings.length > 0}
						<div class="rounded-xl bg-amber-50 p-4 dark:bg-amber-950/50 space-y-2">
							<div class="text-xs font-bold text-amber-800 dark:text-amber-300">Advisory Warnings:</div>
							<ul class="list-disc pl-5 text-xs text-amber-700 dark:text-amber-400 space-y-1">
								{#each validation.warnings as warn}
									<li>{warn}</li>
								{/each}
							</ul>
						</div>
					{/if}
				</div>
			</div>
		{/if}

		<!-- Tab 4: History -->
		{#if activeTab === 'history'}
			<div class="space-y-4">
				{#if history.length === 0}
					<Empty title="No history found" description="No version history records exist for this route plan yet." />
				{:else}
					<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-xs dark:border-slate-800 dark:bg-slate-900">
						<table class="w-full text-left text-sm text-slate-600 dark:text-slate-400">
							<thead class="border-b border-slate-200 bg-slate-50 text-xs font-semibold uppercase text-slate-500 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-400">
								<tr>
									<th class="px-4 py-3">Timestamp</th>
									<th class="px-4 py-3">Change Type</th>
									<th class="px-4 py-3">Changed By</th>
									<th class="px-4 py-3 text-right">Actions</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
								{#each history as h}
									<tr>
										<td class="px-4 py-3 text-xs text-slate-500">
											{new Date(h.changed_at).toLocaleString()}
										</td>
										<td class="px-4 py-3 font-semibold text-xs text-slate-900 dark:text-white uppercase">
											{h.change_type}
										</td>
										<td class="px-4 py-3 font-mono text-xs text-slate-500">
											{h.changed_by.slice(0, 8)}...
										</td>
										<td class="px-4 py-3 text-right">
											{#if canEdit}
												<button
													type="button"
													onclick={() => handleRollback(h.id)}
													class="rounded px-2.5 py-1 text-xs font-semibold text-amber-600 hover:bg-amber-50 dark:text-amber-400 dark:hover:bg-amber-950/50"
												>
													Rollback to Version
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
		{/if}
	{/if}
</div>

<!-- Entry Modal -->
{#if showEntryModal}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/60 backdrop-blur-xs p-4">
		<div class="w-full max-w-lg rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900 space-y-4 max-h-[90vh] overflow-y-auto">
			<div class="flex items-center justify-between">
				<h2 class="text-lg font-bold text-slate-900 dark:text-white">
					{editingEntry ? 'Edit Route Entry' : 'Add Egress Connection Entry'}
				</h2>
				<button type="button" onclick={() => showEntryModal = false} aria-label="Close modal" class="rounded p-1 text-slate-400 hover:text-slate-600">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			{#if entryError}
				<div class="rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300">
					{entryError}
				</div>
			{/if}

			<div class="space-y-3">
				<div>
					<label for="entry-conn" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Egress Connection *</label>
					<select
						id="entry-conn"
						bind:value={entryConnectionId}
						disabled={!!editingEntry}
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					>
						{#each availableConnections as c}
							<option value={c.id}>{c.name} - {c.protocol.toUpperCase()} ({c.status})</option>
						{/each}
					</select>
				</div>

				<div class="grid grid-cols-2 gap-3">
					<div>
						<label for="entry-priority" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Priority Order *</label>
						<input
							id="entry-priority"
							type="number"
							min="1"
							bind:value={entryPriority}
							class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
						/>
					</div>
					<div>
						<label for="entry-weight" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Weight (%) *</label>
						<input
							id="entry-weight"
							type="number"
							min="1"
							max="100"
							bind:value={entryWeight}
							class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
						/>
					</div>
				</div>

				<div>
					<label for="entry-scope" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Scope Type *</label>
					<select
						id="entry-scope"
						bind:value={entryScopeType}
						disabled={!!editingEntry}
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					>
						<option value="global">Global (All Destinations)</option>
						<option value="country">Country-specific</option>
						<option value="operator">Operator-specific</option>
						<option value="prefix">Prefix-specific</option>
					</select>
				</div>

				{#if entryScopeType === 'country'}
					<div>
						<label for="entry-country" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Destination Country *</label>
						<select
							id="entry-country"
							bind:value={entryCountryId}
							disabled={!!editingEntry}
							class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
						>
							<option value="">Select country...</option>
							{#each availableCountries as cnt}
								<option value={cnt.id}>{cnt.name} ({cnt.iso2})</option>
							{/each}
						</select>
					</div>
				{/if}

				{#if entryScopeType === 'operator'}
					<div>
						<label for="entry-operator" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Destination Operator *</label>
						<select
							id="entry-operator"
							bind:value={entryOperatorId}
							disabled={!!editingEntry}
							class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
						>
							<option value="">Select operator...</option>
							{#each availableOperators as op}
								<option value={op.id}>{op.name} ({op.mcc && op.mnc ? `${op.mcc}-${op.mnc}` : 'MNC'})</option>
							{/each}
						</select>
					</div>
				{/if}

				{#if entryScopeType === 'prefix'}
					<div>
						<label for="entry-prefix" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">E.164 Prefix *</label>
						<input
							id="entry-prefix"
							type="text"
							bind:value={entryPrefix}
							disabled={!!editingEntry}
							placeholder="e.g. 1202 or 447"
							class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
						/>
					</div>
				{/if}

				<div>
					<label for="entry-min-margin" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Min Margin Override (%)</label>
					<input
						id="entry-min-margin"
						type="text"
						bind:value={entryMinMargin}
						placeholder="Optional, e.g. 5.00"
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					/>
				</div>

				<div class="flex items-center gap-2 pt-1">
					<input
						id="entry-tested-cov"
						type="checkbox"
						bind:checked={entryAllowTested}
						class="h-4 w-4 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500"
					/>
					<label for="entry-tested-cov" class="text-xs font-medium text-slate-700 dark:text-slate-300">
						Allow tested coverage (in addition to active coverage)
					</label>
				</div>

				<div>
					<label for="entry-audit-reason" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Change Reason / Audit Note</label>
					<input
						id="entry-audit-reason"
						type="text"
						bind:value={entryReason}
						placeholder="Optional note for maker-checker review..."
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					/>
				</div>
			</div>

			<div class="flex justify-end gap-2 pt-2">
				<button
					type="button"
					onclick={() => showEntryModal = false}
					class="rounded-lg border border-slate-200 px-4 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
				>
					Cancel
				</button>
				<button
					type="button"
					disabled={entrySubmitting}
					onclick={handleSaveEntry}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					{entrySubmitting ? 'Saving...' : 'Save Entry'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Action Prompt Modal -->
{#if showReasonModal}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/60 backdrop-blur-xs p-4">
		<div class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900 space-y-4">
			<h2 class="text-lg font-bold text-slate-900 dark:text-white capitalize">
				Confirm Plan {pendingAction}
			</h2>
			<p class="text-xs text-slate-500">
				Please provide a reason for auditing and maker-checker approval review.
			</p>
			<div>
				<label for="action-reason" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Reason</label>
				<textarea
					id="action-reason"
					bind:value={actionReason}
					rows="3"
					placeholder="Enter reason for this action..."
					class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
				></textarea>
			</div>
			<div class="flex justify-end gap-2 pt-2">
				<button
					type="button"
					onclick={() => showReasonModal = false}
					class="rounded-lg border border-slate-200 px-4 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
				>
					Cancel
				</button>
				<button
					type="button"
					disabled={actionSubmitting}
					onclick={handleExecuteAction}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					{actionSubmitting ? 'Submitting...' : 'Confirm'}
				</button>
			</div>
		</div>
	</div>
{/if}
