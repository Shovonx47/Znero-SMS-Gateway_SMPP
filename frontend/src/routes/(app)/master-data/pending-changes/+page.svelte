<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { ChangeRequest } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let changeRequests = $state<ChangeRequest[]>([]);
	let totalCount = $state(0);
	let page = $state(1);
	let pageSize = 15;
	let statusFilter = $state('pending');
	let entityTypeFilter = $state('');
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	// Detailed view / modal
	let selectedRequest = $state<ChangeRequest | null>(null);
	let isRejectModalOpen = $state(false);
	let rejectReason = $state('');
	let isProcessing = $state(false);

	const currentUserId = $derived(auth.tenant?.actor_user_id);
	const canApprove = $derived(
		auth.hasPermission('country.country.approve') ||
		auth.hasPermission('operator.operator.approve') ||
		auth.hasPermission('number.prefix.approve') ||
		auth.hasPermission('provider.provider.approve') ||
		auth.hasPermission('provider.connection.approve') ||
		auth.hasPermission('provider.coverage.approve') ||
		auth.hasPermission('customer.profile.approve') ||
		auth.hasPermission('customer.senderid.approve') ||
		auth.hasPermission('import.job.approve') ||
		auth.hasPermission('wallet.approve') ||
		auth.hasPermission('billing.invoice.void') ||
		auth.hasPermission('routing.plan.approve') ||
		auth.hasPermission('routing.entry.approve') ||
		auth.hasPermission('routing.assignment.approve') ||
		auth.hasPermission('routing.rule.approve')
	);

	async function loadChangeRequests() {
		isLoading = true;
		errorMessage = null;

		try {
			const res = await apiClient.listChangeRequests({
				status: statusFilter || undefined,
				entity_type: entityTypeFilter || undefined,
				page,
				pageSize
			});
			changeRequests = res.data.items || [];
			totalCount = res.data.total_count;
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to load change requests.';
			}
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadChangeRequests();
	});

	async function approve(req: ChangeRequest) {
		if (req.maker_user_id === currentUserId) {
			toast('Maker-Checker Violation: You cannot approve your own change request.', 'error');
			return;
		}
		if (!confirm(`Are you sure you want to approve this ${req.operation} on ${req.entity_type}?`)) {
			return;
		}

		isProcessing = true;
		try {
			await apiClient.approveChangeRequest(req.id);
			toast('Change request approved and applied', 'success');
			selectedRequest = null;
			await loadChangeRequests();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Approval failed', 'error');
		} finally {
			isProcessing = false;
		}
	}

	function openRejectModal(req: ChangeRequest) {
		selectedRequest = req;
		rejectReason = '';
		isRejectModalOpen = true;
	}

	async function confirmReject() {
		if (!selectedRequest || !rejectReason.trim()) return;
		isProcessing = true;

		try {
			await apiClient.rejectChangeRequest(selectedRequest.id, rejectReason.trim());
			toast('Change request rejected', 'success');
			isRejectModalOpen = false;
			selectedRequest = null;
			await loadChangeRequests();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Rejection failed', 'error');
		} finally {
			isProcessing = false;
		}
	}

	function renderDiff(curr: unknown, prop: unknown) {
		const cObj = (curr && typeof curr === 'object') ? curr as Record<string, unknown> : {};
		const pObj = (prop && typeof prop === 'object') ? prop as Record<string, unknown> : {};
		const keys = Array.from(new Set([...Object.keys(cObj), ...(Object.keys(pObj))]));

		return keys.map((key) => {
			const before = cObj[key];
			const after = pObj[key];
			const changed = JSON.stringify(before) !== JSON.stringify(after);
			return { key, before, after, changed };
		});
	}
</script>

