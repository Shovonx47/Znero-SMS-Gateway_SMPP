<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import type { AuditEntry } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let logs = $state<AuditEntry[]>([]);
	let totalCount = $state(0);
	let page = $state(1);
	let pageSize = 20;

	let moduleFilter = $state('');
	let actionFilter = $state('');
	let entityTypeFilter = $state('');
	let search = $state('');

	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	// Detail / Diff modal
	let isModalOpen = $state(false);
	let selectedEntry = $state<AuditEntry | null>(null);

	async function loadLogs() {
		isLoading = true;
		errorMessage = null;

		try {
			const res = await apiClient.listAuditLogs({
				module: moduleFilter || undefined,
				action: actionFilter.trim() || undefined,
				entityType: entityTypeFilter || undefined,
				search: search.trim() || undefined,
				page,
				pageSize
			});
			logs = res.data.items;
			totalCount = res.data.total_count;
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to load audit logs.';
			}
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadLogs();
	});

	function handleSearch(e: SubmitEvent) {
		e.preventDefault();
		page = 1;
		loadLogs();
	}

	function openDetail(entry: AuditEntry) {
		selectedEntry = entry;
		isModalOpen = true;
	}

	function formatJSON(val: unknown): string {
		if (val === undefined || val === null) return 'None';
		if (typeof val === 'string') {
			try {
				const parsed = JSON.parse(val);
				return JSON.stringify(parsed, null, 2);
			} catch {
				return val;
			}
		}
		return JSON.stringify(val, null, 2);
	}
</script>

<svelte:head>
	<title>Audit Logs - Znero International</title>
</svelte:head>

