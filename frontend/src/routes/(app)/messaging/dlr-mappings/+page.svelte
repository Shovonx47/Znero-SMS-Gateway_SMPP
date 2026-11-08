<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import type { DLRMapping, ProviderConnection } from '$lib/types/api';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let mappings = $state<DLRMapping[]>([]);
	let connections = $state<ProviderConnection[]>([]);
	let selectedConn = $state('');

	// Modal State
	let showModal = $state(false);
	let editMode = $state(false);
	let currentID = $state('');
	let connID = $state('');
	let upstreamStat = $state('');
	let upstreamErr = $state('');
	let mappedStatus = $state('delivered');
	let description = $state('');
	let saving = $state(false);
	let saveError = $state<string | null>(null);

	async function loadMappings() {
		loading = true;
		error = null;
		try {
			const [mapRes, connRes] = await Promise.all([
				apiClient.listDLRMappings(selectedConn || undefined),
				apiClient.listAllConnections()
			]);
			mappings = mapRes.data.items || [];
			connections = connRes.data || [];
		} catch (err: any) {
			error = err.message || 'Failed to load DLR status mappings';
		} finally {
			loading = false;
		}
	}

	function openCreateModal() {
		editMode = false;
		currentID = '';
		connID = selectedConn || (connections[0]?.id ?? '');
		upstreamStat = '';
		upstreamErr = '';
		mappedStatus = 'delivered';
		description = '';
		saveError = null;
		showModal = true;
	}

	function openEditModal(m: DLRMapping) {
		editMode = true;
		currentID = m.id;
		connID = m.connection_id;
		upstreamStat = m.upstream_stat;
		upstreamErr = m.upstream_err || '';
		mappedStatus = m.mapped_status;
		description = m.description || '';
		saveError = null;
		showModal = true;
	}

	async function handleSave(e: SubmitEvent) {
		e.preventDefault();
		saving = true;
		saveError = null;

		const payload: Partial<DLRMapping> = {
			connection_id: connID,
			upstream_stat: upstreamStat.trim().toUpperCase(),
			upstream_err: upstreamErr.trim() || undefined,
			mapped_status: mappedStatus,
			description: description.trim() || undefined
		};

		try {
			if (editMode) {
				await apiClient.updateDLRMapping(currentID, payload);
			} else {
				await apiClient.createDLRMapping(payload);
			}
			showModal = false;
			loadMappings();
		} catch (err: any) {
			saveError = err.message || 'Failed to save DLR mapping';
		} finally {
			saving = false;
		}
	}

	async function handleDelete(id: string) {
		if (!confirm('Are you sure you want to delete this DLR mapping override?')) return;
		try {
			await apiClient.deleteDLRMapping(id);
			loadMappings();
		} catch (err: any) {
			alert('Delete failed: ' + err.message);
		}
	}

	onMount(() => {
		loadMappings();
	});
</script>

<svelte:head>
	<title>DLR Status Mappings — Znero</title>
