<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { Permission, Role } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let roles = $state<Role[]>([]);
	let allPermissions = $state<Permission[]>([]);
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	// Modals
	let isCreateModalOpen = $state(false);
	let roleName = $state('');
	let roleDescription = $state('');
	let selectedPermissions = $state<string[]>([]);
	let isSaving = $state(false);
	let modalError = $state<string | null>(null);

	// Edit mode
	let editingRole = $state<Role | null>(null);

	// Clone mode
	let isCloneModalOpen = $state(false);
	let cloneSourceRole = $state<Role | null>(null);
	let cloneName = $state('');

	// Group permissions by module
	let permissionsByModule = $derived.by(() => {
		const groups: Record<string, Permission[]> = {};
		for (const p of allPermissions) {
			if (!groups[p.module]) {
				groups[p.module] = [];
			}
			groups[p.module].push(p);
		}
		return groups;
	});

	async function loadData() {
		isLoading = true;
		errorMessage = null;

		try {
			const [rolesRes, permsRes] = await Promise.all([
				apiClient.listRoles(),
				apiClient.listPermissions()
			]);
			roles = rolesRes.data;
			allPermissions = permsRes.data;
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to load roles and permissions catalog.';
			}
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadData();
	});

	function openCreate() {
		editingRole = null;
		roleName = '';
		roleDescription = '';
		selectedPermissions = [];
		modalError = null;
		isCreateModalOpen = true;
	}

	function openEdit(r: Role) {
		editingRole = r;
		roleName = r.name;
		roleDescription = r.description;
		selectedPermissions = r.permissions ? [...r.permissions] : [];
		modalError = null;
		isCreateModalOpen = true;
	}

	function openClone(r: Role) {
		cloneSourceRole = r;
		cloneName = `${r.name} (Copy)`;
		modalError = null;
		isCloneModalOpen = true;
	}

	async function handleSaveRole(e: SubmitEvent) {
		e.preventDefault();
		if (!roleName.trim()) return;

		isSaving = true;
		modalError = null;

		try {
			if (editingRole) {
				await apiClient.updateRole(editingRole.id, {
					name: roleName.trim(),
					description: roleDescription.trim(),
					permissions: selectedPermissions
				});
			} else {
				await apiClient.createRole({
					name: roleName.trim(),
					description: roleDescription.trim(),
					permissions: selectedPermissions
				});
			}
			isCreateModalOpen = false;
			await loadData();
		} catch (err) {
			if (err instanceof ApiClientError) {
				modalError = err.message;
			} else {
				modalError = 'Failed to save role.';
			}
		} finally {
			isSaving = false;
		}
	}

	async function handleClone(e: SubmitEvent) {
		e.preventDefault();
		if (!cloneSourceRole || !cloneName.trim()) return;

		isSaving = true;
		modalError = null;

		try {
			await apiClient.cloneRole(cloneSourceRole.id, cloneName.trim());
			isCloneModalOpen = false;
			await loadData();
		} catch (err) {
			if (err instanceof ApiClientError) {
				modalError = err.message;
			} else {
				modalError = 'Failed to clone role.';
			}
		} finally {
			isSaving = false;
		}
	}

	async function handleDelete(r: Role) {
		if (!confirm(`Are you sure you want to delete role "${r.name}"?`)) return;

		try {
			await apiClient.deleteRole(r.id);
			await loadData();
		} catch (err) {
			alert(err instanceof Error ? err.message : 'Delete failed');
		}
	}

	function toggleModuleAll(mod: string, checked: boolean) {
		const modPerms = permissionsByModule[mod] || [];
		const codes = modPerms.map((p) => p.code);
		if (checked) {
			const set = new Set([...selectedPermissions, ...codes]);
			selectedPermissions = Array.from(set);
		} else {
			selectedPermissions = selectedPermissions.filter((c) => !codes.includes(c));
		}
	}
</script>

<svelte:head>
	<title>Roles & Permissions (RBAC) - Znero International</title>
</svelte:head>

