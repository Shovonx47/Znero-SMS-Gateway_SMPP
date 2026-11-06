<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { Country, CustomerProfile, Organization, SenderID, SenderIDHistory } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let senderIds = $state<SenderID[]>([]);
	let countries = $state<Country[]>([]);
	let customerOrgs = $state<Organization[]>([]);
	let profiles = $state<CustomerProfile[]>([]);
	let totalCount = $state(0);
	let page = $state(1);
	let pageSize = 25;

	let statusFilter = $state('');
	let typeFilter = $state('');
	let search = $state('');

	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	// Submit Request Modal
	let isCreateModalOpen = $state(false);
	let formOrgId = $state('');
	let formSenderId = $state('');
	let formSenderType = $state<'alphanumeric' | 'numeric' | 'shortcode'>('alphanumeric');
	let formCountryId = $state('');
	let formNotes = $state('');
	let formReason = $state('');
	let formError = $state<string | null>(null);
	let isSubmitting = $state(false);

	// Reject Modal
	let rejectTarget = $state<SenderID | null>(null);
	let rejectReason = $state('');
	let isRejecting = $state(false);

	// History Modal
	let histTarget = $state<SenderID | null>(null);
	let sidHistory = $state<SenderIDHistory[]>([]);

	const canCreate = $derived(auth.hasPermission('customer.senderid.create'));
	const canApprove = $derived(auth.tenant?.actor_org_type === 'platform' && auth.hasPermission('customer.senderid.approve'));
	const selectedCountry = $derived(countries.find((c) => c.id === formCountryId) || null);

	async function loadData() {
		isLoading = true;
		errorMessage = null;
		try {
			const [sidRes, cRes] = await Promise.all([
				apiClient.listSenderIDs({
					status: statusFilter || undefined,
					sender_type: typeFilter || undefined,
					search: search.trim() || undefined,
					page,
					pageSize
				}),
				apiClient.listCountries({ pageSize: 300 })
			]);
			senderIds = sidRes.data.items || [];
			totalCount = sidRes.data.total || 0;
			countries = cRes.data.items || [];

			try {
				const profRes = await apiClient.listCustomerProfiles({ pageSize: 100 });
				profiles = profRes.data.items || [];
			} catch {
				profiles = [];
			}
			try {
				const orgRes = await apiClient.listOrganizations({ type: 'customer', pageSize: 100 });
				customerOrgs = orgRes.data.items || [];
			} catch {
				customerOrgs = [];
			}
		} catch (err) {
			errorMessage = err instanceof ApiClientError ? err.message : 'Failed to load Sender ID registry.';
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadData();
	});

	function openSubmitModal() {
		formOrgId = auth.tenant?.actor_org_type === 'customer'
			? (auth.tenant.actor_org_id || '')
			: (profiles[0]?.organization_id || customerOrgs[0]?.id || '');
		formSenderId = '';
		formSenderType = 'alphanumeric';
		formCountryId = '';
		formNotes = '';
		formReason = '';
		formError = null;
		isCreateModalOpen = true;
	}

	async function handleCreate() {
		isSubmitting = true;
		formError = null;
		try {
			await apiClient.createSenderID({
				organization_id: formOrgId,
				sender_id: formSenderId.trim(),
				sender_type: formSenderType,
				country_id: formCountryId || null,
				approval_notes: formNotes.trim(),
				data_source: 'Sender ID Portal',
				reason: formReason.trim() || 'Submitted Sender ID registration request'
			});
			isCreateModalOpen = false;
			toast('Sender ID request submitted (status: pending)', 'success');
			await loadData();
		} catch (err) {
			formError = err instanceof ApiClientError ? err.message : 'Failed to submit Sender ID';
		} finally {
			isSubmitting = false;
		}
	}

	async function handleApprove(sid: SenderID) {
		try {
			await apiClient.approveSenderID(sid.id, 'Approved via Sender ID Registry', 'Verified originator compliance');
			toast(`Approved Sender ID ${sid.sender_id}`, 'success');
			await loadData();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Approval failed', 'error');
		}
	}

	async function handleConfirmReject() {
		if (!rejectTarget || !rejectReason.trim()) return;
		isRejecting = true;
		try {
			await apiClient.rejectSenderID(rejectTarget.id, rejectReason.trim());
			toast(`Rejected Sender ID ${rejectTarget.sender_id}`, 'success');
			rejectTarget = null;
			await loadData();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Reject failed', 'error');
		} finally {
			isRejecting = false;
		}
	}

	async function openHistory(sid: SenderID) {
		histTarget = sid;
		try {
			const res = await apiClient.getSenderIDHistory(sid.id);
			sidHistory = res.data || [];
		} catch {
			toast('Failed to load Sender ID history', 'error');
		}
	}
</script>

