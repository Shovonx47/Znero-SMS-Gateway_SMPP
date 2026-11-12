<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { RoutePlan, RoutePlanStrategy } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let plans = $state<RoutePlan[]>([]);
	let total = $state(0);
	let page = $state(1);
	let pageSize = $state(20);
	let search = $state('');
	let statusFilter = $state('');
	let strategyFilter = $state('');
	let loading = $state(true);
	let errorMessage = $state('');

	// Create modal
	let showCreateModal = $state(false);
	let createName = $state('');
	let createCode = $state('');
	let createDescription = $state('');
	let createStrategy = $state<RoutePlanStrategy>('priority');
	let createNotes = $state('');
	let createSubmitting = $state(false);
	let createError = $state('');

	const isPlatformOrg = $derived(auth.tenant?.actor_org_type === 'platform');
	const canCreate = $derived(isPlatformOrg && (auth.hasPermission('routing.plan.create') || auth.hasPermission('routing.plan.override')));
	const canActivate = $derived(isPlatformOrg && (auth.hasPermission('routing.plan.activate') || auth.hasPermission('routing.plan.override')));

	async function loadPlans() {
		if (auth.tenant?.actor_org_type && !isPlatformOrg) {
			errorMessage = '403 Forbidden: Routing Engine is platform-only and restricted to Platform operators.';
			loading = false;
			return;
		}
		loading = true;
		errorMessage = '';
		try {
			const res = await apiClient.listRoutePlans({
				search: search.trim() || undefined,
				status: statusFilter || undefined,
				strategy: strategyFilter || undefined,
				page,
				page_size: pageSize
			});
			plans = res.data.items || [];
			total = res.data.total;
		} catch (err: any) {
			errorMessage = err.message || 'Failed to load route plans';
		} finally {
			loading = false;
		}
	}

	function handleSearch() {
		page = 1;
		loadPlans();
	}

	async function handleCreatePlan() {
		if (!createName.trim() || !createCode.trim()) {
			createError = 'Name and Code are required.';
			return;
		}
		createSubmitting = true;
		createError = '';
		try {
			await apiClient.createRoutePlan({
				name: createName.trim(),
				code: createCode.trim().toUpperCase(),
				description: createDescription.trim() || undefined,
				strategy: createStrategy,
				notes: createNotes.trim() || undefined
			});
			showCreateModal = false;
			createName = '';
			createCode = '';
			createDescription = '';
			createNotes = '';
			loadPlans();
		} catch (err: any) {
			createError = err.message || 'Failed to create route plan';
		} finally {
			createSubmitting = false;
		}
	}

	function getStatusBadge(status: string) {
		switch (status) {
			case 'active':
				return 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300';
			case 'draft':
				return 'bg-slate-100 text-slate-800 dark:bg-slate-800 dark:text-slate-300';
			case 'deactivated':
				return 'bg-amber-100 text-amber-800 dark:bg-amber-950/60 dark:text-amber-300';
			case 'archived':
				return 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-300';
			default:
				return 'bg-slate-100 text-slate-800 dark:bg-slate-800 dark:text-slate-300';
		}
	}

	function getStrategyBadge(strategy: string) {
		switch (strategy) {
			case 'priority':
				return 'bg-blue-50 text-blue-700 dark:bg-blue-950/50 dark:text-blue-300';
			case 'least_cost':
				return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/50 dark:text-emerald-300';
			case 'weighted':
				return 'bg-purple-50 text-purple-700 dark:bg-purple-950/50 dark:text-purple-300';
			default:
				return 'bg-slate-50 text-slate-700 dark:bg-slate-800 dark:text-slate-300';
		}
	}

	onMount(() => {
		loadPlans();
	});
</script>

