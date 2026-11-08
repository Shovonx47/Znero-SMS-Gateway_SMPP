<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import type { InboundNumber } from '$lib/types/api';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let successMsg = $state<string | null>(null);
	let numbers = $state<InboundNumber[]>([]);
	let total = $state(0);
	let page = $state(1);

	let showAddModal = $state(false);
	let newRawNumber = $state('');
	let newType = $state('longcode');
	let newCustomerOrgID = $state('');

	async function loadNumbers() {
		loading = true;
		error = null;
		try {
			const res = await apiClient.listInboundNumbers({ page, per_page: 25 });
			numbers = res.data.items || [];
			total = res.data.total || 0;
		} catch (err: any) {
			error = err.message || 'Failed to load inbound virtual numbers';
		} finally {
			loading = false;
		}
	}

	async function addNumber() {
		if (!newRawNumber.trim()) return;
		error = null;
		successMsg = null;
		try {
			const res = await apiClient.createInboundNumber({
				number_raw: newRawNumber.trim(),
				type: newType,
				customer_org_id: newCustomerOrgID.trim() || undefined,
				capabilities: ['sms']
			});
			numbers = [res.data, ...numbers];
			newRawNumber = '';
			newCustomerOrgID = '';
			showAddModal = false;
			successMsg = 'Inbound virtual number added successfully';
		} catch (err: any) {
			error = err.message || 'Failed to add inbound number';
		}
	}

	onMount(() => {
		loadNumbers();
	});
</script>

<svelte:head>
	<title>Inbound Numbers â€” Operations</title>
</svelte:head>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Inbound Numbers (MO)</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Manage virtual longcodes and shortcodes assigned to customer organizations for inbound SMS routing.
			</p>
		</div>
		<button
			type="button"
			onclick={() => (showAddModal = true)}
			class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-3.5 py-2 text-xs font-semibold text-white shadow hover:bg-indigo-700"
		>
			+ Add Inbound Number
		</button>
	</div>

	{#if error}
		<div class="rounded-lg bg-rose-50 p-4 text-sm text-rose-700 dark:bg-rose-950/40 dark:text-rose-300">{error}</div>
	{/if}
	{#if successMsg}
		<div class="rounded-lg bg-emerald-50 p-4 text-sm text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300">{successMsg}</div>
	{/if}

	<!-- Table -->
	<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
		<table class="w-full text-left text-xs">
			<thead class="border-b border-slate-200 bg-slate-50 font-medium text-slate-500 dark:border-slate-800 dark:bg-slate-800/60 dark:text-slate-400">
				<tr>
					<th class="px-4 py-3">Number (E.164)</th>
					<th class="px-4 py-3">Type</th>
					<th class="px-4 py-3">Capabilities</th>
					<th class="px-4 py-3">Assigned Customer Org</th>
					<th class="px-4 py-3">Status</th>
					<th class="px-4 py-3">Created</th>
				</tr>
			</thead>
			<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
				{#if loading && numbers.length === 0}
					<tr>
						<td colspan="6" class="px-4 py-8 text-center text-slate-400">Loading inbound numbers...</td>
					</tr>
				{:else if numbers.length === 0}
					<tr>
						<td colspan="6" class="px-4 py-8 text-center text-slate-400">No inbound numbers provisioned yet.</td>
					</tr>
				{:else}
					{#each numbers as n (n.id)}
						<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/50">
							<td class="px-4 py-3 font-mono font-bold text-slate-900 dark:text-white">{n.number_e164}</td>
							<td class="px-4 py-3 font-mono uppercase text-slate-600 dark:text-slate-400">{n.type}</td>
							<td class="px-4 py-3">
								<div class="flex gap-1">
									{#each n.capabilities as cap}
										<span class="rounded bg-slate-100 px-2 py-0.5 text-[10px] uppercase font-mono text-slate-600 dark:bg-slate-800 dark:text-slate-300">
											{cap}
										</span>
									{/each}
								</div>
							</td>
							<td class="px-4 py-3 font-mono text-xs">
								{#if n.customer_org_id}
									<span class="text-indigo-600 dark:text-indigo-400">{n.customer_org_id}</span>
								{:else}
									<span class="italic text-slate-400">Unassigned (Pool)</span>
								{/if}
							</td>
							<td class="px-4 py-3">
								<span
									class="inline-flex rounded-full px-2 py-0.5 text-[10px] font-semibold {n.status ===
									'active'
										? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
										: 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300'}"
								>
									{n.status}
								</span>
							</td>
							<td class="px-4 py-3 text-slate-500">{new Date(n.created_at).toLocaleDateString()}</td>
						</tr>
					{/each}
				{/if}
			</tbody>
		</table>
	</div>

	<!-- Add Modal -->
	{#if showAddModal}
		<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4">
			<div class="w-full max-w-md rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
				<h3 class="text-base font-bold text-slate-900 dark:text-white">Provision Inbound Number</h3>
				<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
					Add a new virtual number and optionally route it directly to a customer tenant.
				</p>

				<div class="mt-4 space-y-4">
					<div>
						<label for="num-raw" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Number (E.164)</label>
						<input
							id="num-raw"
							type="text"
							bind:value={newRawNumber}
							placeholder="+14155550199"
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 text-xs font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
					<div>
						<label for="num-type" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Number Type</label>
						<select
							id="num-type"
							bind:value={newType}
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 text-xs dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						>
							<option value="longcode">Longcode (10D/E.164)</option>
							<option value="shortcode">Shortcode (5-6 Digits)</option>
						</select>
					</div>
					<div>
						<label for="cust-org-id" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Customer Org ID (Optional)</label>
						<input
							id="cust-org-id"
							type="text"
							bind:value={newCustomerOrgID}
							placeholder="e.g. 550e8400-e29b-41d4-a716-446655440000"
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 text-xs font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
				</div>

				<div class="mt-6 flex justify-end gap-3">
					<button
						type="button"
						onclick={() => (showAddModal = false)}
						class="rounded-lg px-4 py-2 text-xs font-medium text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
					>
						Cancel
					</button>
					<button
						type="button"
						onclick={() => addNumber()}
						class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow hover:bg-indigo-700"
					>
						Provision
					</button>
				</div>
			</div>
		</div>
	{/if}
</div>