<div class="space-y-6">
	<!-- Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Pending Changes (Maker-Checker)</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Dual-control approval queue for master data modifications. Makers cannot approve their own changes.
			</p>
		</div>
	</div>

	<!-- Filters -->
	<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
		<div class="flex flex-wrap items-center gap-3">
			<div>
				<select aria-label="Filter by status"
					bind:value={statusFilter}
					onchange={() => { page = 1; loadChangeRequests(); }}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="pending">Pending Review</option>
					<option value="approved">Approved</option>
					<option value="rejected">Rejected</option>
					<option value="">All Statuses</option>
				</select>
			</div>

			<div>
				<select aria-label="Filter by entity type"
					bind:value={entityTypeFilter}
					onchange={() => { page = 1; loadChangeRequests(); }}
					class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="">All Entity Types</option>
					<option value="country">Country</option>
					<option value="operator">Operator</option>
					<option value="prefix">Prefix</option>
					<option value="provider">Provider</option>
					<option value="provider_connection">Provider Connection</option>
					<option value="provider_coverage">Provider Coverage</option>
					<option value="customer_profile">Customer Profile</option>
					<option value="sender_id">Sender ID</option>
					<option value="import_job">Import Job</option>
					<option value="wallet_topup">Wallet Top-up</option>
					<option value="wallet_adjustment">Wallet Adjustment</option>
					<option value="wallet_unfreeze">Wallet Unfreeze</option>
					<option value="invoice_void">Invoice Void</option>
					<option value="route_plan_activate">Route Plan Activation</option>
					<option value="route_plan_deactivate">Route Plan Deactivation</option>
					<option value="route_plan_entry_change">Route Plan Entry Change</option>
					<option value="route_assignment_change">Route Assignment Change</option>
					<option value="routing_rule_change">Routing Rule Change</option>
					<option value="connection_reenable">Connection Re-enable</option>
				</select>
			</div>
		</div>
	</div>

	<!-- Content -->
	{#if isLoading}
		<div class="flex justify-center py-16">
			<Loading message="Loading pending change requests..." />
		</div>
	{:else if errorMessage}
		<ErrorState message={errorMessage} retry={loadChangeRequests} />
	{:else if changeRequests.length === 0}
		<Empty
			title="No change requests found"
			description="There are currently no change requests matching your filter."
		/>
	{:else}
		<div class="overflow-x-auto rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-left text-xs">
				<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 dark:text-slate-400 uppercase font-semibold">
					<tr>
						<th class="px-4 py-3">Entity</th>
						<th class="px-4 py-3">Operation</th>
						<th class="px-4 py-3">Maker</th>
						<th class="px-4 py-3">Submitted At</th>
						<th class="px-4 py-3">Status</th>
						<th class="px-4 py-3 text-right">Actions</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-200 dark:divide-slate-800 text-slate-700 dark:text-slate-300">
					{#each changeRequests as req}
						{@const isOwn = req.maker_user_id === currentUserId}
						<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40 transition">
							<td class="px-4 py-3 font-semibold uppercase text-slate-900 dark:text-white">
								{req.entity_type}
								{#if req.entity_id}
									<span class="block text-[10px] font-mono text-slate-400 lowercase">{req.entity_id.slice(0, 8)}...</span>
								{/if}
							</td>
							<td class="px-4 py-3 font-mono font-medium">
								<span class="rounded bg-indigo-50 dark:bg-indigo-950/50 px-2 py-0.5 text-[10px] text-indigo-700 dark:text-indigo-300 uppercase">
									{req.operation}
								</span>
							</td>
							<td class="px-4 py-3 font-mono text-[11px]">
								{req.maker_user_id.slice(0, 8)}...
								{#if isOwn}
									<span class="ml-1 rounded bg-slate-100 dark:bg-slate-800 px-1 py-0.5 text-[9px] font-bold text-slate-600 dark:text-slate-400">YOU</span>
								{/if}
							</td>
							<td class="px-4 py-3 text-slate-500">
								{new Date(req.made_at).toLocaleString()}
							</td>
							<td class="px-4 py-3">
								{#if req.status === 'pending'}
									<span class="inline-flex items-center rounded-full bg-amber-100 px-2 py-0.5 text-[10px] font-semibold text-amber-800 dark:bg-amber-950/60 dark:text-amber-300">
										Pending
									</span>
								{:else if req.status === 'approved'}
									<span class="inline-flex items-center rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-semibold text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300">
										Approved
									</span>
								{:else}
									<span class="inline-flex items-center rounded-full bg-rose-100 px-2 py-0.5 text-[10px] font-semibold text-rose-800 dark:bg-rose-950/60 dark:text-rose-300">
										Rejected
									</span>
								{/if}
							</td>
							<td class="px-4 py-3 text-right">
								<div class="flex items-center justify-end gap-2">
									<button
										type="button"
										onclick={() => selectedRequest = req}
										class="rounded px-2 py-1 text-slate-600 hover:bg-slate-100 font-semibold dark:text-slate-300 dark:hover:bg-slate-800"
									>
										Review Diff
									</button>

									{#if req.status === 'pending' && canApprove}
										<button
											type="button"
											disabled={isOwn || isProcessing}
											onclick={() => approve(req)}
											title={isOwn ? 'Maker cannot approve own request' : 'Approve Change'}
											class="rounded-lg bg-emerald-600 px-3 py-1 font-semibold text-white hover:bg-emerald-500 disabled:opacity-40 transition"
										>
											Approve
										</button>
										<button
											type="button"
											disabled={isProcessing}
											onclick={() => openRejectModal(req)}
											class="rounded-lg border border-rose-300 px-3 py-1 font-semibold text-rose-600 hover:bg-rose-50 dark:border-rose-800 dark:text-rose-400 dark:hover:bg-rose-950/50 transition"
										>
											Reject
										</button>
									{/if}
								</div>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>

<!-- Diff / Review Modal -->
{#if selectedRequest && !isRejectModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm overflow-y-auto">
		<div class="w-full max-w-3xl rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900 my-8">
			<div class="flex items-center justify-between border-b border-slate-200 pb-4 dark:border-slate-800">
				<div>
					<h2 class="text-lg font-bold text-slate-900 dark:text-white">
						Review Change Request: {selectedRequest.operation.toUpperCase()} on {selectedRequest.entity_type.toUpperCase()}
					</h2>
					<p class="text-xs text-slate-500">Maker: {selectedRequest.maker_user_id} • Status: {selectedRequest.status}</p>
				</div>
				<button
					type="button"
					aria-label="Close dialog"
					onclick={() => selectedRequest = null}
					class="rounded p-1 text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			{#if selectedRequest.reject_reason}
				<div class="mt-4 rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300 border border-rose-200 dark:border-rose-800">
					<strong>Rejection Reason:</strong> {selectedRequest.reject_reason}
				</div>
			{/if}

			<div class="mt-4 space-y-3">
				<h3 class="text-xs font-bold uppercase tracking-wider text-slate-400">Proposed Changes (Diff)</h3>
				<div class="overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-800">
					<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-xs">
						<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 font-semibold">
							<tr>
								<th class="px-3 py-2 text-left w-36">Field</th>
								<th class="px-3 py-2 text-left">Current Value</th>
								<th class="px-3 py-2 text-left">Proposed Value</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-slate-200 dark:divide-slate-800 font-mono text-[11px]">
							{#each renderDiff(selectedRequest.current_data, selectedRequest.proposed_data) as d}
								<tr class={d.changed ? 'bg-amber-50/60 dark:bg-amber-950/20' : ''}>
									<td class="px-3 py-2 font-semibold text-slate-700 dark:text-slate-300">{d.key}</td>
									<td class="px-3 py-2 text-slate-500">{JSON.stringify(d.before) ?? '—'}</td>
									<td class="px-3 py-2 font-bold {d.changed ? 'text-indigo-600 dark:text-indigo-400' : 'text-slate-700 dark:text-slate-300'}">
										{JSON.stringify(d.after) ?? '—'}
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			</div>

			<div class="mt-6 flex items-center justify-end gap-3 border-t border-slate-200 pt-4 dark:border-slate-800">
				<button
					type="button"
					onclick={() => selectedRequest = null}
					class="rounded-lg border border-slate-300 px-4 py-2 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
				>
					Close
				</button>
				{#if selectedRequest.status === 'pending' && canApprove}
					<button
						type="button"
						onclick={() => openRejectModal(selectedRequest!)}
						class="rounded-lg border border-rose-300 px-4 py-2 text-xs font-semibold text-rose-600 hover:bg-rose-50"
					>
						Reject
					</button>
					<button
						type="button"
						disabled={selectedRequest.maker_user_id === currentUserId || isProcessing}
						onclick={() => approve(selectedRequest!)}
						class="rounded-lg bg-emerald-600 px-4 py-2 text-xs font-semibold text-white hover:bg-emerald-500 disabled:opacity-40"
					>
						Approve & Apply
					</button>
				{/if}
			</div>
		</div>
	</div>
{/if}

<!-- Reject Modal -->
{#if isRejectModalOpen && selectedRequest}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-md rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Reject Change Request</h2>
			<p class="text-xs text-slate-500 mt-1">Please provide a mandatory reason for rejecting this change.</p>

			<textarea
				bind:value={rejectReason}
				rows="3"
				placeholder="State the reason for rejection..."
				class="mt-4 w-full rounded-lg border border-slate-300 p-2 text-xs text-slate-900 focus:border-rose-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
			></textarea>

			<div class="mt-4 flex items-center justify-end gap-2">
				<button
					type="button"
					onclick={() => isRejectModalOpen = false}
					class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700 hover:bg-slate-50"
				>
					Cancel
				</button>
				<button
					type="button"
					disabled={!rejectReason.trim() || isProcessing}
					onclick={confirmReject}
					class="rounded-lg bg-rose-600 px-3 py-1.5 text-xs font-semibold text-white hover:bg-rose-500 disabled:opacity-50"
				>
					Confirm Reject
				</button>
			</div>
		</div>
	</div>
{/if}