<div class="space-y-6">
	{#if auth.tenant?.actor_org_type && !isPlatformOrg}
		<div class="rounded-xl border border-rose-200 bg-rose-50 p-6 text-sm text-rose-800 dark:border-rose-800 dark:bg-rose-950/40 dark:text-rose-200">
			<strong>403 Forbidden — Platform-Only Resource:</strong> Route plans and routing engine are strictly restricted to the Platform organization. Reseller and customer organizations cannot view or manage routing plans.
		</div>
	{:else}
	<!-- Page Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Route Plans</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Manage routing policies, failover strategies, and egress provider connections.
			</p>
		</div>

		{#if canCreate}
			<button
				type="button"
				onclick={() => { showCreateModal = true; createError = ''; }}
				class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white shadow-sm hover:bg-indigo-500 transition focus:outline-none focus:ring-2 focus:ring-indigo-600"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
				</svg>
				<span>Create Route Plan</span>
			</button>
		{/if}
	</div>

	<!-- Search & Filters -->
	<div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-4 shadow-xs sm:flex-row sm:items-center sm:justify-between dark:border-slate-800 dark:bg-slate-900">
		<div class="flex flex-1 flex-col gap-3 sm:flex-row sm:items-center">
			<div class="relative flex-1">
				<input aria-label="Select search"
					type="text"
					bind:value={search}
					onkeydown={(e) => e.key === 'Enter' && handleSearch()}
					placeholder="Search by plan name or code..."
					class="w-full rounded-lg border border-slate-200 bg-slate-50 py-2 pl-9 pr-3 text-sm text-slate-900 placeholder:text-slate-400 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
				/>
				<svg class="absolute left-3 top-2.5 h-4 w-4 text-slate-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
				</svg>
			</div>

			<select aria-label="Filter by status"
				bind:value={statusFilter}
				onchange={handleSearch}
				class="rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
			>
				<option value="">All Statuses</option>
				<option value="draft">Draft</option>
				<option value="active">Active</option>
				<option value="deactivated">Deactivated</option>
				<option value="archived">Archived</option>
			</select>

			<select aria-label="Filter by strategy"
				bind:value={strategyFilter}
				onchange={handleSearch}
				class="rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
			>
				<option value="">All Strategies</option>
				<option value="priority">Priority (Fixed)</option>
				<option value="least_cost">Least Cost</option>
				<option value="weighted">Weighted Split</option>
			</select>
		</div>

		<button
			type="button"
			onclick={handleSearch}
			class="rounded-lg border border-slate-200 px-3 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
		>
			Refresh
		</button>
	</div>

	<!-- Content -->
	{#if loading}
		<Loading message="Loading route plans..." />
	{:else if errorMessage}
		<ErrorState title="Error Loading Plans" message={errorMessage} retry={loadPlans} />
	{:else if plans.length === 0}
		<Empty
			title="No route plans found"
			description="No route plans matched your current filter criteria."
		/>
	{:else}
		<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-xs dark:border-slate-800 dark:bg-slate-900">
			<div class="overflow-x-auto">
				<table class="w-full text-left text-sm text-slate-600 dark:text-slate-400">
					<thead class="border-b border-slate-200 bg-slate-50 text-xs font-semibold uppercase text-slate-500 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-400">
						<tr>
							<th class="px-4 py-3">Code / Name</th>
							<th class="px-4 py-3">Strategy</th>
							<th class="px-4 py-3">Status</th>
							<th class="px-4 py-3">Entries</th>
							<th class="px-4 py-3">Version</th>
							<th class="px-4 py-3">Updated</th>
							<th class="px-4 py-3 text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
						{#each plans as plan}
							<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/40 transition">
								<td class="px-4 py-3">
									<div class="font-mono text-xs font-bold text-slate-900 dark:text-white">
										<a href={`/routing/plans/${plan.id}`} class="hover:text-indigo-600 dark:hover:text-indigo-400 underline decoration-slate-300">
											{plan.code}
										</a>
									</div>
									<div class="text-xs text-slate-500 dark:text-slate-400">{plan.name}</div>
								</td>
								<td class="px-4 py-3">
									<span class="inline-flex rounded-full px-2.5 py-0.5 text-xs font-medium {getStrategyBadge(plan.strategy)}">
										{plan.strategy.toUpperCase()}
									</span>
								</td>
								<td class="px-4 py-3">
									<span class="inline-flex rounded-full px-2.5 py-0.5 text-xs font-medium {getStatusBadge(plan.status)}">
										{plan.status.toUpperCase()}
									</span>
								</td>
								<td class="px-4 py-3 font-mono text-xs">
									{plan.entries_count ?? 0}
								</td>
								<td class="px-4 py-3 font-mono text-xs text-slate-500">
									v{plan.version}
								</td>
								<td class="px-4 py-3 text-xs text-slate-500">
									{new Date(plan.updated_at).toLocaleString()}
								</td>
								<td class="px-4 py-3 text-right">
									<a
										href={`/routing/plans/${plan.id}`}
										class="inline-flex items-center gap-1 rounded-md px-2.5 py-1 text-xs font-semibold text-indigo-600 hover:bg-indigo-50 dark:text-indigo-400 dark:hover:bg-indigo-950/50"
									>
										<span>Manage</span>
										<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
											<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7" />
										</svg>
									</a>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>

			<!-- Pagination -->
			{#if total > pageSize}
				<div class="flex items-center justify-between border-t border-slate-200 px-4 py-3 dark:border-slate-800">
					<div class="text-xs text-slate-500">
						Showing {(page - 1) * pageSize + 1} to {Math.min(page * pageSize, total)} of {total} plans
					</div>
					<div class="flex gap-2">
						<button
							disabled={page <= 1}
							onclick={() => { page--; loadPlans(); }}
							class="rounded-lg border border-slate-200 px-2.5 py-1 text-xs font-semibold text-slate-700 disabled:opacity-40 dark:border-slate-700 dark:text-slate-300"
						>
							Previous
						</button>
						<button
							disabled={page * pageSize >= total}
							onclick={() => { page++; loadPlans(); }}
							class="rounded-lg border border-slate-200 px-2.5 py-1 text-xs font-semibold text-slate-700 disabled:opacity-40 dark:border-slate-700 dark:text-slate-300"
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
{#if showCreateModal}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/60 backdrop-blur-xs p-4">
		<div class="w-full max-w-lg rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900 space-y-4">
			<div class="flex items-center justify-between">
				<h2 class="text-lg font-bold text-slate-900 dark:text-white">Create New Route Plan</h2>
				<button
					type="button"
					onclick={() => showCreateModal = false}
					aria-label="Close modal"
					class="rounded p-1 text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			{#if createError}
				<div class="rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300">
					{createError}
				</div>
			{/if}

			<div class="space-y-3">
				<div>
					<label for="create-plan-code" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Plan Code *</label>
					<input
						id="create-plan-code"
						type="text"
						bind:value={createCode}
						placeholder="e.g. STD-TIER1 or LC-GLOBAL"
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-sm uppercase text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					/>
				</div>

				<div>
					<label for="create-plan-name" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Plan Name *</label>
					<input
						id="create-plan-name"
						type="text"
						bind:value={createName}
						placeholder="e.g. Standard Direct Route Plan"
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					/>
				</div>

				<div>
					<label for="create-plan-strategy" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Routing Strategy *</label>
					<select
						id="create-plan-strategy"
						bind:value={createStrategy}
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					>
						<option value="priority">Priority (Fixed sequence with failover)</option>
						<option value="least_cost">Least Cost (Exact decimal buy-rate sorting)</option>
						<option value="weighted">Weighted Split (Traffic load distribution)</option>
					</select>
				</div>

				<div>
					<label for="create-plan-desc" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Description</label>
					<textarea
						id="create-plan-desc"
						bind:value={createDescription}
						rows="2"
						placeholder="Optional summary of this route plan's intent..."
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					></textarea>
				</div>

				<div>
					<label for="create-plan-notes" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Notes / Changelog</label>
					<input
						id="create-plan-notes"
						type="text"
						bind:value={createNotes}
						placeholder="Optional notes..."
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					/>
				</div>
			</div>

			<div class="flex justify-end gap-2 pt-2">
				<button
					type="button"
					onclick={() => showCreateModal = false}
					class="rounded-lg border border-slate-200 px-4 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
				>
					Cancel
				</button>
				<button
					type="button"
					disabled={createSubmitting}
					onclick={handleCreatePlan}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					{createSubmitting ? 'Creating...' : 'Create Draft Plan'}
				</button>
			</div>
		</div>
	</div>
{/if}