<div class="space-y-6">
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Sender ID Registry</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Alphanumeric, numeric, and shortcode originator registration and platform compliance approval workflow.
			</p>
		</div>

		<div class="flex flex-wrap items-center gap-2">
			<a
				href="/master-data/import"
				class="rounded-lg border border-indigo-300 bg-indigo-50 px-3 py-2 text-xs font-semibold text-indigo-700 hover:bg-indigo-100"
			>
				Batch Import Sender IDs
			</a>
			{#if canCreate}
				<button
					type="button"
					onclick={openSubmitModal}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white hover:bg-indigo-500"
				>
					+ Submit Sender ID Request
				</button>
			{/if}
		</div>
	</div>

	<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
		<div class="flex flex-wrap items-center gap-3">
			<input
				type="text"
				aria-label="Search sender IDs"
				placeholder="Search sender ID or organization..."
				bind:value={search}
				onkeydown={(e) => e.key === 'Enter' && loadData()}
				class="w-64 rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
			/>
			<select
				aria-label="Filter by status"
				bind:value={statusFilter}
				onchange={() => { page = 1; loadData(); }}
				class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
			>
				<option value="">All Statuses</option>
				<option value="pending">Pending Approval</option>
				<option value="approved">Approved</option>
				<option value="rejected">Rejected</option>
				<option value="suspended">Suspended</option>
			</select>
			<select
				aria-label="Filter by sender type"
				bind:value={typeFilter}
				onchange={() => { page = 1; loadData(); }}
				class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
			>
				<option value="">All Types</option>
				<option value="alphanumeric">Alphanumeric (3–11 chars)</option>
				<option value="numeric">Numeric (3–15 digits)</option>
				<option value="shortcode">Shortcode (3–8 digits)</option>
			</select>
		</div>
	</div>

	{#if isLoading}
		<div class="flex justify-center py-16">
			<Loading message="Loading Sender IDs..." />
		</div>
	{:else if errorMessage}
		<ErrorState message={errorMessage} retry={loadData} />
	{:else if senderIds.length === 0}
		<Empty title="No Sender IDs found" description="Submit a Sender ID registration request to begin." />
	{:else}
		<div class="overflow-x-auto rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-left text-xs">
				<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 uppercase font-semibold">
					<tr>
						<th class="px-4 py-3">Sender ID</th>
						<th class="px-4 py-3">Type</th>
						<th class="px-4 py-3">Customer Organization</th>
						<th class="px-4 py-3">Country & Policy</th>
						<th class="px-4 py-3">Status</th>
						<th class="px-4 py-3">Approval Notes</th>
						<th class="px-4 py-3 text-right">Actions</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
					{#each senderIds as sid}
						<tr>
							<td class="px-4 py-3 font-mono font-bold text-indigo-600 dark:text-indigo-400">{sid.sender_id}</td>
							<td class="px-4 py-3 font-mono uppercase">{sid.sender_type}</td>
							<td class="px-4 py-3 font-semibold">{sid.organization_name || sid.organization_id.slice(0, 8)}</td>
							<td class="px-4 py-3">
								{sid.country_iso2 ? `${sid.country_iso2} — ${sid.country_name}` : 'Global'}
								{#if sid.country_sender_id_policy_notes}
									<div class="text-[11px] text-slate-400">{sid.country_sender_id_policy_notes}</div>
								{/if}
							</td>
							<td class="px-4 py-3">
								<span class="rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase {sid.status === 'approved'
									? 'bg-emerald-100 text-emerald-800'
									: sid.status === 'pending'
										? 'bg-amber-100 text-amber-800'
										: 'bg-rose-100 text-rose-800'}">
									{sid.status}
								</span>
							</td>
							<td class="px-4 py-3 text-slate-500">{sid.approval_notes || '—'}</td>
							<td class="px-4 py-3 text-right space-x-2">
								{#if sid.status === 'pending' && canApprove}
									<button
										type="button"
										onclick={() => handleApprove(sid)}
										class="rounded bg-emerald-600 px-2.5 py-1 text-xs font-semibold text-white hover:bg-emerald-500"
									>
										Approve
									</button>
									<button
										type="button"
										onclick={() => { rejectTarget = sid; rejectReason = ''; }}
										class="rounded border border-rose-300 px-2.5 py-1 text-xs font-semibold text-rose-600 hover:bg-rose-50"
									>
										Reject
									</button>
								{/if}
								<button
									type="button"
									onclick={() => openHistory(sid)}
									class="rounded px-2 py-1 text-xs font-semibold text-slate-600 hover:bg-slate-100"
								>
									History (v{sid.version})
								</button>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>

{#if isCreateModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-lg rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Submit Sender ID Request</h2>
			{#if formError}
				<div class="mt-2 rounded bg-rose-50 p-2 text-xs text-rose-700">{formError}</div>
			{/if}
			<div class="mt-4 space-y-3 text-xs">
				{#if auth.tenant?.actor_org_type !== 'customer'}
					<div>
						<label for="sid-org" class="block font-medium text-slate-700 mb-1">Customer Organization *</label>
						<select id="sid-org" bind:value={formOrgId} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
							<option value="">Select Customer Organization</option>
							{#each customerOrgs as o}
								<option value={o.id}>{o.name}</option>
							{/each}
							{#if customerOrgs.length === 0}
								{#each profiles as p}
									<option value={p.organization_id}>{p.organization_name || p.organization_id}</option>
								{/each}
							{/if}
						</select>
					</div>
				{/if}
				<div class="grid grid-cols-2 gap-3">
					<div>
						<label for="sid-val" class="block font-medium text-slate-700 mb-1">Sender ID *</label>
						<input id="sid-val" type="text" bind:value={formSenderId} placeholder="e.g. ZNERO-OTP" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="sid-type" class="block font-medium text-slate-700 mb-1">Sender Type *</label>
						<select id="sid-type" bind:value={formSenderType} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
							<option value="alphanumeric">Alphanumeric (3–11 chars)</option>
							<option value="numeric">Numeric (3–15 digits)</option>
							<option value="shortcode">Shortcode (3–8 digits)</option>
						</select>
					</div>
				</div>
				<div>
					<label for="sid-country" class="block font-medium text-slate-700 mb-1">Destination Country (Optional)</label>
					<select id="sid-country" bind:value={formCountryId} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="">Global (All Countries)</option>
						{#each countries as c}
							<option value={c.id}>{c.iso2} — {c.name}</option>
						{/each}
					</select>
				</div>
				{#if selectedCountry?.sender_id_policy_notes}
					<div class="rounded-lg border border-indigo-200 bg-indigo-50 p-2.5 text-xs text-indigo-800">
						<strong>{selectedCountry.iso2} Sender ID Policy Guidance:</strong> {selectedCountry.sender_id_policy_notes}
					</div>
				{/if}
				<div>
					<label for="sid-reason" class="block font-medium text-slate-700 mb-1">Use Case / Reason *</label>
					<input id="sid-reason" type="text" bind:value={formReason} placeholder="e.g. Transactional OTP messages" class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
			</div>
			<div class="mt-5 flex justify-end gap-2">
				<button type="button" onclick={() => isCreateModalOpen = false} class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700">Cancel</button>
				<button
					type="button"
					disabled={isSubmitting || !formSenderId.trim() || !formOrgId}
					onclick={handleCreate}
					class="rounded-lg bg-indigo-600 px-4 py-1.5 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					{isSubmitting ? 'Submitting...' : 'Submit Sender ID'}
				</button>
			</div>
		</div>
	</div>
{/if}

{#if rejectTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-md rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Reject Sender ID: {rejectTarget.sender_id}</h2>
			<p class="mt-1 text-xs text-slate-500">A rejection reason is mandatory and will be recorded in the version history.</p>
			<div class="mt-3">
				<label for="sid-reject-reason" class="block text-xs font-medium text-slate-700 mb-1">Rejection Reason *</label>
				<textarea id="sid-reject-reason" rows="3" bind:value={rejectReason} placeholder="State why this Sender ID was rejected..." class="w-full rounded-lg border border-slate-300 p-2 text-xs dark:border-slate-700 dark:bg-slate-800 dark:text-white"></textarea>
			</div>
			<div class="mt-4 flex justify-end gap-2">
				<button type="button" onclick={() => rejectTarget = null} class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700">Cancel</button>
				<button
					type="button"
					disabled={isRejecting || !rejectReason.trim()}
					onclick={handleConfirmReject}
					class="rounded-lg bg-rose-600 px-3 py-1.5 text-xs font-semibold text-white hover:bg-rose-500 disabled:opacity-50"
				>
					Confirm Reject
				</button>
			</div>
		</div>
	</div>
{/if}

{#if histTarget}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-xl rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
			<div class="flex items-center justify-between border-b border-slate-200 pb-3">
				<h2 class="text-base font-bold text-slate-900 dark:text-white">Sender ID History: {histTarget.sender_id}</h2>
				<button type="button" aria-label="Close modal" onclick={() => histTarget = null} class="text-slate-400">✕</button>
			</div>
			<div class="mt-4 max-h-80 overflow-y-auto">
				<table class="min-w-full divide-y divide-slate-200 text-xs">
					<thead>
						<tr>
							<th class="px-3 py-2 text-left">Ver</th>
							<th class="px-3 py-2 text-left">When</th>
							<th class="px-3 py-2 text-left">Reason</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200">
						{#each sidHistory as h}
							<tr>
								<td class="px-3 py-2 font-mono font-bold">v{h.version}</td>
								<td class="px-3 py-2">{new Date(h.changed_at).toLocaleString()}</td>
								<td class="px-3 py-2">{h.reason}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>
	</div>
{/if}
