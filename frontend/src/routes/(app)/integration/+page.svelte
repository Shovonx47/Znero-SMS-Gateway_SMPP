<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import type { CustomerWebhook, CustomerWebhookDelivery } from '$lib/types/api';

	let activeTab = $state<'overview' | 'webhooks' | 'smpp' | 'limits'>('overview');
	let loading = $state(true);
	let error = $state<string | null>(null);
	let successMsg = $state<string | null>(null);

	// Account & Limits
	let account = $state<any>(null);
	let balance = $state<any>(null);

	// Webhooks
	let webhooks = $state<CustomerWebhook[]>([]);
	let showCreateWebhookModal = $state(false);
	let newWebhookURL = $state('');
	let selectedEvents = $state<string[]>(['message.delivered', 'message.failed']);
	let createdSecret = $state<string | null>(null);

	// Deliveries drawer
	let selectedWebhook = $state<CustomerWebhook | null>(null);
	let deliveries = $state<CustomerWebhookDelivery[]>([]);
	let loadingDeliveries = $state(false);
	let testingWebhookID = $state<string | null>(null);

	const curlSnippet = `curl -X POST https://api.znero.io/api/v1/customer/sms \\
  -H "Authorization: Bearer zn_live_your_api_key" \\
  -H "Content-Type: application/json" \\
  -d '{
    "to": "+14155550199",
    "from": "ZNERO",
    "text": "Your one-time passcode is 492019",
    "client_reference": "ref-order-88192"
  }'`;

	async function loadData() {
		loading = true;
		error = null;
		try {
			const [accRes, balRes, whRes] = await Promise.allSettled([
				apiClient.getCustomerAccount(),
				apiClient.getCustomerBalance(),
				apiClient.listCustomerWebhooks()
			]);

			if (accRes.status === 'fulfilled') account = accRes.value.data;
			if (balRes.status === 'fulfilled') balance = balRes.value.data;
			if (whRes.status === 'fulfilled') webhooks = whRes.value.data || [];
		} catch (err: any) {
			error = err.message || 'Failed to load integration data';
		} finally {
			loading = false;
		}
	}

	async function createWebhook() {
		if (!newWebhookURL.trim()) return;
		error = null;
		successMsg = null;
		try {
			const res = await apiClient.createCustomerWebhook({
				url: newWebhookURL.trim(),
				events: selectedEvents
			});
			webhooks = [res.data, ...webhooks];
			if (res.data.secret) {
				createdSecret = res.data.secret;
			}
			newWebhookURL = '';
			showCreateWebhookModal = false;
			successMsg = 'Webhook endpoint created successfully. Copy the signing secret now!';
		} catch (err: any) {
			error = err.message || 'Failed to create webhook';
		}
	}

	async function deleteWebhook(id: string) {
		if (!confirm('Are you sure you want to delete this webhook endpoint?')) return;
		try {
			await apiClient.deleteCustomerWebhook(id);
			webhooks = webhooks.filter((w) => w.id !== id);
			if (selectedWebhook?.id === id) {
				selectedWebhook = null;
				deliveries = [];
			}
			successMsg = 'Webhook deleted';
		} catch (err: any) {
			error = err.message || 'Failed to delete webhook';
		}
	}

	async function testWebhook(id: string) {
		testingWebhookID = id;
		try {
			const res = await apiClient.testCustomerWebhook(id);
			successMsg = res.data.message || 'Test event dispatched successfully';
			if (selectedWebhook?.id === id) {
				await viewDeliveries(selectedWebhook);
			}
		} catch (err: any) {
			error = err.message || 'Failed to test webhook';
		} finally {
			testingWebhookID = null;
		}
	}

	async function viewDeliveries(wh: CustomerWebhook) {
		selectedWebhook = wh;
		loadingDeliveries = true;
		try {
			const res = await apiClient.listCustomerWebhookDeliveries(wh.id, { per_page: 50 });
			deliveries = res.data.items || [];
		} catch (err: any) {
			error = err.message || 'Failed to load deliveries';
		} finally {
			loadingDeliveries = false;
		}
	}

	function toggleEvent(evt: string) {
		if (selectedEvents.includes(evt)) {
			selectedEvents = selectedEvents.filter((e) => e !== evt);
		} else {
			selectedEvents = [...selectedEvents, evt];
		}
	}

	onMount(() => {
		loadData();
	});
</script>

<svelte:head>
	<title>API & Integration â€” Customer Portal</title>
</svelte:head>

