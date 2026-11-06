<!--
  (app)/dashboard/+page.svelte
  System Status / Infrastructure Health Dashboard — PROTECTED.
  Decision: System Status is authenticated-only. It reveals internal infrastructure
  details (PostgreSQL connection latency, Kafka broker status, migration version,
  schema_info table) that must not be publicly accessible.
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import type { LiveStatus, ReadyReport, SystemInfo, KafkaPublishResult } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import Empty from '$lib/components/Empty.svelte';

	// State
	let loading = $state(true);
	let error = $state<ApiClientError | null>(null);

	let health = $state<LiveStatus | null>(null);
	let readyReport = $state<ReadyReport | null>(null);
	let isDegraded = $state(false);
	let systemInfo = $state<SystemInfo | null>(null);
	let lastChecked = $state<string | null>(null);
	let lastRequestId = $state<string | null>(null);

	// Kafka test message form state
	let testMessage = $state('Phase 1 local health verification message');
	let isPublishing = $state(false);
	let publishResult = $state<KafkaPublishResult | null>(null);
	let publishError = $state<string | null>(null);

	// Auto refresh
	let autoRefresh = $state(false);
	let timer: ReturnType<typeof setInterval> | null = null;

	async function fetchStatus() {
		try {
			error = null;
			const [healthRes, readyRes, sysRes] = await Promise.allSettled([
				apiClient.getHealth(),
				apiClient.getReadiness(),
				apiClient.getSystemInfo()
			]);

			if (healthRes.status === 'fulfilled') {
				health = healthRes.value.data;
				lastRequestId = healthRes.value.requestId;
			} else {
				throw healthRes.reason;
			}

			if (readyRes.status === 'fulfilled') {
				readyReport = readyRes.value.report;
				isDegraded = readyRes.value.isDegraded;
				lastRequestId = readyRes.value.requestId || lastRequestId;
			} else {
				readyReport = null;
			}

			if (sysRes.status === 'fulfilled') {
				systemInfo = sysRes.value.data;
			} else {
				systemInfo = null;
			}

			lastChecked = new Date().toLocaleTimeString();
		} catch (err) {
			if (err instanceof ApiClientError) {
				error = err;
			} else {
				error = new ApiClientError(0, 'unknown_error', err instanceof Error ? err.message : String(err));
			}
		} finally {
			loading = false;
		}
	}

	function toggleAutoRefresh() {
		autoRefresh = !autoRefresh;
		if (autoRefresh) {
			timer = setInterval(fetchStatus, 5000);
		} else if (timer) {
			clearInterval(timer);
			timer = null;
		}
	}

	async function handlePublishKafka(e: SubmitEvent) {
		e.preventDefault();
		if (!testMessage.trim() || isPublishing) return;

		isPublishing = true;
		publishResult = null;
		publishError = null;

		try {
			const res = await apiClient.publishTestKafkaMessage(testMessage.trim());
			publishResult = res.data;
			lastRequestId = res.requestId;
		} catch (err) {
			if (err instanceof ApiClientError) {
				publishError = `${err.code}: ${err.message}`;
			} else {
				publishError = err instanceof Error ? err.message : String(err);
			}
		} finally {
			isPublishing = false;
		}
	}

	onMount(() => {
		fetchStatus();
		return () => {
			if (timer) clearInterval(timer);
		};
	});
</script>

<svelte:head>
	<title>System Status — Znero International</title>
</svelte:head>

