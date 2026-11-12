<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { Role, User } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let users = $state<User[]>([]);
	let totalCount = $state(0);
	let page = $state(1);
	let pageSize = 15;
	let search = $state('');
	let statusFilter = $state('');
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	// Roles list for modals
	let availableRoles = $state<Role[]>([]);

	// Invite modal
	let isInviteModalOpen = $state(false);
	let inviteEmail = $state('');
	let inviteOrgId = $state('');
	let selectedRoleIds = $state<string[]>([]);
	let isInviting = $state(false);
	let modalError = $state<string | null>(null);

	// Roles edit modal
	let isRoleModalOpen = $state(false);
	let targetUser = $state<User | null>(null);
	let editRoleIds = $state<string[]>([]);
	let isSavingRoles = $state(false);

	async function loadUsers() {
		isLoading = true;
		errorMessage = null;

		try {
			const res = await apiClient.listUsers({
				search: search.trim() || undefined,
				status: statusFilter || undefined,
				page,
				pageSize
			});
			users = res.data.items || [];
			totalCount = res.data.total;
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to load users.';
			}
		} finally {
			isLoading = false;
		}
	}

	async function loadRoles() {
		try {
			const res = await apiClient.listRoles();
			availableRoles = res.data;
		} catch {
			// silent fallback
		}
	}

	onMount(() => {
		loadUsers();
		loadRoles();
	});

	function handleSearch(e: SubmitEvent) {
		e.preventDefault();
		page = 1;
		loadUsers();
	}

	async function handleInvite(e: SubmitEvent) {
		e.preventDefault();
		if (!inviteEmail.trim() || selectedRoleIds.length === 0) {
			modalError = 'Email and at least one role are required.';
			return;
		}

		isInviting = true;
		modalError = null;

		try {
			await apiClient.inviteUser({
				email: inviteEmail.trim(),
				organization_id: inviteOrgId.trim() || undefined,
				role_ids: selectedRoleIds
			});
			isInviteModalOpen = false;
			inviteEmail = '';
			inviteOrgId = '';
			selectedRoleIds = [];
			await loadUsers();
		} catch (err) {
			if (err instanceof ApiClientError) {
				modalError = err.message;
			} else {
				modalError = 'Failed to invite user.';
			}
		} finally {
			isInviting = false;
		}
	}

	function openRoleModal(user: User) {
		targetUser = user;
		editRoleIds = user.role_ids ? [...user.role_ids] : [];
		modalError = null;
		isRoleModalOpen = true;
	}

	async function handleSaveRoles(e: SubmitEvent) {
		e.preventDefault();
		if (!targetUser) return;

		isSavingRoles = true;
		modalError = null;

		try {
			await apiClient.updateUserRoles(targetUser.id, editRoleIds);
			isRoleModalOpen = false;
			targetUser = null;
			await loadUsers();
		} catch (err) {
			if (err instanceof ApiClientError) {
				modalError = err.message;
			} else {
				modalError = 'Failed to update user roles.';
			}
		} finally {
			isSavingRoles = false;
		}
	}

	async function toggleUserStatus(user: User) {
		const action = user.status === 'active' ? 'suspend' : 'activate';
		if (!confirm(`Are you sure you want to ${action} user "${user.email}"? ${action === 'suspend' ? 'This immediately revokes all of their active sessions.' : ''}`)) {
			return;
		}

		try {
			if (user.status === 'active') {
				await apiClient.suspendUser(user.id, 'Manual administrative suspension');
			} else {
				await apiClient.activateUser(user.id);
			}
			await loadUsers();
		} catch (err) {
			alert(err instanceof Error ? err.message : 'Action failed');
		}
	}
</script>

<svelte:head>
	<title>User Accounts & Invitations - Znero International</title>
</svelte:head>