<div class="space-y-6">
	<!-- Page Header -->
	<div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">API & Integration</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Connect your applications via REST API, SMPP 3.4 client, and real-time webhook event dispatchers.
			</p>
		</div>
		<button
			type="button"
			onclick={() => loadData()}
			class="inline-flex items-center gap-2 rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm font-medium text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
		>
			Refresh
		</button>
	</div>

	<!-- Notifications -->
	{#if error}
		<div class="rounded-lg bg-rose-50 p-4 text-sm text-rose-700 dark:bg-rose-950/40 dark:text-rose-300">
			{error}
		</div>
	{/if}
	{#if successMsg}
		<div class="rounded-lg bg-emerald-50 p-4 text-sm text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300">
			{successMsg}
		</div>
	{/if}

	<!-- Secret Revelation Banner -->
	{#if createdSecret}
		<div class="rounded-xl border border-amber-300 bg-amber-50 p-5 dark:border-amber-700 dark:bg-amber-950/40">
			<div class="flex items-start justify-between">
				<div>
					<h3 class="text-sm font-semibold text-amber-900 dark:text-amber-200">
						âš ï¸ Webhook Signing Secret (Write-Only)
					</h3>
					<p class="mt-1 text-xs text-amber-700 dark:text-amber-400">
						This secret is shown only once. Store it securely in your application to verify HMAC signatures (<code class="rounded bg-amber-200/50 px-1 dark:bg-amber-900/50">X-Znero-Signature: t=&lt;ts&gt;,v1=&lt;sig&gt;</code>).
					</p>
					<div class="mt-3 flex items-center gap-2">
						<input aria-label="Generated webhook secret"
							type="text"
							readonly
							value={createdSecret}
							class="w-96 rounded-lg border border-amber-400 bg-white px-3 py-1.5 font-mono text-xs text-slate-800 dark:border-amber-600 dark:bg-slate-900 dark:text-slate-100"
						/>
						<button
							type="button"
							onclick={() => {
								if (createdSecret) navigator.clipboard.writeText(createdSecret);
							}}
							class="rounded-lg bg-amber-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-amber-700"
						>
							Copy
						</button>
					</div>
				</div>
				<button
					type="button"
					onclick={() => (createdSecret = null)}
					class="text-amber-700 hover:text-amber-900 dark:text-amber-400"
				>
					âœ•
				</button>
			</div>
		</div>
	{/if}

	<!-- Tabs Navigation -->
	<div class="border-b border-slate-200 dark:border-slate-800">
		<nav class="-mb-px flex gap-6">
			<button
				type="button"
				onclick={() => (activeTab = 'overview')}
				class="border-b-2 py-3 text-sm font-medium transition-colors {activeTab === 'overview'
					? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400'
					: 'border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-400'}"
			>
				HTTP REST API
			</button>
			<button
				type="button"
				onclick={() => (activeTab = 'smpp')}
				class="border-b-2 py-3 text-sm font-medium transition-colors {activeTab === 'smpp'
					? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400'
					: 'border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-400'}"
			>
				SMPP 3.4 Server
			</button>
			<button
				type="button"
				onclick={() => (activeTab = 'webhooks')}
				class="border-b-2 py-3 text-sm font-medium transition-colors {activeTab === 'webhooks'
					? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400'
					: 'border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-400'}"
			>
				Webhooks ({webhooks.length})
			</button>
			<button
				type="button"
				onclick={() => (activeTab = 'limits')}
				class="border-b-2 py-3 text-sm font-medium transition-colors {activeTab === 'limits'
					? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400'
					: 'border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-400'}"
			>
				Usage & TPS Limits
			</button>
		</nav>
	</div>

	<!-- TAB 1: REST API OVERVIEW -->
	{#if activeTab === 'overview'}
		<div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
			<!-- Quick Reference -->
			<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<h2 class="text-base font-semibold text-slate-900 dark:text-white">API Endpoints Reference</h2>
				<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
					All customer endpoints require <code class="font-mono text-indigo-600 dark:text-indigo-400">Authorization: Bearer &lt;API_KEY&gt;</code>.
				</p>

				<div class="mt-4 space-y-3 font-mono text-xs">
					<div class="flex items-center justify-between rounded-lg bg-slate-50 p-2.5 dark:bg-slate-800/60">
						<span class="font-bold text-emerald-600 dark:text-emerald-400">POST</span>
						<span class="text-slate-700 dark:text-slate-300">/api/v1/customer/sms</span>
						<span class="text-slate-400">Single SMS</span>
					</div>
					<div class="flex items-center justify-between rounded-lg bg-slate-50 p-2.5 dark:bg-slate-800/60">
						<span class="font-bold text-emerald-600 dark:text-emerald-400">POST</span>
						<span class="text-slate-700 dark:text-slate-300">/api/v1/customer/sms/bulk</span>
						<span class="text-slate-400">Bulk SMS (&le; 1000)</span>
					</div>
					<div class="flex items-center justify-between rounded-lg bg-slate-50 p-2.5 dark:bg-slate-800/60">
						<span class="font-bold text-indigo-600 dark:text-indigo-400">GET</span>
						<span class="text-slate-700 dark:text-slate-300">/api/v1/customer/sms/:id</span>
						<span class="text-slate-400">Query SMS Status</span>
					</div>
					<div class="flex items-center justify-between rounded-lg bg-slate-50 p-2.5 dark:bg-slate-800/60">
						<span class="font-bold text-indigo-600 dark:text-indigo-400">GET</span>
						<span class="text-slate-700 dark:text-slate-300">/api/v1/customer/balance</span>
						<span class="text-slate-400">Wallet & Holds</span>
					</div>
					<div class="flex items-center justify-between rounded-lg bg-slate-50 p-2.5 dark:bg-slate-800/60">
						<span class="font-bold text-indigo-600 dark:text-indigo-400">GET</span>
						<span class="text-slate-700 dark:text-slate-300">/api/v1/customer/rates</span>
						<span class="text-slate-400">Active Sell Rates</span>
					</div>
					<div class="flex items-center justify-between rounded-lg bg-slate-50 p-2.5 dark:bg-slate-800/60">
						<span class="font-bold text-indigo-600 dark:text-indigo-400">GET</span>
						<span class="text-slate-700 dark:text-slate-300">/api/v1/customer/inbound</span>
						<span class="text-slate-400">Inbound (MO) SMS</span>
					</div>
				</div>
			</div>

			<!-- cURL Quick Start -->
			<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<h2 class="text-base font-semibold text-slate-900 dark:text-white">Single SMS Submission Example</h2>
				<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
					Idempotency is supported via the optional <code class="font-mono text-indigo-600 dark:text-indigo-400">client_reference</code> parameter.
				</p>

				<pre class="mt-4 overflow-x-auto rounded-lg bg-slate-950 p-4 font-mono text-xs text-slate-200"><code>{curlSnippet}</code></pre>
			</div>
		</div>
	{/if}

	<!-- TAB 2: SMPP CREDENTIALS -->
	{#if activeTab === 'smpp'}
		<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-base font-semibold text-slate-900 dark:text-white">Customer SMPP 3.4 Gateway</h2>
			<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
				High-throughput carrier bind connection settings for high-volume transmitter and receiver sessions.
			</p>

			<div class="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 font-mono text-xs">
				<div class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-slate-800 dark:bg-slate-800/40">
					<div class="text-slate-400">HOST</div>
					<div class="mt-1 text-sm font-bold text-slate-800 dark:text-slate-200">smpp.znero.io</div>
				</div>
				<div class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-slate-800 dark:bg-slate-800/40">
					<div class="text-slate-400">PORT (TCP)</div>
					<div class="mt-1 text-sm font-bold text-slate-800 dark:text-slate-200">2776</div>
				</div>
				<div class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-slate-800 dark:bg-slate-800/40">
					<div class="text-slate-400">PORT (TLS)</div>
					<div class="mt-1 text-sm font-bold text-slate-800 dark:text-slate-200">3550</div>
				</div>
				<div class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-slate-800 dark:bg-slate-800/40">
					<div class="text-slate-400">ALLOWED BIND TYPES</div>
					<div class="mt-1 text-sm font-bold text-slate-800 dark:text-slate-200">TX, RX, TRX</div>
				</div>
				<div class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-slate-800 dark:bg-slate-800/40">
					<div class="text-slate-400">ENQUIRE_LINK INTERVAL</div>
					<div class="mt-1 text-sm font-bold text-slate-800 dark:text-slate-200">30 seconds</div>
				</div>
				<div class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-slate-800 dark:bg-slate-800/40">
					<div class="text-slate-400">DLR DELIVERY</div>
					<div class="mt-1 text-sm font-bold text-emerald-600 dark:text-emerald-400">Real-time deliver_sm</div>
				</div>
			</div>
		</div>
	{/if}

	<!-- TAB 3: WEBHOOKS -->
	{#if activeTab === 'webhooks'}
		<div class="space-y-6">
			<div class="flex items-center justify-between">
				<div>
					<h2 class="text-base font-semibold text-slate-900 dark:text-white">Configured Webhook Endpoints</h2>
					<p class="text-xs text-slate-500 dark:text-slate-400">
						Receive HTTP POST notifications when messages transition or inbound SMS arrive.
					</p>
				</div>
				<button
					type="button"
					onclick={() => (showCreateWebhookModal = true)}
					class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-3.5 py-2 text-sm font-medium text-white shadow hover:bg-indigo-700"
				>
					+ Add Webhook Endpoint
				</button>
			</div>

			<!-- Webhooks List Table -->
			<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50 font-medium text-slate-500 dark:border-slate-800 dark:bg-slate-800/60 dark:text-slate-400">
						<tr>
							<th class="px-4 py-3">Endpoint URL</th>
							<th class="px-4 py-3">Subscribed Events</th>
							<th class="px-4 py-3">Status</th>
							<th class="px-4 py-3">Failures</th>
							<th class="px-4 py-3">Created</th>
							<th class="px-4 py-3 text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
						{#if webhooks.length === 0}
							<tr>
								<td colspan="6" class="px-4 py-8 text-center text-slate-400">
									No webhooks configured yet. Click "+ Add Webhook Endpoint" to configure one.
								</td>
							</tr>
						{:else}
							{#each webhooks as wh (wh.id)}
								<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/50">
									<td class="px-4 py-3 font-mono font-medium text-slate-800 dark:text-slate-200">
										{wh.url}
									</td>
									<td class="px-4 py-3">
										<div class="flex flex-wrap gap-1">
											{#each wh.events as evt}
												<span class="rounded bg-slate-100 px-2 py-0.5 font-mono text-[10px] text-slate-600 dark:bg-slate-800 dark:text-slate-300">
													{evt}
												</span>
											{/each}
										</div>
									</td>
									<td class="px-4 py-3">
										<span
											class="inline-flex rounded-full px-2 py-0.5 text-[10px] font-semibold {wh.status ===
											'active'
												? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
												: wh.status === 'disabled_auto'
													? 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-300'
													: 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300'}"
										>
											{wh.status}
										</span>
									</td>
									<td class="px-4 py-3 font-mono text-slate-600 dark:text-slate-400">
										{wh.consecutive_failures}
									</td>
									<td class="px-4 py-3 text-slate-500">
										{new Date(wh.created_at).toLocaleDateString()}
									</td>
									<td class="px-4 py-3 text-right">
										<div class="flex items-center justify-end gap-2">
											<button
												type="button"
												onclick={() => viewDeliveries(wh)}
												class="rounded px-2 py-1 text-xs font-medium text-indigo-600 hover:bg-indigo-50 dark:text-indigo-400 dark:hover:bg-indigo-950/40"
											>
												Deliveries
											</button>
											<button
												type="button"
												disabled={testingWebhookID === wh.id}
												onclick={() => testWebhook(wh.id)}
												class="rounded px-2 py-1 text-xs font-medium text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
											>
												{testingWebhookID === wh.id ? 'Sending...' : 'Test Event'}
											</button>
											<button
												type="button"
												onclick={() => deleteWebhook(wh.id)}
												class="rounded px-2 py-1 text-xs font-medium text-rose-600 hover:bg-rose-50 dark:text-rose-400 dark:hover:bg-rose-950/40"
											>
												Delete
											</button>
										</div>
									</td>
								</tr>
							{/each}
						{/if}
					</tbody>
				</table>
			</div>

			<!-- Delivery History Drawer -->
			{#if selectedWebhook}
				<div class="rounded-xl border border-indigo-200 bg-white p-5 shadow dark:border-indigo-900/60 dark:bg-slate-900">
					<div class="flex items-center justify-between border-b border-slate-100 pb-3 dark:border-slate-800">
						<div>
							<h3 class="text-sm font-semibold text-slate-900 dark:text-white">
								Recent Deliveries for <span class="font-mono text-indigo-600 dark:text-indigo-400">{selectedWebhook.url}</span>
							</h3>
						</div>
						<button
							type="button"
							onclick={() => (selectedWebhook = null)}
							class="text-xs text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
						>
							Close
						</button>
					</div>

					<div class="mt-4">
						{#if loadingDeliveries}
							<div class="py-6 text-center text-xs text-slate-400">Loading delivery attempts...</div>
						{:else if deliveries.length === 0}
							<div class="py-6 text-center text-xs text-slate-400">No delivery attempts logged yet.</div>
						{:else}
							<div class="space-y-2">
								{#each deliveries as d (d.id)}
									<div class="flex items-center justify-between rounded-lg border border-slate-100 bg-slate-50 p-3 text-xs dark:border-slate-800 dark:bg-slate-800/40">
										<div class="flex items-center gap-3">
											<span
												class="rounded px-2 py-0.5 font-mono text-[10px] font-semibold {d.status ===
												'success'
													? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
													: 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-300'}"
											>
												{d.status}
											</span>
											<span class="font-mono text-slate-700 dark:text-slate-300">{d.event_type}</span>
											<span class="text-slate-400">Attempts: {d.attempts}/{d.max_attempts}</span>
										</div>
										<div class="text-slate-400">
											{new Date(d.created_at).toLocaleTimeString()}
										</div>
									</div>
								{/each}
							</div>
						{/if}
					</div>
				</div>
			{/if}
		</div>
	{/if}

	<!-- TAB 4: USAGE & LIMITS -->
	{#if activeTab === 'limits'}
		<div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
			<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="text-xs text-slate-500 dark:text-slate-400">THROUGHPUT RATE LIMIT</div>
				<div class="mt-2 text-3xl font-extrabold text-indigo-600 dark:text-indigo-400">
					{account?.max_tps || 100} <span class="text-sm font-normal text-slate-500">TPS</span>
				</div>
				<p class="mt-2 text-xs text-slate-500">
					Enforced via token bucket on both HTTP REST API and SMPP <code class="font-mono">submit_sm</code>.
				</p>
			</div>

			<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="text-xs text-slate-500 dark:text-slate-400">WALLET AVAILABLE BALANCE</div>
				<div class="mt-2 text-3xl font-extrabold text-emerald-600 dark:text-emerald-400">
					{balance?.available_balance || balance?.cached_balance || '0.00'} <span class="text-sm font-normal text-slate-500">{balance?.currency || 'USD'}</span>
				</div>
				<p class="mt-2 text-xs text-slate-500">
					Credit Limit: {balance?.credit_limit || '0.00'} {balance?.currency || 'USD'}
				</p>
			</div>

			<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="text-xs text-slate-500 dark:text-slate-400">ACTIVE ENCRYPTION</div>
				<div class="mt-2 text-3xl font-extrabold text-slate-800 dark:text-slate-200">
					AES-256-GCM
				</div>
				<p class="mt-2 text-xs text-slate-500">
					All webhook secrets and message bodies are encrypted at rest with hardware KMS keys.
				</p>
			</div>
		</div>
	{/if}

	<!-- Modal: Add Webhook -->
	{#if showCreateWebhookModal}
		<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4">
			<div class="w-full max-w-lg rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
				<h3 class="text-lg font-bold text-slate-900 dark:text-white">Add Webhook Endpoint</h3>
				<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
					Enter a publicly reachable HTTPS URL to receive event payloads.
				</p>

				<div class="mt-4 space-y-4">
					<div>
						<label for="endpoint-url" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Endpoint URL</label>
						<input
							id="endpoint-url"
							type="url"
							bind:value={newWebhookURL}
							placeholder="https://example.com/api/webhooks/sms"
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 text-xs font-mono text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>

					<div>
						<span class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Subscribed Events</span>
						<div class="mt-2 space-y-2">
							<label class="flex items-center gap-2 text-xs text-slate-700 dark:text-slate-300">
								<input
									type="checkbox"
									checked={selectedEvents.includes('message.delivered')}
									onchange={() => toggleEvent('message.delivered')}
									class="rounded border-slate-300 text-indigo-600 focus:ring-indigo-500"
								/>
								<span><code class="font-mono">message.delivered</code> (Successful carrier delivery)</span>
							</label>
							<label class="flex items-center gap-2 text-xs text-slate-700 dark:text-slate-300">
								<input
									type="checkbox"
									checked={selectedEvents.includes('message.failed')}
									onchange={() => toggleEvent('message.failed')}
									class="rounded border-slate-300 text-indigo-600 focus:ring-indigo-500"
								/>
								<span><code class="font-mono">message.failed</code> (Carrier rejection or expiry)</span>
							</label>
							<label class="flex items-center gap-2 text-xs text-slate-700 dark:text-slate-300">
								<input
									type="checkbox"
									checked={selectedEvents.includes('message.inbound')}
									onchange={() => toggleEvent('message.inbound')}
									class="rounded border-slate-300 text-indigo-600 focus:ring-indigo-500"
								/>
								<span><code class="font-mono">message.inbound</code> (Incoming MO SMS to virtual number)</span>
							</label>
						</div>
					</div>
				</div>

				<div class="mt-6 flex justify-end gap-3">
					<button
						type="button"
						onclick={() => (showCreateWebhookModal = false)}
						class="rounded-lg px-4 py-2 text-xs font-medium text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
					>
						Cancel
					</button>
					<button
						type="button"
						onclick={() => createWebhook()}
						class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow hover:bg-indigo-700"
					>
						Save Webhook
					</button>
				</div>
			</div>
		</div>
	{/if}
</div>
