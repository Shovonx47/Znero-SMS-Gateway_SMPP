<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { Organization } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let orgs = $state<Organization[]>([]);
	let totalCount = $state(0);
	let page = $state(1);
	let pageSize = 15;
	let search = $state('');
	let statusFilter = $state('');
	let typeFilter = $state('');
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	// Modals
	let isCreateModalOpen = $state(false);
	let newOrgName = $state('');
	let newOrgType = $state<'reseller' | 'customer'>('customer');
	let newOrgParentId = $state('');
	let isCreating = $state(false);
	let modalError = $state<string | null>(null);

	// Edit modal
	let isEditModalOpen = $state(false);
	let editingOrg = $state<Organization | null>(null);
	let editName = $state('');
	let isUpdating = $state(false);

	async function loadOrganizations() {
		isLoading = true;
		errorMessage = null;

		try {
			const res = await apiClient.listOrganizations({
				search: search.trim() || undefined,
				status: statusFilter || undefined,
				type: typeFilter || undefined,
				page,
				pageSize
			});
			orgs = res.data.items;
			totalCount = res.data.total;
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to load organizations.';
			}
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadOrganizations();
	});

	function handleSearch(e: SubmitEvent) {
		e.preventDefault();
		page = 1;
		loadOrganizations();
	}

	async function handleCreate(e: SubmitEvent) {
		e.preventDefault();
		if (!newOrgName.trim()) return;

		isCreating = true;
		modalError = null;

		try {
			await apiClient.createOrganization({
				name: newOrgName.trim(),
				type: newOrgType,
				parent_id: newOrgParentId || undefined
			});
			isCreateModalOpen = false;
			newOrgName = '';
			newOrgType = 'customer';
			newOrgParentId = '';
			await loadOrganizations();
		} catch (err) {
			if (err instanceof ApiClientError) {
				modalError = err.message;
			} else {
				modalError = 'Failed to create organization.';
			}
		} finally {
			isCreating = false;
		}
	}

	function openEdit(org: Organization) {
		editingOrg = org;
		editName = org.name;
		modalError = null;
		isEditModalOpen = true;
	}

	async function handleUpdate(e: SubmitEvent) {
		e.preventDefault();
		if (!editingOrg || !editName.trim()) return;

		isUpdating = true;
		modalError = null;

		try {
			await apiClient.updateOrganization(editingOrg.id, editName.trim());
			isEditModalOpen = false;
			editingOrg = null;
			await loadOrganizations();
		} catch (err) {
			if (err instanceof ApiClientError) {
				modalError = err.message;
			} else {
				modalError = 'Failed to update organization name.';
			}
		} finally {
			isUpdating = false;
		}
	}

	async function toggleStatus(org: Organization) {
		const action = org.status === 'active' ? 'suspend' : 'activate';
		if (!confirm(`Are you sure you want to ${action} organization "${org.name}"? ${action === 'suspend' ? 'This will immediately revoke active sessions for all users in this organization.' : ''}`)) {
			return;
		}

		try {
			if (org.status === 'active') {
				await apiClient.suspendOrganization(org.id);
			} else {
				await apiClient.activateOrganization(org.id);
			}
			await loadOrganizations();
		} catch (err) {
			alert(err instanceof Error ? err.message : 'Action failed');
		}
	}
</script>

<svelte:head>
	<title>Organizations - Znero International</title>
</svelte:head>

