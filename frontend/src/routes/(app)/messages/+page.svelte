<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { MessageView, Organization, ProviderConnection } from '$lib/types/api';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let messages = $state<MessageView[]>([]);
	let total = $state(0);
	let page = $state(1);
	const perPage = 20;

	// Filters
	let filterStatus = $state('');
	let filterDestination = $state('');
	let filterSenderID = $state('');
	let filterCustOrg = $state('');
	let filterConn = $state('');

	// Dropdown data for platform operators
	let customers = $state<Organization[]>([]);
	let connections = $state<ProviderConnection[]>([]);

	// Test Send Modal
	let showTestModal = $state(false);
	let testCustOrg = $state('');
	let testDest = $state('');
	let testSender = $state('VERIFY');
	let testText = $state('Your verification code is 123456. Valid for 10 minutes.');
	let testSending = $state(false);
	let testSuccess = $state<string | null>(null);
	let testError = $state<string | null>(null);

	const isPlatform = $derived(auth.tenant?.actor_org_type === 'platform');
	const canTestSend = $derived(auth.hasPermission('message.platform.manage') || isPlatform);

	async function loadMessages() {
		loading = true;
		error = null;
		try {
			const res = await apiClient.listMessages({
				page,
				per_page: perPage,
				status: filterStatus || undefined,
				destination: filterDestination || undefined,
				sender_id: filterSenderID || undefined,
				customer_org_id: filterCustOrg || undefined,
				provider_connection_id: filterConn || undefined
			});
			messages = res.data.items || [];
			total = res.data.total || 0;
		} catch (err: any) {
			error = err.message || 'Failed to load messages';
		} finally {
			loading = false;
		}
	}

	async function loadDropdowns() {
		if (!isPlatform) return;
		try {
			const [orgsRes, connsRes] = await Promise.all([
				auth.hasPermission('org.organization.view')
					? apiClient.listOrganizations({ page: 1, pageSize: 100 })
					: Promise.resolve({ data: { items: [] as Organization[] } }),
				auth.hasPermission('provider.connection.view')
					? apiClient.listAllConnections()
					: Promise.resolve({ data: [] as ProviderConnection[] })
			]);
			customers = (orgsRes.data.items || []).filter((o: Organization) => o.type === 'customer');
			connections = connsRes.data || [];
		} catch {
			// Non-blocking
		}
	}

	async function handleTestSend(e: SubmitEvent) {
		e.preventDefault();
		testSending = true;
		testSuccess = null;
		testError = null;

		try {
			const res = await apiClient.testSendMessage({
				customer_org_id: testCustOrg,
				destination: testDest,
				sender_id: testSender,
				text: testText
			});
			testSuccess = `Test message accepted! ID: ${res.data.message_id} (Status: ${res.data.status})`;
			testDest = '';
			loadMessages();
		} catch (err: any) {
			testError = err.message || 'Test send failed';
		} finally {
			testSending = false;
		}
	}

	function handleExport(format: 'csv' | 'json') {
		const q = new URLSearchParams();
		q.set('format', format);
		if (filterStatus) q.set('status', filterStatus);
		if (filterDestination) q.set('destination', filterDestination);
		if (filterSenderID) q.set('sender_id', filterSenderID);
		if (filterCustOrg) q.set('customer_org_id', filterCustOrg);
		if (filterConn) q.set('provider_connection_id', filterConn);

		window.open(`/api/v1/messages/export?${q.toString()}`, '_blank');
	}

	onMount(() => {
		loadMessages();
		loadDropdowns();
	});

	function getStatusClass(status: string) {
		switch (status) {
			case 'delivered':
				return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300 border border-emerald-200 dark:border-emerald-800';
			case 'submitted':
				return 'bg-sky-50 text-sky-700 dark:bg-sky-950/40 dark:text-sky-300 border border-sky-200 dark:border-sky-800';
			case 'submitting':
			case 'queued':
			case 'accepted':
				return 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/40 dark:text-indigo-300 border border-indigo-200 dark:border-indigo-800';
			case 'unknown':
				return 'bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300 border border-amber-200 dark:border-amber-800';
			case 'undelivered':
			case 'failed':
			case 'rejected':
			case 'expired':
				return 'bg-rose-50 text-rose-700 dark:bg-rose-950/40 dark:text-rose-300 border border-rose-200 dark:border-rose-800';
			default:
				return 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300 border border-slate-200 dark:border-slate-700';
		}
	}