<div class="space-y-6">
	<!-- Page Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-xl font-bold tracking-tight text-slate-900 dark:text-white">User Accounts</h1>
			<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
				Manage users, invite new team members, assign RBAC roles, and enforce session security.
			</p>
		</div>

		{#if auth.hasPermission('user.account.create')}
			<button
				type="button"
				onclick={() => {
					modalError = null;
					selectedRoleIds = [];
					isInviteModalOpen = true;
				}}
				class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-3.5 py-2 text-xs font-semibold text-white shadow-xs transition hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-500"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M18 9v3m0 0v3m0-3h3m-3 0h-3m-2-5a4 4 0 11-8 0 4 4 0 018 0zM3 20a6 6 0 0112 0v1H3v-1z" />
				</svg>
				Invite User
			</button>
		{/if}
	</div>

	<!-- Filters -->
	<form onsubmit={handleSearch} class="flex flex-wrap items-center gap-3 rounded-xl border border-slate-200 bg-white p-4 shadow-2xs dark:border-slate-800 dark:bg-slate-900">
		<div class="flex-1 min-w-[200px]">
			<input
				type="text"
				bind:value={search}
				placeholder="Search by email..."
				class="w-full rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
			/>
		</div>

		<select aria-label="Filter by status"
			bind:value={statusFilter}
			onchange={() => { page = 1; loadUsers(); }}
			class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs text-slate-700 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
		>
			<option value="">All Statuses</option>
			<option value="active">Active</option>
			<option value="invited">Invited</option>
			<option value="suspended">Suspended</option>
		</select>

		<button
			type="submit"
			class="rounded-lg bg-slate-100 px-3 py-1.5 text-xs font-semibold text-slate-700 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-200"
		>
			Search
		</button>
	</form>

	<!-- Table -->
	{#if isLoading}
		<Loading message="Loading user directory..." />
	{:else if errorMessage}
		<ErrorState title="Error Loading Users" message={errorMessage} retry={loadUsers} />
	{:else if users.length === 0}
		<Empty title="No Users Found" description="No user accounts match your search filters." />
	{:else}
		<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-2xs dark:border-slate-800 dark:bg-slate-900">
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50/75 text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/40 dark:text-slate-400">
						<tr>
							<th class="px-5 py-3">User Email</th>
							<th class="px-4 py-3">Status</th>
							<th class="px-4 py-3">2FA</th>
							<th class="px-4 py-3">Assigned Roles</th>
							<th class="px-4 py-3">Organization ID</th>
							<th class="px-5 py-3 text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
						{#each users as user}
							<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/30 transition">
								<td class="px-5 py-3.5">
									<div class="font-bold text-slate-900 dark:text-white">{user.email}</div>
									<div class="font-mono text-[10px] text-slate-400">{user.id}</div>
								</td>
								<td class="px-4 py-3.5">
									<span class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[10px] font-semibold {user.status === 'active' ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300' : user.status === 'invited' ? 'bg-blue-50 text-blue-700 dark:bg-blue-950/40 dark:text-blue-300' : 'bg-rose-50 text-rose-700 dark:bg-rose-950/40 dark:text-rose-300'}">
										<span class="h-1.5 w-1.5 rounded-full {user.status === 'active' ? 'bg-emerald-500' : user.status === 'invited' ? 'bg-blue-500' : 'bg-rose-500'}"></span>
										{user.status}
									</span>
								</td>
								<td class="px-4 py-3.5">
									{#if user.mfa_enabled}
										<span class="rounded bg-purple-50 px-2 py-0.5 text-[10px] font-bold text-purple-700 dark:bg-purple-950/40 dark:text-purple-300">
											TOTP ON
										</span>
									{:else}
										<span class="text-slate-400 text-[10px]">Disabled</span>
									{/if}
								</td>
								<td class="px-4 py-3.5">
									<div class="flex flex-wrap gap-1">
										{#if user.roles && user.roles.length > 0}
											{#each user.roles as role}
												<span class="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] font-medium text-slate-700 dark:bg-slate-800 dark:text-slate-300">
													{role}
												</span>
											{/each}
										{:else}
											<span class="text-slate-400 text-[11px]">No roles assigned</span>
										{/if}
									</div>
								</td>
								<td class="px-4 py-3.5 font-mono text-[10px] text-slate-500 truncate max-w-[120px]" title={user.organization_id}>
									{user.organization_id}
								</td>
								<td class="px-5 py-3.5 text-right space-x-2">
									{#if auth.hasPermission('role.role.assign')}
										<button
											type="button"
											onclick={() => openRoleModal(user)}
											class="text-indigo-600 hover:text-indigo-500 font-semibold"
										>
											Roles
										</button>
									{/if}

									{#if auth.hasPermission('user.account.suspend') && user.id !== auth.user?.id}
										<button
											type="button"
											onclick={() => toggleUserStatus(user)}
											class="font-semibold {user.status === 'active' ? 'text-rose-600 hover:text-rose-500' : 'text-emerald-600 hover:text-emerald-500'}"
										>
											{user.status === 'active' ? 'Suspend' : 'Activate'}
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
				<div>Showing {users.length} of {totalCount} users</div>
				<div class="flex gap-2">
					<button
						type="button"
						disabled={page <= 1}
						onclick={() => { page--; loadUsers(); }}
						class="rounded border border-slate-300 px-2.5 py-1 disabled:opacity-40 hover:bg-slate-50 dark:border-slate-700"
					>
						Previous
					</button>
					<button
						type="button"
						disabled={page * pageSize >= totalCount}
						onclick={() => { page++; loadUsers(); }}
						class="rounded border border-slate-300 px-2.5 py-1 disabled:opacity-40 hover:bg-slate-50 dark:border-slate-700"
					>
						Next
					</button>
				</div>
			</div>
		</div>
	{/if}
</div>

<!-- Invite User Modal -->
{#if isInviteModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 backdrop-blur-xs p-4">
		<div class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h3 class="text-base font-bold text-slate-900 dark:text-white">Invite User</h3>
			<p class="mt-1 text-xs text-slate-500">The invited user will receive a single-use link to set their password.</p>

			{#if modalError}
				<div class="mt-3 rounded-lg border border-rose-200 bg-rose-50 p-2.5 text-xs text-rose-700 dark:border-rose-900/50 dark:bg-rose-950/40 dark:text-rose-300">
					{modalError}
				</div>
			{/if}

			<form onsubmit={handleInvite} class="mt-4 space-y-4">
				<div>
					<label for="invEmail" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Email Address</label>
					<input
						id="invEmail"
						type="email"
						bind:value={inviteEmail}
						required
						placeholder="user@example.com"
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				{#if auth.tenant?.is_platform_admin}
					<div>
						<label for="invOrg" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Target Organization ID (Optional)</label>
						<input
							id="invOrg"
							type="text"
							bind:value={inviteOrgId}
							placeholder="Leave empty for your current organization"
							class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
				{/if}

				<div>
					<span class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-2">Assign Roles</span>
					<div class="space-y-1.5 max-h-40 overflow-y-auto rounded-lg border border-slate-200 p-2 dark:border-slate-800">
						{#each availableRoles as r}
							<label class="flex items-center gap-2 text-xs text-slate-700 dark:text-slate-300 cursor-pointer">
								<input
									type="checkbox"
									value={r.id}
									checked={selectedRoleIds.includes(r.id)}
									onchange={(e) => {
										const target = e.currentTarget;
										if (target.checked) {
											selectedRoleIds = [...selectedRoleIds, r.id];
										} else {
											selectedRoleIds = selectedRoleIds.filter(id => id !== r.id);
										}
									}}
									class="rounded border-slate-300 text-indigo-600 focus:ring-indigo-500"
								/>
								<span class="font-medium">{r.name}</span>
								{#if r.is_system}
									<span class="text-[10px] text-slate-400">(System)</span>
								{/if}
							</label>
						{/each}
					</div>
				</div>

				<div class="mt-6 flex justify-end gap-2">
					<button
						type="button"
						onclick={() => (isInviteModalOpen = false)}
						class="rounded-lg px-3 py-2 text-xs font-semibold text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={isInviting}
						class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
					>
						{isInviting ? 'Inviting...' : 'Send Invitation'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}

<!-- Edit Roles Modal -->
{#if isRoleModalOpen && targetUser}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 backdrop-blur-xs p-4">
		<div class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h3 class="text-base font-bold text-slate-900 dark:text-white">Edit User Roles</h3>
			<p class="mt-1 text-xs text-slate-500">Configuring roles for <span class="font-semibold">{targetUser.email}</span></p>

			{#if modalError}
				<div class="mt-3 rounded-lg border border-rose-200 bg-rose-50 p-2.5 text-xs text-rose-700 dark:border-rose-900/50 dark:bg-rose-950/40 dark:text-rose-300">
					{modalError}
				</div>
			{/if}

			<form onsubmit={handleSaveRoles} class="mt-4 space-y-4">
				<div class="space-y-2 max-h-52 overflow-y-auto rounded-lg border border-slate-200 p-3 dark:border-slate-800">
					{#each availableRoles as r}
						<label class="flex items-start gap-2.5 text-xs text-slate-700 dark:text-slate-300 cursor-pointer">
							<input
								type="checkbox"
								value={r.id}
								checked={editRoleIds.includes(r.id)}
								onchange={(e) => {
									const target = e.currentTarget;
									if (target.checked) {
										editRoleIds = [...editRoleIds, r.id];
									} else {
										editRoleIds = editRoleIds.filter(id => id !== r.id);
									}
								}}
								class="mt-0.5 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500"
							/>
							<div>
								<div class="font-semibold text-slate-800 dark:text-white">{r.name}</div>
								<div class="text-[11px] text-slate-500 dark:text-slate-400">{r.description}</div>
							</div>
						</label>
					{/each}
				</div>

				<div class="mt-6 flex justify-end gap-2">
					<button
						type="button"
						onclick={() => (isRoleModalOpen = false)}
						class="rounded-lg px-3 py-2 text-xs font-semibold text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={isSavingRoles}
						class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
					>
						{isSavingRoles ? 'Saving...' : 'Save Roles'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}
