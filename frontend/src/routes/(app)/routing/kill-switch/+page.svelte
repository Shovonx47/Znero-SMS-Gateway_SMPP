<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { ProviderConnection, ConnectionOverride } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let connections = $state<ProviderConnection[]>([]);
	let overrides = $state<Record<string, ConnectionOverride>>({});
	let loading = $state(true);
	let errorMessage = $state('');
	let actionMessage = $state('');
	let actionError = $state('');

	// Disable Modal state
	let showDisableModal = $state(false);
	let targetConnection = $state<ProviderConnection | null>(null);
	let disableReason = $state('');
	let disableSubmitting = $state(false);
	let disableError = $state('');

	// Enable Modal state
	let showEnableModal = $state(false);
	let enableReason = $state('');
	let enableSubmitting = $state(false);
	let enableError = $state('');

	const canKill = $derived(auth.hasPermission('routing.killswitch'));

	async function loadData() {
		loading = true;
		errorMessage = '';
		try {
			const [cRes, oRes] = await Promise.all([
				apiClient.listAllConnections(),
				apiClient.listConnectionOverrides()
			]);
			connections = cRes.data || [];
			const map: Record<string, ConnectionOverride> = {};
			for (const o of oRes.data || []) {
				map[o.connection_id] = o;
			}
			overrides = map;
		} catch (err: any) {
			errorMessage = err.message || 'Failed to load connections and kill switch status';
		} finally {
			loading = false;
		}
	}

	function openDisableModal(c: ProviderConnection) {
		targetConnection = c;
		disableReason = '';
		disableError = '';
		showDisableModal = true;
	}

	function openEnableModal(c: ProviderConnection) {
		targetConnection = c;
		enableReason = '';
		enableError = '';
		showEnableModal = true;
	}

	async function handleDisable() {
		if (!targetConnection || !disableReason.trim()) {
			disableError = 'Mandatory reason is required to activate kill switch.';
			return;
		}
		disableSubmitting = true;
		disableError = '';
		try {
			await apiClient.disableConnection(targetConnection.id, disableReason.trim());
			actionMessage = `Kill switch ACTIVATED for connection "${targetConnection.name}". Instant snapshot reload executed.`;
			showDisableModal = false;
			loadData();
		} catch (err: any) {
			disableError = err.message || 'Failed to activate kill switch';
		} finally {
			disableSubmitting = false;
		}
	}

	async function handleEnable() {
		if (!targetConnection || !enableReason.trim()) {
			enableError = 'Mandatory justification reason is required for re-enable approval.';
			return;
		}
		enableSubmitting = true;
		enableError = '';
		try {
			const res = await apiClient.enableConnection(targetConnection.id, enableReason.trim());
			if (res.data.change_request) {
				actionMessage = `Re-enable change request submitted for checker review (CR: ${res.data.change_request.id.slice(0, 8)}...). Approval required before traffic resumes.`;
			} else {
				actionMessage = `Connection "${targetConnection.name}" re-enabled successfully.`;
			}
			showEnableModal = false;
			loadData();
		} catch (err: any) {
			enableError = err.message || 'Failed to request re-enable';
		} finally {
			enableSubmitting = false;
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
			<div class="flex items-center gap-3">
				<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Emergency Kill Switch</h1>
				<span class="rounded bg-rose-100 px-2.5 py-0.5 text-xs font-bold text-rose-800 dark:bg-rose-950/60 dark:text-rose-300">
					CRITICAL CONTROLS
				</span>
			</div>
			<p class="text-sm text-slate-500 dark:text-slate-400 mt-1">
				Instantly take down faulty provider egress connections with mandatory auditing. Re-enabling requires maker-checker approval.
			</p>
		</div>

		<button
			type="button"
			onclick={loadData}
			class="rounded-lg border border-slate-200 px-3 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
		>
			Refresh Status
		</button>
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

	<!-- Connections Table -->
	{#if loading}
		<Loading message="Loading egress connections and kill switch status..." />
	{:else if errorMessage}
		<ErrorState title="Error Loading Kill Switch" message={errorMessage} retry={loadData} />
	{:else if connections.length === 0}
		<Empty title="No connections found" description="No provider egress connections have been configured yet." />
	{:else}
		<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-xs dark:border-slate-800 dark:bg-slate-900">
			<table class="w-full text-left text-sm text-slate-600 dark:text-slate-400">
				<thead class="border-b border-slate-200 bg-slate-50 text-xs font-semibold uppercase text-slate-500 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-400">
					<tr>
						<th class="px-4 py-3">Connection Name</th>
						<th class="px-4 py-3">Provider</th>
						<th class="px-4 py-3">Protocol</th>
						<th class="px-4 py-3">Status</th>
						<th class="px-4 py-3">Kill Switch State</th>
						<th class="px-4 py-3">Reason / Details</th>
						<th class="px-4 py-3 text-right">Emergency Action</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
					{#each connections as c}
						{@const isKilled = !!overrides[c.id]}
						<tr class={isKilled ? 'bg-rose-50/40 dark:bg-rose-950/20' : 'hover:bg-slate-50/50 dark:hover:bg-slate-800/40'}>
							<td class="px-4 py-3 font-semibold text-slate-900 dark:text-white">
								{c.name}
							</td>
							<td class="px-4 py-3 text-xs text-slate-500">
								{c.name}
							</td>
							<td class="px-4 py-3 font-mono text-xs uppercase">
								{c.protocol}
							</td>
							<td class="px-4 py-3">
								<span class="inline-flex rounded-full px-2 py-0.5 text-[11px] font-semibold {c.status === 'active' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300' : 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300'}">
									{c.status.toUpperCase()}
								</span>
							</td>
							<td class="px-4 py-3">
								{#if isKilled}
									<span class="inline-flex items-center gap-1.5 rounded-full bg-rose-100 px-2.5 py-0.5 text-xs font-bold text-rose-800 dark:bg-rose-950/60 dark:text-rose-300">
										<span class="h-2 w-2 rounded-full bg-rose-600 animate-pulse"></span>
										<span>DISABLED</span>
									</span>
								{:else}
									<span class="inline-flex rounded-full bg-emerald-100 px-2.5 py-0.5 text-xs font-semibold text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300">
										NORMAL
									</span>
								{/if}
							</td>
							<td class="px-4 py-3 text-xs">
								{#if isKilled}
									<div class="font-medium text-rose-700 dark:text-rose-300">
										{overrides[c.id].reason}
									</div>
									<div class="text-[10px] text-slate-400 mt-0.5">
										Set {new Date(overrides[c.id].set_at).toLocaleString()}
									</div>
								{:else}
									<span class="text-slate-400">Operating normally</span>
								{/if}
							</td>
							<td class="px-4 py-3 text-right">
								{#if canKill}
									{#if isKilled}
										<button
											type="button"
											onclick={() => openEnableModal(c)}
											class="rounded-lg bg-emerald-600 px-3 py-1.5 text-xs font-semibold text-white shadow-xs hover:bg-emerald-500 transition"
										>
											Request Re-enable
										</button>
									{:else}
										<button
											type="button"
											onclick={() => openDisableModal(c)}
											class="rounded-lg bg-rose-600 px-3 py-1.5 text-xs font-semibold text-white shadow-xs hover:bg-rose-500 transition"
										>
											Disable (Kill Switch)
										</button>
									{/if}
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>

<!-- Disable Prompt Modal -->
{#if showDisableModal && targetConnection}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/60 backdrop-blur-xs p-4">
		<div class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900 space-y-4">
			<div class="flex items-center gap-3">
				<div class="flex h-10 w-10 items-center justify-center rounded-xl bg-rose-100 text-rose-700 dark:bg-rose-950 dark:text-rose-300 font-bold">
					!
				</div>
				<div>
					<h2 class="text-lg font-bold text-slate-900 dark:text-white">Activate Kill Switch</h2>
					<p class="text-xs text-slate-500">Target: {targetConnection.name}</p>
				</div>
			</div>

			<p class="text-xs text-rose-600 dark:text-rose-400 font-medium">
				Warning: This action takes effect INSTANTLY on the in-memory routing engine. All route candidates pointing to this connection will be immediately disqualified.
			</p>

			{#if disableError}
				<div class="rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300">
					{disableError}
				</div>
			{/if}

			<div>
				<label for="disable-reason" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Mandatory Incident Reason *</label>
				<textarea
					id="disable-reason"
					bind:value={disableReason}
					rows="3"
					placeholder="e.g. Major vendor upstream timeout, routing corrupted DLRs, security alert..."
					class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-rose-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
				></textarea>
			</div>

			<div class="flex justify-end gap-2 pt-2">
				<button
					type="button"
					onclick={() => showDisableModal = false}
					class="rounded-lg border border-slate-200 px-4 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
				>
					Cancel
				</button>
				<button
					type="button"
					disabled={disableSubmitting}
					onclick={handleDisable}
					class="rounded-lg bg-rose-600 px-4 py-2 text-sm font-semibold text-white hover:bg-rose-500 disabled:opacity-50"
				>
					{disableSubmitting ? 'Disabling...' : 'Confirm Emergency Disable'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Enable Prompt Modal -->
{#if showEnableModal && targetConnection}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/60 backdrop-blur-xs p-4">
		<div class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900 space-y-4">
			<h2 class="text-lg font-bold text-slate-900 dark:text-white">
				Request Connection Re-enable
			</h2>
			<p class="text-xs text-slate-500">
				Re-enabling a killed connection requires maker-checker approval. Traffic will NOT resume until an authorized approver reviews this change request.
			</p>

			{#if enableError}
				<div class="rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300">
					{enableError}
				</div>
			{/if}

			<div>
				<label for="enable-reason" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Incident Resolution Justification *</label>
				<textarea
					id="enable-reason"
					bind:value={enableReason}
					rows="3"
					placeholder="e.g. Vendor fiber maintenance completed, ping and bind tests confirmed clean..."
					class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
				></textarea>
			</div>

			<div class="flex justify-end gap-2 pt-2">
				<button
					type="button"
					onclick={() => showEnableModal = false}
					class="rounded-lg border border-slate-200 px-4 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
				>
					Cancel
				</button>
				<button
					type="button"
					disabled={enableSubmitting}
					onclick={handleEnable}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					{enableSubmitting ? 'Submitting...' : 'Submit Re-enable Request'}
				</button>
			</div>
		</div>
	</div>
{/if}