<div class="space-y-8">
	<!-- Page Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h2 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
				Infrastructure & Health Dashboard
			</h2>
			<p class="mt-1 text-sm text-slate-500 dark:text-slate-400">
				Real-time connectivity and status of the Znero modular monolith, PostgreSQL pool, and Kafka
				KRaft broker.
			</p>
		</div>

		<div class="flex items-center gap-3">
			<button
				type="button"
				onclick={toggleAutoRefresh}
				class="inline-flex items-center gap-2 rounded-lg border px-3 py-2 text-xs font-medium transition {autoRefresh
					? 'border-indigo-600 bg-indigo-50 text-indigo-700 dark:border-indigo-500 dark:bg-indigo-950/60 dark:text-indigo-300'
					: 'border-slate-300 bg-white text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200 dark:hover:bg-slate-700'}"
			>
				<span class="h-2 w-2 rounded-full {autoRefresh ? 'bg-indigo-600 animate-ping' : 'bg-slate-400'}"></span>
				Auto-Refresh (5s)
			</button>

			<button
				type="button"
				onclick={() => {
					loading = true;
					fetchStatus();
				}}
				disabled={loading}
				class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm transition hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-500 focus:ring-offset-2 disabled:opacity-50 dark:bg-indigo-500 dark:hover:bg-indigo-400"
			>
				<svg class="h-4 w-4 {loading ? 'animate-spin' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
				</svg>
				Refresh Now
			</button>
		</div>
	</div>

	<!-- Status / Error Banner -->
	{#if error}
		<ErrorState
			title="Backend Service Unreachable"
			message={error.message}
			code={error.code}
			requestId={error.requestId}
			retry={() => {
				loading = true;
				fetchStatus();
			}}
		/>
	{/if}

	<!-- Overall Readiness Summary Cards -->
	{#if loading && !health && !readyReport}
		<div class="rounded-xl border border-slate-200 bg-white p-12 dark:border-slate-800 dark:bg-slate-900">
			<Loading message="Inspecting local system infrastructure..." subtext="Querying /healthz, /readyz and /api/v1/system/info" />
		</div>
	{:else}
		<div class="grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-4">
			<!-- Backend Process Card -->
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-xs dark:border-slate-800 dark:bg-slate-900">
				<div class="flex items-center justify-between">
					<span class="text-xs font-medium uppercase tracking-wider text-slate-500 dark:text-slate-400">Process</span>
					<span
						class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-semibold {health?.status === 'ok'
							? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
							: 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-300'}"
					>
						<span class="h-1.5 w-1.5 rounded-full {health?.status === 'ok' ? 'bg-emerald-500' : 'bg-rose-500'}"></span>
						{health?.status === 'ok' ? 'Alive (/healthz)' : 'Unhealthy'}
					</span>
				</div>
				<div class="mt-3">
					<div class="text-xl font-bold text-slate-900 dark:text-white">Go + Fiber API</div>
					<div class="mt-1 text-xs text-slate-500 dark:text-slate-400">
						Uptime: <span class="font-mono font-medium text-slate-700 dark:text-slate-300">{health?.uptime_seconds ?? 0}s</span> | Version: <span class="font-mono">{health?.version ?? 'unknown'}</span>
					</div>
				</div>
			</div>

			<!-- PostgreSQL Status Card -->
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-xs dark:border-slate-800 dark:bg-slate-900">
				<div class="flex items-center justify-between">
					<span class="text-xs font-medium uppercase tracking-wider text-slate-500 dark:text-slate-400">Database</span>
					{#if readyReport?.checks?.database}
						<span
							class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-semibold {readyReport.checks.database.status === 'up'
								? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
								: 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-300'}"
						>
							<span class="h-1.5 w-1.5 rounded-full {readyReport.checks.database.status === 'up' ? 'bg-emerald-500' : 'bg-rose-500'}"></span>
							{readyReport.checks.database.status.toUpperCase()}
						</span>
					{:else}
						<span class="rounded bg-slate-100 px-2 py-0.5 text-xs font-medium text-slate-500 dark:bg-slate-800">UNKNOWN</span>
					{/if}
				</div>
				<div class="mt-3">
					<div class="text-xl font-bold text-slate-900 dark:text-white">PostgreSQL 17</div>
					<div class="mt-1 text-xs text-slate-500 dark:text-slate-400">
						{#if readyReport?.checks?.database}
							Latency: <span class="font-mono font-medium text-slate-700 dark:text-slate-300">{readyReport.checks.database.latency_ms}ms</span>
							{#if readyReport.checks.database.reason}
								<span class="text-rose-600 dark:text-rose-400">({readyReport.checks.database.reason})</span>
							{/if}
						{:else}
							No response received
						{/if}
					</div>
				</div>
			</div>

			<!-- Apache Kafka KRaft Card -->
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-xs dark:border-slate-800 dark:bg-slate-900">
				<div class="flex items-center justify-between">
					<span class="text-xs font-medium uppercase tracking-wider text-slate-500 dark:text-slate-400">Event Broker</span>
					{#if readyReport?.checks?.kafka}
						<span
							class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-semibold {readyReport.checks.kafka.status === 'up'
								? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
								: 'bg-amber-100 text-amber-800 dark:bg-amber-950/60 dark:text-amber-300'}"
						>
							<span class="h-1.5 w-1.5 rounded-full {readyReport.checks.kafka.status === 'up' ? 'bg-emerald-500' : 'bg-amber-500'}"></span>
							{readyReport.checks.kafka.status === 'up' ? 'UP' : 'DOWN (ISOLATED)'}
						</span>
					{:else}
						<span class="rounded bg-slate-100 px-2 py-0.5 text-xs font-medium text-slate-500 dark:bg-slate-800">UNKNOWN</span>
					{/if}
				</div>
				<div class="mt-3">
					<div class="text-xl font-bold text-slate-900 dark:text-white">Kafka 3.9 (KRaft)</div>
					<div class="mt-1 text-xs text-slate-500 dark:text-slate-400">
						{#if readyReport?.checks?.kafka}
							{#if readyReport.checks.kafka.status === 'up'}
								Latency: <span class="font-mono font-medium text-slate-700 dark:text-slate-300">{readyReport.checks.kafka.latency_ms}ms</span>
							{:else}
								Status: <span class="text-amber-700 font-medium dark:text-amber-400">{readyReport.checks.kafka.reason || 'unreachable'}</span>
							{/if}
						{:else}
							Broker offline or unreachable
						{/if}
					</div>
				</div>
			</div>

			<!-- Cluster Readiness Summary Card -->
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-xs dark:border-slate-800 dark:bg-slate-900">
				<div class="flex items-center justify-between">
					<span class="text-xs font-medium uppercase tracking-wider text-slate-500 dark:text-slate-400">Cluster Status</span>
					<span
						class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-bold {readyReport?.status === 'up'
							? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300'
							: 'bg-amber-100 text-amber-800 dark:bg-amber-950/60 dark:text-amber-300'}"
					>
						{readyReport?.status === 'up' ? 'ALL SYSTEMS OPERATIONAL' : 'DEGRADED / PARTIAL'}
					</span>
				</div>
				<div class="mt-3">
					<div class="text-xl font-bold text-slate-900 dark:text-white">
						{readyReport?.status === 'up' ? 'Ready for Traffic' : 'Non-Fatal Degraded'}
					</div>
					<div class="mt-1 text-xs text-slate-500 dark:text-slate-400 truncate">
						Last probe: <span class="font-mono">{lastChecked ?? 'pending'}</span>
					</div>
				</div>
			</div>
		</div>

		<!-- Main 2-column content -->
		<div class="grid grid-cols-1 gap-8 lg:grid-cols-2">
			<!-- Left: Real Kafka Interactive Test Publish -->
			<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900">
				<div class="flex items-center justify-between border-b border-slate-100 pb-4 dark:border-slate-800">
					<div>
						<h3 class="text-base font-semibold text-slate-900 dark:text-white">Kafka Messaging Test</h3>
						<p class="text-xs text-slate-500 dark:text-slate-400">
							Publishes to topic <code class="rounded bg-slate-100 px-1 py-0.5 font-mono dark:bg-slate-800">znero.dev.test</code> and verifies broker delivery.
						</p>
					</div>
					<span class="rounded-md bg-indigo-50 px-2 py-1 text-xs font-medium text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300">
						Producer / Consumer
					</span>
				</div>

				<form onsubmit={handlePublishKafka} class="mt-4 space-y-4">
					<div>
						<label for="kafka-msg" class="block text-xs font-medium text-slate-700 dark:text-slate-300">
							Test Payload
						</label>
						<div class="mt-1">
							<input
								id="kafka-msg"
								type="text"
								bind:value={testMessage}
								maxlength="200"
								placeholder="Enter a test message (max 200 chars)..."
								class="block w-full rounded-lg border border-slate-300 px-3 py-2 text-sm text-slate-900 placeholder-slate-400 shadow-xs focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white dark:placeholder-slate-500"
								required
							/>
						</div>
						<p class="mt-1 text-[11px] text-slate-400 dark:text-slate-500">
							{testMessage.length}/200 characters. Backed by backend <code class="font-mono">POST /api/v1/dev/kafka/test-message</code>.
						</p>
					</div>

					<button
						type="submit"
						disabled={isPublishing || !testMessage.trim()}
						class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm transition hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-500 focus:ring-offset-2 disabled:opacity-50 dark:bg-indigo-500 dark:hover:bg-indigo-400"
					>
						{#if isPublishing}
							<svg class="h-3.5 w-3.5 animate-spin" fill="none" viewBox="0 0 24 24" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
							</svg>
							Publishing...
						{:else}
							<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M14 5l7 7m0 0l-7 7m7-7H3" />
							</svg>
							Send Test Message
						{/if}
					</button>
				</form>

				{#if publishError}
					<div class="mt-4 rounded-lg border border-rose-200 bg-rose-50 p-3 text-xs text-rose-800 dark:border-rose-900/40 dark:bg-rose-950/30 dark:text-rose-300">
						<strong>Publish Failed:</strong> {publishError}
					</div>
				{/if}

				{#if publishResult}
					<div class="mt-4 rounded-lg border border-emerald-200 bg-emerald-50/70 p-4 text-xs dark:border-emerald-900/40 dark:bg-emerald-950/20">
						<div class="flex items-center justify-between font-semibold text-emerald-900 dark:text-emerald-300">
							<span>Message Delivered Successfully</span>
							<span class="font-mono text-[11px] text-emerald-700 dark:text-emerald-400">TOPIC: {publishResult.topic}</span>
						</div>
						<div class="mt-2 space-y-1 font-mono text-[11px] text-slate-700 dark:text-slate-300">
							<div><span class="font-semibold text-slate-500">ID:</span> {publishResult.message.id}</div>
							<div><span class="font-semibold text-slate-500">Payload:</span> "{publishResult.message.message}"</div>
							<div><span class="font-semibold text-slate-500">SentAt:</span> {publishResult.message.sent_at}</div>
						</div>
						<p class="mt-2 text-[10px] text-slate-500 dark:text-slate-400">
							Check backend console logs to see the consumer receive and log this event.
						</p>
					</div>
				{/if}
			</div>

			<!-- Right: PostgreSQL Schema & Migration Status -->
			<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900">
				<div class="flex items-center justify-between border-b border-slate-100 pb-4 dark:border-slate-800">
					<div>
						<h3 class="text-base font-semibold text-slate-900 dark:text-white">Database Schema & Migrations</h3>
						<p class="text-xs text-slate-500 dark:text-slate-400">
							Read from <code class="rounded bg-slate-100 px-1 py-0.5 font-mono dark:bg-slate-800">schema_migrations</code> and <code class="rounded bg-slate-100 px-1 py-0.5 font-mono dark:bg-slate-800">schema_info</code>.
						</p>
					</div>
					<span class="rounded-md bg-slate-100 px-2 py-1 text-xs font-mono font-medium text-slate-700 dark:bg-slate-800 dark:text-slate-300">
						golang-migrate
					</span>
				</div>

				{#if !systemInfo}
					<div class="py-8">
						<Empty title="No Schema Data Available" description="Ensure PostgreSQL is running and migrations have been applied." />
					</div>
				{:else}
					<div class="mt-4 space-y-4">
						<!-- Migration State -->
						<div class="grid grid-cols-2 gap-3 rounded-lg bg-slate-50 p-3 text-xs dark:bg-slate-800/60">
							<div>
								<span class="text-slate-500 dark:text-slate-400">Migration Version:</span>
								<div class="mt-0.5 font-mono text-sm font-bold text-slate-800 dark:text-slate-200">
									v{systemInfo.migration.version}
								</div>
							</div>
							<div>
								<span class="text-slate-500 dark:text-slate-400">Migration Dirty Flag:</span>
								<div class="mt-0.5 font-mono text-sm font-bold {systemInfo.migration.dirty ? 'text-rose-600' : 'text-emerald-600 dark:text-emerald-400'}">
									{systemInfo.migration.dirty ? 'DIRTY (ERROR)' : 'CLEAN (SYNCED)'}
								</div>
							</div>
						</div>

						<!-- Schema info table records -->
						<div>
							<h4 class="text-xs font-semibold uppercase tracking-wider text-slate-500 dark:text-slate-400">
								schema_info Table Records
							</h4>
							<div class="mt-2 overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-800">
								<table class="min-w-full divide-y divide-slate-200 text-left text-xs dark:divide-slate-800">
									<thead class="bg-slate-50 font-medium text-slate-600 dark:bg-slate-800/80 dark:text-slate-300">
										<tr>
											<th scope="col" class="px-3 py-2">Key</th>
											<th scope="col" class="px-3 py-2">Value</th>
											<th scope="col" class="px-3 py-2">Created At</th>
										</tr>
									</thead>
									<tbody class="divide-y divide-slate-200 font-mono dark:divide-slate-800">
										{#each systemInfo.schema as row}
											<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/40">
												<td class="px-3 py-2 font-medium text-indigo-700 dark:text-indigo-400">{row.key}</td>
												<td class="px-3 py-2 text-slate-700 dark:text-slate-300">{row.value}</td>
												<td class="px-3 py-2 text-slate-500 dark:text-slate-400">{row.created_at}</td>
											</tr>
										{/each}
									</tbody>
								</table>
							</div>
						</div>
					</div>
				{/if}
			</div>
		</div>

		<!-- Footer audit / trace info -->
		<div class="flex flex-wrap items-center justify-between rounded-xl border border-slate-200 bg-white px-5 py-3 text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400">
			<div>
				Phase 1 Status: <strong class="text-slate-700 dark:text-slate-300">Foundation Ready</strong> (REST + WebSocket engine prepared)
			</div>
			{#if lastRequestId}
				<div class="font-mono text-[11px]">
					Trace Header: <span class="rounded bg-slate-100 px-1.5 py-0.5 text-slate-700 dark:bg-slate-800 dark:text-slate-300">X-Request-ID: {lastRequestId}</span>
				</div>
			{/if}
		</div>
	{/if}
</div>