<div class="space-y-6">
	<!-- Page Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-xl font-bold tracking-tight text-slate-900 dark:text-white">Organization Hierarchy</h1>
			<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
				Tenant scoping model: Platform → Reseller → Customer. Suspending an organization blocks all its users.
			</p>
		</div>

		{#if auth.hasPermission('org.organization.create')}
			<button
				type="button"
				onclick={() => {
					modalError = null;
					isCreateModalOpen = true;
				}}
				class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-3.5 py-2 text-xs font-semibold text-white shadow-xs transition hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-500"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
				</svg>
				New Organization
			</button>
		{/if}
	</div>

	<!-- Filter & Search Toolbar -->
	<form onsubmit={handleSearch} class="flex flex-wrap items-center gap-3 rounded-xl border border-slate-200 bg-white p-4 shadow-2xs dark:border-slate-800 dark:bg-slate-900">
		<div class="flex-1 min-w-[200px]">
			<input
				type="text"
				bind:value={search}
				placeholder="Search by organization name..."
				class="w-full rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
			/>
		</div>

		<select
			bind:value={typeFilter}
			onchange={() => { page = 1; loadOrganizations(); }}
			class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs text-slate-700 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
		>
			<option value="">All Types</option>
			<option value="platform">Platform</option>
			<option value="reseller">Reseller</option>
			<option value="customer">Customer</option>
		</select>

		<select
			bind:value={statusFilter}
			onchange={() => { page = 1; loadOrganizations(); }}
			class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs text-slate-700 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
		>
			<option value="">All Statuses</option>
			<option value="active">Active</option>
			<option value="suspended">Suspended</option>
		</select>

		<button
			type="submit"
			class="rounded-lg bg-slate-100 px-3 py-1.5 text-xs font-semibold text-slate-700 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-200"
		>
			Search
		</button>
	</form>

	<!-- Table / State -->
	{#if isLoading}
		<Loading message="Loading organizations..." />
	{:else if errorMessage}
		<ErrorState title="Error Loading Organizations" message={errorMessage} retry={loadOrganizations} />
	{:else if orgs.length === 0}
		<Empty title="No Organizations Found" description="No organizations match your current search filters." />
	{:else}
		<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-2xs dark:border-slate-800 dark:bg-slate-900">
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50/75 text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/40 dark:text-slate-400">
						<tr>
							<th class="px-5 py-3">Organization</th>
							<th class="px-4 py-3">Type</th>
							<th class="px-4 py-3">Hierarchy Path</th>
							<th class="px-4 py-3">Status</th>
							<th class="px-4 py-3">Created</th>
							<th class="px-5 py-3 text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
						{#each orgs as org}
							<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/30 transition">
								<td class="px-5 py-3.5">
									<div class="font-bold text-slate-900 dark:text-white">{org.name}</div>
									<div class="font-mono text-[10px] text-slate-400">{org.id}</div>
								</td>
								<td class="px-4 py-3.5">
									<span class="inline-flex items-center rounded-md px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider {org.type === 'platform' ? 'bg-purple-100 text-purple-800 dark:bg-purple-900/50 dark:text-purple-300' : org.type === 'reseller' ? 'bg-blue-100 text-blue-800 dark:bg-blue-900/50 dark:text-blue-300' : 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/50 dark:text-emerald-300'}">
										{org.type}
									</span>
								</td>
								<td class="px-4 py-3.5 font-mono text-[10px] text-slate-500 dark:text-slate-400 truncate max-w-[200px]" title={org.path}>
									{org.path}
								</td>
								<td class="px-4 py-3.5">
									<span class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[10px] font-semibold {org.status === 'active' ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300' : 'bg-rose-50 text-rose-700 dark:bg-rose-950/40 dark:text-rose-300'}">
										<span class="h-1.5 w-1.5 rounded-full {org.status === 'active' ? 'bg-emerald-500' : 'bg-rose-500'}"></span>
										{org.status}
									</span>
								</td>
								<td class="px-4 py-3.5 text-slate-500 dark:text-slate-400">
									{new Date(org.created_at).toLocaleDateString()}
								</td>
								<td class="px-5 py-3.5 text-right space-x-2">
									{#if auth.hasPermission('org.organization.edit')}
										<button
											type="button"
											onclick={() => openEdit(org)}
											class="text-indigo-600 hover:text-indigo-500 font-semibold"
										>
											Edit
										</button>
									{/if}

									{#if auth.hasPermission('org.organization.suspend') && org.type !== 'platform'}
										<button
											type="button"
											onclick={() => toggleStatus(org)}
											class="font-semibold {org.status === 'active' ? 'text-rose-600 hover:text-rose-500' : 'text-emerald-600 hover:text-emerald-500'}"
										>
											{org.status === 'active' ? 'Suspend' : 'Activate'}
										</button>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>

			<!-- Pagination -->
			<div class="flex items-center justify-between border-t border-slate-200 px-5 py-3 text-xs text-slate-500 dark:border-slate-800">
				<div>Showing {orgs.length} of {totalCount} organizations</div>
				<div class="flex gap-2">
					<button
						type="button"
						disabled={page <= 1}
						onclick={() => { page--; loadOrganizations(); }}
						class="rounded border border-slate-300 px-2.5 py-1 disabled:opacity-40 hover:bg-slate-50 dark:border-slate-700"
					>
						Previous
					</button>
					<button
						type="button"
						disabled={page * pageSize >= totalCount}
						onclick={() => { page++; loadOrganizations(); }}
						class="rounded border border-slate-300 px-2.5 py-1 disabled:opacity-40 hover:bg-slate-50 dark:border-slate-700"
					>
						Next
					</button>
				</div>
			</div>
		</div>
	{/if}
</div>

<!-- Create Org Modal -->
{#if isCreateModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 backdrop-blur-xs p-4">
		<div class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h3 class="text-base font-bold text-slate-900 dark:text-white">Create Organization</h3>
			<p class="mt-1 text-xs text-slate-500">Add a new reseller or customer tenant into the hierarchy.</p>

			{#if modalError}
				<div class="mt-3 rounded-lg border border-rose-200 bg-rose-50 p-2.5 text-xs text-rose-700 dark:border-rose-900/50 dark:bg-rose-950/40 dark:text-rose-300">
					{modalError}
				</div>
			{/if}

			<form onsubmit={handleCreate} class="mt-4 space-y-4">
				<div>
					<label for="cname" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Organization Name</label>
					<input
						id="cname"
						type="text"
						bind:value={newOrgName}
						required
						placeholder="Acme Telecom"
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div>
					<label for="ctype" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Organization Type</label>
					<select
						id="ctype"
						bind:value={newOrgType}
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						<option value="customer">Customer (Direct client, cannot have child orgs)</option>
						<option value="reseller">Reseller (Can create customer child orgs)</option>
					</select>
				</div>

				{#if auth.tenant?.is_platform_admin}
					<div>
						<label for="cparent" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Parent Organization ID (Optional)</label>
						<input
							id="cparent"
							type="text"
							bind:value={newOrgParentId}
							placeholder="Leave empty for platform root"
							class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
				{/if}

				<div class="mt-6 flex justify-end gap-2">
					<button
						type="button"
						onclick={() => (isCreateModalOpen = false)}
						class="rounded-lg px-3 py-2 text-xs font-semibold text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={isCreating}
						class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
					>
						{isCreating ? 'Creating...' : 'Create Organization'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}

<!-- Edit Org Modal -->
{#if isEditModalOpen && editingOrg}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 backdrop-blur-xs p-4">
		<div class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h3 class="text-base font-bold text-slate-900 dark:text-white">Edit Organization</h3>

			{#if modalError}
				<div class="mt-3 rounded-lg border border-rose-200 bg-rose-50 p-2.5 text-xs text-rose-700 dark:border-rose-900/50 dark:bg-rose-950/40 dark:text-rose-300">
					{modalError}
				</div>
			{/if}

			<form onsubmit={handleUpdate} class="mt-4 space-y-4">
				<div>
					<label for="ename" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Organization Name</label>
					<input
						id="ename"
						type="text"
						bind:value={editName}
						required
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div class="mt-6 flex justify-end gap-2">
					<button
						type="button"
						onclick={() => (isEditModalOpen = false)}
						class="rounded-lg px-3 py-2 text-xs font-semibold text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={isUpdating}
						class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
					>
						{isUpdating ? 'Saving...' : 'Save Changes'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}