</svelte:head>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-xl font-bold text-slate-900 dark:text-white">DLR Status Mappings</h1>
			<p class="text-xs text-slate-500 dark:text-slate-400">
				Configure custom receipt status overrides per upstream carrier connection with versioned history.
			</p>
		</div>

		<button
			type="button"
			onclick={openCreateModal}
			class="inline-flex items-center gap-1.5 rounded-lg bg-indigo-600 px-3.5 py-1.5 text-xs font-semibold text-white shadow-sm hover:bg-indigo-700"
		>
			<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
			</svg>
			<span>New DLR Mapping</span>
		</button>
	</div>

	<!-- Connection Filter -->
	<div class="flex items-center gap-3">
		<label for="conn-filter" class="text-xs font-semibold text-slate-700 dark:text-slate-300">Filter Connection:</label>
		<select
			id="conn-filter"
			bind:value={selectedConn}
			onchange={loadMappings}
			class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs text-slate-900 shadow-sm dark:border-slate-700 dark:bg-slate-800 dark:text-white"
		>
			<option value="">All Provider Connections</option>
			{#each connections as c}
				<option value={c.id}>{c.name}</option>
			{/each}
		</select>
	</div>

	<!-- Table -->
	<div class="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
		{#if loading}
			<div class="p-8 text-center text-xs text-slate-500">Loading mappings...</div>
		{:else if error}
			<div class="p-8 text-center text-xs text-rose-600">{error}</div>
		{:else if mappings.length === 0}
			<div class="p-12 text-center text-xs text-slate-500">
				No custom DLR status mappings configured. Standard SMPP 3.4 status mapping is active.
			</div>
		{:else}
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50 text-[11px] font-semibold uppercase text-slate-400">
						<tr>
							<th class="px-4 py-3">Carrier Connection</th>
							<th class="px-4 py-3">Upstream Stat</th>
							<th class="px-4 py-3">Error Code</th>
							<th class="px-4 py-3">Mapped Target Status</th>
							<th class="px-4 py-3">Version</th>
							<th class="px-4 py-3">Description</th>
							<th class="px-4 py-3 text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
						{#each mappings as m}
							{@const connObj = connections.find((c) => c.id === m.connection_id)}
							<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/50">
								<td class="px-4 py-3 font-medium text-slate-900 dark:text-white">
									{connObj?.name || m.connection_id}
								</td>
								<td class="px-4 py-3 font-mono font-bold text-slate-900 dark:text-white">
									{m.upstream_stat}
								</td>
								<td class="px-4 py-3 font-mono text-slate-500">
									{m.upstream_err || 'Any (*)'}
								</td>
								<td class="px-4 py-3">
									<span class="rounded bg-indigo-50 px-2 py-0.5 text-[10px] font-bold text-indigo-700 uppercase dark:bg-indigo-950/40 dark:text-indigo-300">
										{m.mapped_status}
									</span>
								</td>
								<td class="px-4 py-3 font-mono text-slate-500">
									v{m.version}
								</td>
								<td class="px-4 py-3 text-slate-500 max-w-xs truncate">
									{m.description || '—'}
								</td>
								<td class="px-4 py-3 text-right whitespace-nowrap space-x-2">
									<button
										type="button"
										onclick={() => openEditModal(m)}
										class="font-semibold text-indigo-600 hover:text-indigo-800 dark:text-indigo-400"
									>
										Edit
									</button>
									<button
										type="button"
										onclick={() => handleDelete(m.id)}
										class="font-semibold text-rose-600 hover:text-rose-800"
									>
										Delete
									</button>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</div>
</div>

<!-- Modal -->
{#if showModal}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/60 p-4 backdrop-blur-xs">
		<div class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<div class="flex items-center justify-between border-b border-slate-200 pb-3 dark:border-slate-800">
				<h3 class="text-sm font-bold text-slate-900 dark:text-white">
					{editMode ? 'Edit DLR Mapping Override' : 'New DLR Mapping Override'}
				</h3>
				<button
					type="button"
					aria-label="Close"
					onclick={() => (showModal = false)}
					class="text-slate-400 hover:text-slate-600"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			<form onsubmit={handleSave} class="mt-4 space-y-4 text-xs">
				{#if saveError}
					<div class="rounded-lg bg-rose-50 p-3 text-rose-800 dark:bg-rose-950/40 dark:text-rose-300">
						{saveError}
					</div>
				{/if}

				<div>
					<label for="modal-conn" class="block font-semibold text-slate-700 dark:text-slate-300">Carrier Connection</label>
					<select
						id="modal-conn"
						bind:value={connID}
						required
						disabled={editMode}
						class="mt-1 block w-full rounded-lg border border-slate-300 px-3 py-2 text-slate-900 shadow-sm dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						{#each connections as c}
							<option value={c.id}>{c.name}</option>
						{/each}
					</select>
				</div>

				<div class="grid grid-cols-2 gap-3">
					<div>
						<label for="modal-stat" class="block font-semibold text-slate-700 dark:text-slate-300">Upstream Stat</label>
						<input
							id="modal-stat"
							type="text"
							bind:value={upstreamStat}
							required
							placeholder="e.g. DELIVRD"
							class="mt-1 block w-full rounded-lg border border-slate-300 px-3 py-2 text-slate-900 uppercase font-mono shadow-sm dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
					<div>
						<label for="modal-err" class="block font-semibold text-slate-700 dark:text-slate-300">Err Code (Optional)</label>
						<input
							id="modal-err"
							type="text"
							bind:value={upstreamErr}
							placeholder="e.g. 000"
							class="mt-1 block w-full rounded-lg border border-slate-300 px-3 py-2 text-slate-900 font-mono shadow-sm dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
				</div>

				<div>
					<label for="modal-mapped" class="block font-semibold text-slate-700 dark:text-slate-300">Mapped Internal Status</label>
					<select
						id="modal-mapped"
						bind:value={mappedStatus}
						required
						class="mt-1 block w-full rounded-lg border border-slate-300 px-3 py-2 text-slate-900 shadow-sm dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						<option value="delivered">Delivered (Captures Hold)</option>
						<option value="undelivered">Undelivered (Releases Hold)</option>
						<option value="expired">Expired (Releases Hold)</option>
						<option value="failed">Failed (Releases Hold)</option>
						<option value="unknown">Unknown (Retains Hold)</option>
					</select>
				</div>

				<div>
					<label for="modal-desc" class="block font-semibold text-slate-700 dark:text-slate-300">Description / Reason</label>
					<textarea
						id="modal-desc"
						bind:value={description}
						rows="2"
						placeholder="Document carrier quirk or policy reasoning"
						class="mt-1 block w-full rounded-lg border border-slate-300 px-3 py-2 text-slate-900 shadow-sm dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					></textarea>
				</div>

				<div class="flex justify-end gap-2 pt-2">
					<button
						type="button"
						onclick={() => (showModal = false)}
						class="rounded-lg border border-slate-300 px-3 py-1.5 font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={saving}
						class="rounded-lg bg-indigo-600 px-4 py-1.5 font-semibold text-white shadow-sm hover:bg-indigo-700 disabled:opacity-50"
					>
						{saving ? 'Saving...' : 'Save Mapping'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}