</script>

<svelte:head>
	<title>Messages — Znero</title>
</svelte:head>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-xl font-bold text-slate-900 dark:text-white">Messages Explorer</h1>
			<p class="text-xs text-slate-500 dark:text-slate-400">
				Live transaction ledger for outbound SMS traffic with carrier statuses and margin economics.
			</p>
		</div>

		<div class="flex flex-wrap items-center gap-2">
			<button
				type="button"
				onclick={() => handleExport('csv')}
				class="inline-flex items-center gap-1.5 rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200 dark:hover:bg-slate-700"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 10v6m0 0l-3-3m3 3l3-3m2 8H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z" />
				</svg>
				<span>Export CSV</span>
			</button>

			<button
				type="button"
				onclick={() => handleExport('json')}
				class="inline-flex items-center gap-1.5 rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200 dark:hover:bg-slate-700"
			>
				<span>JSON</span>
			</button>

			{#if canTestSend}
				<button
					type="button"
					onclick={() => (showTestModal = true)}
					class="inline-flex items-center gap-1.5 rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-semibold text-white shadow-sm hover:bg-indigo-700"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 19l9 2-9-18-9 18 9-2zm0 0v-8" />
					</svg>
					<span>Send Test Message</span>
				</button>
			{/if}
		</div>
	</div>

	<!-- Filters Strip -->
	<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
		<div class="grid grid-cols-1 gap-3 sm:grid-cols-2 md:grid-cols-3 lg:grid-cols-5">
			<!-- Status -->
			<div>
				<label for="filter-status" class="block text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:text-slate-400">
					Status
				</label>
				<select
					id="filter-status"
					bind:value={filterStatus}
					onchange={() => { page = 1; loadMessages(); }}
					class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-2.5 py-1.5 text-xs text-slate-900 shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="">All Statuses</option>
					<option value="delivered">Delivered</option>
					<option value="submitted">Submitted (Provider Accept)</option>
					<option value="submitting">Submitting</option>
					<option value="queued">Queued</option>
					<option value="accepted">Accepted</option>
					<option value="unknown">Unknown</option>
					<option value="undelivered">Undelivered</option>
					<option value="failed">Failed</option>
					<option value="expired">Expired</option>
				</select>
			</div>

			<!-- Destination -->
			<div>
				<label for="filter-dest" class="block text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:text-slate-400">
					Destination
				</label>
				<input
					id="filter-dest"
					type="text"
					bind:value={filterDestination}
					onchange={() => { page = 1; loadMessages(); }}
					placeholder="e.g. +12025550199"
					class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-2.5 py-1.5 text-xs text-slate-900 shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				/>
			</div>

			<!-- Sender ID -->
			<div>
				<label for="filter-sender" class="block text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:text-slate-400">
					Sender ID
				</label>
				<input
					id="filter-sender"
					type="text"
					bind:value={filterSenderID}
					onchange={() => { page = 1; loadMessages(); }}
					placeholder="e.g. VERIFY"
					class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-2.5 py-1.5 text-xs text-slate-900 shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				/>
			</div>

			<!-- Customer (Platform Only) -->
			{#if isPlatform}
				<div>
					<label for="filter-cust" class="block text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:text-slate-400">
						Customer Org
					</label>
					<select
						id="filter-cust"
						bind:value={filterCustOrg}
						onchange={() => { page = 1; loadMessages(); }}
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-2.5 py-1.5 text-xs text-slate-900 shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						<option value="">All Customers</option>
						{#each customers as c}
							<option value={c.id}>{c.name}</option>
						{/each}
					</select>
				</div>

				<!-- Provider Connection (Platform Only) -->
				<div>
					<label for="filter-conn" class="block text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:text-slate-400">
						Connection
					</label>
					<select
						id="filter-conn"
						bind:value={filterConn}
						onchange={() => { page = 1; loadMessages(); }}
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-2.5 py-1.5 text-xs text-slate-900 shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						<option value="">All Connections</option>
						{#each connections as conn}
							<option value={conn.id}>{conn.name}</option>
						{/each}
					</select>
				</div>
			{/if}
		</div>
	</div>

	<!-- Messages Table -->
	<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
		{#if loading}
			<div class="p-8 text-center text-xs text-slate-500">Loading messages...</div>
		{:else if error}
			<div class="p-8 text-center text-xs text-rose-600 dark:text-rose-400">{error}</div>
		{:else if messages.length === 0}
			<div class="p-12 text-center">
				<svg class="mx-auto h-8 w-8 text-slate-400" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 12h.01M12 12h.01M16 12h.01M21 12c0 4.418-4.03 8-9 8a9.863 9.863 0 01-4.255-.949L3 20l1.395-3.72C3.512 15.042 3 13.574 3 12c0-4.418 4.03-8 9-8s9 3.582 9 8z" />
				</svg>
				<h3 class="mt-2 text-sm font-semibold text-slate-900 dark:text-white">No messages found</h3>
				<p class="mt-1 text-xs text-slate-500">Try adjusting your filters or dispatch a test message.</p>
			</div>
		{:else}
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50 text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-400">
						<tr>
							<th class="px-4 py-3">Timestamp</th>
							<th class="px-4 py-3">Destination</th>
							<th class="px-4 py-3">Sender</th>
							<th class="px-4 py-3">Status</th>
							<th class="px-4 py-3">Parts</th>
							<th class="px-4 py-3">Sell Price</th>
							{#if isPlatform}
								<th class="px-4 py-3">Provider ID</th>
								<th class="px-4 py-3">Buy Cost</th>
								<th class="px-4 py-3">Margin</th>
							{/if}
							<th class="px-4 py-3 text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
						{#each messages as m}
							<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/50">
								<td class="px-4 py-3 whitespace-nowrap text-slate-600 dark:text-slate-400">
									{new Date(m.created_at).toLocaleString()}
								</td>
								<td class="px-4 py-3 font-mono font-medium text-slate-900 dark:text-white">
									{m.destination}
								</td>
								<td class="px-4 py-3 font-medium text-slate-800 dark:text-slate-200">
									{m.sender_id}
								</td>
								<td class="px-4 py-3 whitespace-nowrap">
									<span class="inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase {getStatusClass(m.status)}">
										{m.status}
									</span>
								</td>
								<td class="px-4 py-3 text-slate-600 dark:text-slate-400">
									{m.total_parts}
								</td>
								<td class="px-4 py-3 font-mono text-slate-900 dark:text-white">
									${m.sell_price} {m.sell_currency}
								</td>
								{#if isPlatform}
									<td class="px-4 py-3 font-mono text-[11px] text-slate-500 dark:text-slate-400">
										{m.provider_message_id || '—'}
									</td>
									<td class="px-4 py-3 font-mono text-slate-600 dark:text-slate-300">
										{m.actual_buy_cost ? `$${m.actual_buy_cost}` : (m.estimated_buy_cost ? `est $${m.estimated_buy_cost}` : '—')}
									</td>
									<td class="px-4 py-3 font-mono font-medium {m.margin && parseFloat(m.margin) >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-600'}">
										{m.margin ? `$${m.margin}` : '—'}
									</td>
								{/if}
								<td class="px-4 py-3 text-right whitespace-nowrap">
									<a
										href="/messages/{m.id}"
										class="inline-flex items-center gap-1 font-semibold text-indigo-600 hover:text-indigo-800 dark:text-indigo-400 dark:hover:text-indigo-300"
									>
										<span>Journey</span>
										<svg class="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
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
			<div class="flex items-center justify-between border-t border-slate-200 px-4 py-3 dark:border-slate-800">
				<span class="text-xs text-slate-500">
					Showing {messages.length} of {total} messages
				</span>
				<div class="flex gap-2">
					<button
						type="button"
						disabled={page <= 1}
						onclick={() => { page--; loadMessages(); }}
						class="rounded border border-slate-300 px-2.5 py-1 text-xs font-semibold text-slate-700 disabled:opacity-50 dark:border-slate-700 dark:text-slate-300"
					>
						Previous
					</button>
					<button
						type="button"
						disabled={page * perPage >= total}
						onclick={() => { page++; loadMessages(); }}
						class="rounded border border-slate-300 px-2.5 py-1 text-xs font-semibold text-slate-700 disabled:opacity-50 dark:border-slate-700 dark:text-slate-300"
					>
						Next
					</button>
				</div>
			</div>
		{/if}
	</div>
</div>

<!-- Send Test Message Modal -->
{#if showTestModal}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/60 p-4 backdrop-blur-xs">
		<div class="w-full max-w-md rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<div class="flex items-center justify-between border-b border-slate-200 pb-3 dark:border-slate-800">
				<h3 class="text-sm font-bold text-slate-900 dark:text-white">Send Test SMS Message</h3>
				<button
					type="button"
					aria-label="Close"
					onclick={() => { showTestModal = false; testSuccess = null; testError = null; }}
					class="text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			<form onsubmit={handleTestSend} class="mt-4 space-y-4">
				{#if testSuccess}
					<div class="rounded-lg bg-emerald-50 p-3 text-xs text-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-300">
						{testSuccess}
					</div>
				{/if}
				{#if testError}
					<div class="rounded-lg bg-rose-50 p-3 text-xs text-rose-800 dark:bg-rose-950/40 dark:text-rose-300">
						{testError}
					</div>
				{/if}

				<div>
					<label for="test-org" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Customer Organization</label>
					<select
						id="test-org"
						bind:value={testCustOrg}
						required
						class="mt-1 block w-full rounded-lg border border-slate-300 px-3 py-2 text-xs text-slate-900 shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						<option value="">Select customer org</option>
						{#each customers as c}
							<option value={c.id}>{c.name}</option>
						{/each}
					</select>
				</div>

				<div>
					<label for="test-dest" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Destination E.164 Number</label>
					<input
						id="test-dest"
						type="text"
						bind:value={testDest}
						required
						placeholder="+12025550199"
						class="mt-1 block w-full rounded-lg border border-slate-300 px-3 py-2 text-xs text-slate-900 shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div>
					<label for="test-sender" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Sender ID</label>
					<input
						id="test-sender"
						type="text"
						bind:value={testSender}
						required
						class="mt-1 block w-full rounded-lg border border-slate-300 px-3 py-2 text-xs text-slate-900 shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div>
					<label for="test-text" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Message Text</label>
					<textarea
						id="test-text"
						bind:value={testText}
						required
						rows="3"
						class="mt-1 block w-full rounded-lg border border-slate-300 px-3 py-2 text-xs text-slate-900 shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					></textarea>
				</div>

				<div class="flex justify-end gap-2 pt-2">
					<button
						type="button"
						onclick={() => (showTestModal = false)}
						class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={testSending}
						class="rounded-lg bg-indigo-600 px-4 py-1.5 text-xs font-semibold text-white shadow-sm hover:bg-indigo-700 disabled:opacity-50"
					>
						{testSending ? 'Dispatching...' : 'Dispatch Message'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}