<div class="space-y-6">
	<!-- Page Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-xl font-bold tracking-tight text-slate-900 dark:text-white">Immutable Audit Trail</h1>
			<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
				Tamper-evident system activity log. Database triggers strictly prohibit UPDATE, DELETE, and TRUNCATE operations.
			</p>
		</div>

		<div class="flex items-center gap-2">
			<span class="inline-flex items-center gap-1.5 rounded-full bg-emerald-50 px-2.5 py-1 text-xs font-semibold text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300">
				<span class="h-1.5 w-1.5 rounded-full bg-emerald-500"></span>
				Append-Only Ledger Active
			</span>
		</div>
	</div>

	<!-- Filter & Search Toolbar -->
	<form onsubmit={handleSearch} class="flex flex-wrap items-center gap-3 rounded-xl border border-slate-200 bg-white p-4 shadow-2xs dark:border-slate-800 dark:bg-slate-900">
		<div class="flex-1 min-w-[200px]">
			<input
				type="text"
				bind:value={search}
				placeholder="Search entity ID, reason, or actor..."
				class="w-full rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
			/>
		</div>

		<select
			bind:value={moduleFilter}
			onchange={() => { page = 1; loadLogs(); }}
			class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs text-slate-700 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
		>
			<option value="">All Modules</option>
			<option value="auth">Auth</option>
			<option value="org">Organization</option>
			<option value="user">User</option>
			<option value="role">Role / RBAC</option>
			<option value="system">System</option>
		</select>

		<select
			bind:value={entityTypeFilter}
			onchange={() => { page = 1; loadLogs(); }}
			class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs text-slate-700 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
		>
			<option value="">All Entity Types</option>
			<option value="user">User</option>
			<option value="organization">Organization</option>
			<option value="role">Role</option>
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
		<Loading message="Loading audit trail entries..." />
	{:else if errorMessage}
		<ErrorState title="Error Loading Audit Trail" message={errorMessage} retry={loadLogs} />
	{:else if logs.length === 0}
		<Empty title="No Audit Records Found" description="No audit records match your current filter parameters." />
	{:else}
		<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-2xs dark:border-slate-800 dark:bg-slate-900">
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50/75 text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/40 dark:text-slate-400">
						<tr>
							<th class="px-5 py-3">Timestamp</th>
							<th class="px-4 py-3">Action</th>
							<th class="px-4 py-3">Module</th>
							<th class="px-4 py-3">Entity</th>
							<th class="px-4 py-3">IP / Request</th>
							<th class="px-4 py-3">Reason / Details</th>
							<th class="px-5 py-3 text-right">Payload</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
						{#each logs as log}
							<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/30 transition">
								<td class="px-5 py-3 font-mono text-[11px] text-slate-500 whitespace-nowrap">
									{new Date(log.timestamp).toLocaleString()}
								</td>
								<td class="px-4 py-3">
									<span class="inline-flex items-center rounded-md bg-indigo-50 px-2 py-0.5 font-mono text-[11px] font-bold text-indigo-700 dark:bg-indigo-950/40 dark:text-indigo-300">
										{log.action}
									</span>
								</td>
								<td class="px-4 py-3 uppercase text-[10px] font-bold text-slate-500 dark:text-slate-400">
									{log.module}
								</td>
								<td class="px-4 py-3">
									<div class="font-semibold text-slate-800 dark:text-slate-200">{log.entity_type}</div>
									<div class="font-mono text-[10px] text-slate-400 truncate max-w-[120px]" title={log.entity_id}>{log.entity_id}</div>
								</td>
								<td class="px-4 py-3 font-mono text-[10px] text-slate-500">
									<div>{log.ip_address || '-'}</div>
									<div class="text-[9px] text-slate-400 truncate max-w-[100px]" title={log.request_id}>{log.request_id}</div>
								</td>
								<td class="px-4 py-3 text-slate-600 dark:text-slate-300 max-w-[240px] truncate" title={log.reason}>
									{log.reason || '-'}
								</td>
								<td class="px-5 py-3 text-right">
									<button
										type="button"
										onclick={() => openDetail(log)}
										class="font-semibold text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
									>
										View Diff
									</button>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>

			<!-- Pagination -->
			<div class="flex items-center justify-between border-t border-slate-200 px-5 py-3 text-xs text-slate-500 dark:border-slate-800">
				<div>Showing {logs.length} of {totalCount} records</div>
				<div class="flex gap-2">
					<button
						type="button"
						disabled={page <= 1}
						onclick={() => { page--; loadLogs(); }}
						class="rounded border border-slate-300 px-2.5 py-1 disabled:opacity-40 hover:bg-slate-50 dark:border-slate-700"
					>
						Previous
					</button>
					<button
						type="button"
						disabled={page * pageSize >= totalCount}
						onclick={() => { page++; loadLogs(); }}
						class="rounded border border-slate-300 px-2.5 py-1 disabled:opacity-40 hover:bg-slate-50 dark:border-slate-700"
					>
						Next
					</button>
				</div>
			</div>
		</div>
	{/if}
</div>

<!-- Payload / Diff Modal -->
{#if isModalOpen && selectedEntry}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 backdrop-blur-xs p-4">
		<div class="w-full max-w-3xl rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900 max-h-[85vh] flex flex-col">
			<div class="flex items-center justify-between border-b border-slate-200 pb-3 dark:border-slate-800">
				<div>
					<h3 class="text-base font-bold text-slate-900 dark:text-white">Audit Entry Diff Inspection</h3>
					<p class="text-xs font-mono text-indigo-600 dark:text-indigo-400 mt-0.5">{selectedEntry.action} (ID: {selectedEntry.id})</p>
				</div>
				<button
					type="button"
					onclick={() => (isModalOpen = false)}
					class="text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
				>
					✕
				</button>
			</div>

			<div class="mt-4 flex-1 overflow-y-auto space-y-4">
				<div class="grid grid-cols-2 gap-4 text-xs">
					<div class="rounded-lg bg-slate-50 p-3 dark:bg-slate-800/40">
						<span class="text-slate-400">Actor User:</span>
						<div class="font-mono mt-0.5">{selectedEntry.actor_user_id || 'System / Anonymous'}</div>
					</div>
					<div class="rounded-lg bg-slate-50 p-3 dark:bg-slate-800/40">
						<span class="text-slate-400">Actor Organization:</span>
						<div class="font-mono mt-0.5">{selectedEntry.actor_org_id || 'None'}</div>
					</div>
				</div>

				<div class="grid grid-cols-1 md:grid-cols-2 gap-4">
					<!-- Before Value -->
					<div>
						<div class="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5">Before Value</div>
						<pre class="rounded-lg border border-slate-200 bg-slate-50 p-3 font-mono text-[11px] text-slate-800 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-200 overflow-x-auto max-h-64">{formatJSON(selectedEntry.before_value)}</pre>
					</div>

					<!-- After Value -->
					<div>
						<div class="text-xs font-bold uppercase tracking-wider text-slate-500 mb-1.5">After Value</div>
						<pre class="rounded-lg border border-slate-200 bg-slate-50 p-3 font-mono text-[11px] text-slate-800 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-200 overflow-x-auto max-h-64">{formatJSON(selectedEntry.after_value)}</pre>
					</div>
				</div>

				{#if selectedEntry.reason}
					<div class="rounded-lg border border-slate-200 bg-slate-50/60 p-3 text-xs dark:border-slate-800 dark:bg-slate-800/30">
						<span class="font-semibold text-slate-700 dark:text-slate-300">Stated Reason:</span> {selectedEntry.reason}
					</div>
				{/if}
			</div>

			<div class="mt-4 border-t border-slate-200 pt-3 flex justify-end dark:border-slate-800">
				<button
					type="button"
					onclick={() => (isModalOpen = false)}
					class="rounded-lg bg-slate-100 px-4 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-200"
				>
					Close
				</button>
			</div>
		</div>
	</div>
{/if}