<div class="space-y-6">
	<!-- Page Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-xl font-bold tracking-tight text-slate-900 dark:text-white">Role-Based Access Control (RBAC)</h1>
			<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
				Default deny security model. Permissions follow module.resource.action format.
			</p>
		</div>

		{#if auth.hasPermission('role.role.create')}
			<button
				type="button"
				onclick={openCreate}
				class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-3.5 py-2 text-xs font-semibold text-white shadow-xs transition hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-500"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
				</svg>
				New Custom Role
			</button>
		{/if}
	</div>

	<!-- Roles List -->
	{#if isLoading}
		<Loading message="Loading RBAC roles and permissions catalog..." />
	{:else if errorMessage}
		<ErrorState title="Error Loading RBAC" message={errorMessage} retry={loadData} />
	{:else if roles.length === 0}
		<Empty title="No Roles Found" description="No roles have been configured for your organization." />
	{:else}
		<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-5">
			{#each roles as r}
				<div class="flex flex-col justify-between rounded-xl border border-slate-200 bg-white p-5 shadow-2xs dark:border-slate-800 dark:bg-slate-900">
					<div>
						<div class="flex items-center justify-between">
							<h3 class="font-bold text-slate-900 dark:text-white text-sm">{r.name}</h3>
							{#if r.is_system}
								<span class="rounded bg-indigo-50 px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider text-indigo-700 dark:bg-indigo-950/40 dark:text-indigo-300">
									System
								</span>
							{:else}
								<span class="rounded bg-slate-100 px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider text-slate-600 dark:bg-slate-800 dark:text-slate-300">
									Custom
								</span>
							{/if}
						</div>

						<p class="mt-2 text-xs text-slate-500 dark:text-slate-400 min-h-[32px] line-clamp-2">
							{r.description || 'No description provided.'}
						</p>

						<div class="mt-4 border-t border-slate-100 pt-3 dark:border-slate-800">
							<div class="flex items-center justify-between text-xs">
								<span class="text-slate-500">Granted Permissions:</span>
								<span class="font-semibold text-slate-800 dark:text-slate-200">{r.permissions?.length || 0}</span>
							</div>

							<div class="mt-2.5 flex flex-wrap gap-1 max-h-24 overflow-y-auto">
								{#each (r.permissions || []).slice(0, 10) as perm}
									<span class="rounded bg-slate-50 px-1.5 py-0.5 font-mono text-[10px] text-slate-600 dark:bg-slate-800/80 dark:text-slate-300">
										{perm}
									</span>
								{/each}
								{#if (r.permissions?.length || 0) > 10}
									<span class="text-[10px] text-slate-400 self-center">
										+{(r.permissions?.length || 0) - 10} more
									</span>
								{/if}
							</div>
						</div>
					</div>

					<div class="mt-5 flex items-center justify-end gap-2 border-t border-slate-100 pt-3 text-xs dark:border-slate-800">
						{#if auth.hasPermission('role.role.create')}
							<button
								type="button"
								onclick={() => openClone(r)}
								class="text-slate-600 hover:text-slate-900 font-semibold dark:text-slate-400 dark:hover:text-white"
							>
								Clone
							</button>
						{/if}

						{#if !r.is_system}
							{#if auth.hasPermission('role.role.edit')}
								<button
									type="button"
									onclick={() => openEdit(r)}
									class="text-indigo-600 hover:text-indigo-500 font-semibold"
								>
									Edit
								</button>
							{/if}

							{#if auth.hasPermission('role.role.delete')}
								<button
									type="button"
									onclick={() => handleDelete(r)}
									class="text-rose-600 hover:text-rose-500 font-semibold"
								>
									Delete
								</button>
							{/if}
						{/if}
					</div>
				</div>
			{/each}
		</div>
	{/if}
</div>

<!-- Create / Edit Role Modal -->
{#if isCreateModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 backdrop-blur-xs p-4">
		<div class="w-full max-w-2xl rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900 max-h-[90vh] flex flex-col">
			<div class="flex items-center justify-between border-b border-slate-200 pb-3 dark:border-slate-800">
				<h3 class="text-base font-bold text-slate-900 dark:text-white">
					{editingRole ? `Edit Role: ${editingRole.name}` : 'Create Custom Role'}
				</h3>
				<button
					type="button"
					onclick={() => (isCreateModalOpen = false)}
					class="text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
				>
					✕
				</button>
			</div>

			{#if modalError}
				<div class="mt-3 rounded-lg border border-rose-200 bg-rose-50 p-2.5 text-xs text-rose-700 dark:border-rose-900/50 dark:bg-rose-950/40 dark:text-rose-300">
					{modalError}
				</div>
			{/if}

			<form onsubmit={handleSaveRole} class="mt-4 flex-1 overflow-y-auto space-y-4 pr-1">
				<div>
					<label for="rname" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Role Name</label>
					<input
						id="rname"
						type="text"
						bind:value={roleName}
						required
						placeholder="e.g. Operations Specialist"
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div>
					<label for="rdesc" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Description</label>
					<input
						id="rdesc"
						type="text"
						bind:value={roleDescription}
						placeholder="Brief summary of duties and access permissions"
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<!-- Permission Matrix by Module -->
				<div>
					<div class="flex items-center justify-between mb-2">
						<span class="block text-xs font-bold uppercase tracking-wider text-slate-600 dark:text-slate-300">
							Assign Permissions ({selectedPermissions.length} selected)
						</span>
					</div>

					<div class="space-y-4 rounded-xl border border-slate-200 bg-slate-50/50 p-4 dark:border-slate-800 dark:bg-slate-800/40">
						{#each Object.entries(permissionsByModule) as [mod, perms]}
							<div class="rounded-lg border border-slate-200 bg-white p-3 dark:border-slate-700 dark:bg-slate-900">
								<div class="flex items-center justify-between border-b border-slate-100 pb-2 mb-2 dark:border-slate-800">
									<span class="font-bold uppercase tracking-wider text-xs text-indigo-600 dark:text-indigo-400">
										Module: {mod}
									</span>
									<button
										type="button"
										onclick={() => {
											const allSelected = perms.every(p => selectedPermissions.includes(p.code));
											toggleModuleAll(mod, !allSelected);
										}}
										class="text-[11px] font-semibold text-slate-500 hover:text-slate-800 dark:hover:text-slate-200"
									>
										{perms.every(p => selectedPermissions.includes(p.code)) ? 'Deselect All' : 'Select All'}
									</button>
								</div>

								<div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
									{#each perms as p}
										<label class="flex items-start gap-2 text-xs cursor-pointer hover:bg-slate-50 dark:hover:bg-slate-800/50 p-1.5 rounded">
											<input
												type="checkbox"
												value={p.code}
												checked={selectedPermissions.includes(p.code)}
												onchange={(e) => {
													const target = e.currentTarget;
													if (target.checked) {
														selectedPermissions = [...selectedPermissions, p.code];
													} else {
														selectedPermissions = selectedPermissions.filter(c => c !== p.code);
													}
												}}
												class="mt-0.5 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500"
											/>
											<div>
												<div class="font-mono text-[11px] font-semibold text-slate-800 dark:text-slate-200">{p.code}</div>
												<div class="text-[10px] text-slate-500 dark:text-slate-400">{p.description}</div>
											</div>
										</label>
									{/each}
								</div>
							</div>
						{/each}
					</div>
				</div>

				<div class="sticky bottom-0 bg-white dark:bg-slate-900 pt-3 flex justify-end gap-2 border-t border-slate-200 dark:border-slate-800">
					<button
						type="button"
						onclick={() => (isCreateModalOpen = false)}
						class="rounded-lg px-4 py-2 text-xs font-semibold text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={isSaving}
						class="rounded-lg bg-indigo-600 px-5 py-2 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
					>
						{isSaving ? 'Saving...' : editingRole ? 'Update Role' : 'Create Role'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}

<!-- Clone Role Modal -->
{#if isCloneModalOpen && cloneSourceRole}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 backdrop-blur-xs p-4">
		<div class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h3 class="text-base font-bold text-slate-900 dark:text-white">Clone Role</h3>
			<p class="mt-1 text-xs text-slate-500">
				Cloning from <span class="font-semibold">{cloneSourceRole.name}</span> ({cloneSourceRole.permissions?.length || 0} permissions).
			</p>

			{#if modalError}
				<div class="mt-3 rounded-lg border border-rose-200 bg-rose-50 p-2.5 text-xs text-rose-700 dark:border-rose-900/50 dark:bg-rose-950/40 dark:text-rose-300">
					{modalError}
				</div>
			{/if}

			<form onsubmit={handleClone} class="mt-4 space-y-4">
				<div>
					<label for="clonename" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">New Role Name</label>
					<input
						id="clonename"
						type="text"
						bind:value={cloneName}
						required
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div class="mt-6 flex justify-end gap-2">
					<button
						type="button"
						onclick={() => (isCloneModalOpen = false)}
						class="rounded-lg px-3 py-2 text-xs font-semibold text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={isSaving}
						class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
					>
						{isSaving ? 'Cloning...' : 'Clone Role'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}
