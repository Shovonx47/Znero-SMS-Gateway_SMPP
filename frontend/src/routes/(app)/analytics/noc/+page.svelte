<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { i18n } from '$lib/i18n.svelte';
	import type { NOCDashboard, FiringAlert } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let data = $state<NOCDashboard | null>(null);

	// WebSocket & Live Status
	let ws = $state<WebSocket | null>(null);
	let isWsConnected = $state(false);
	let currentTps = $state(0);
	let pollInterval: ReturnType<typeof setInterval> | null = null;

	async function loadNOC() {
		try {
			error = null;
			const res = await apiClient.getNOCDashboard();
			data = res.data;
			if (!isWsConnected) {
				currentTps = data.current_tps || 0;
			}
		} catch (err: any) {
			error = err.message || 'Failed to load NOC dashboard';
		} finally {
			loading = false;
		}
	}

	function connectWebSocket() {
		if (typeof window === 'undefined') return;

		const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
		const token = localStorage.getItem('znero_access_token') || '';
		const wsUrl = `${protocol}//${window.location.host}/api/v1/ws/live?token=${encodeURIComponent(token)}`;

		try {
			ws = new WebSocket(wsUrl);

			ws.onopen = () => {
				isWsConnected = true;
				// Send authentication frame if needed
				if (token) {
					ws?.send(JSON.stringify({ type: 'auth', token }));
				}
			};

			ws.onmessage = (event) => {
				try {
					const msg = JSON.parse(event.data);
					if (msg.type === 'telemetry' && msg.data) {
						currentTps = msg.data.current_tps || currentTps;
						if (data && msg.data.queued_messages !== undefined) {
							data.total_queued_messages = msg.data.queued_messages;
						}
					}
				} catch (e) {
					// Ignored
				}
			};

			ws.onerror = () => {
				isWsConnected = false;
			};

			ws.onclose = () => {
				isWsConnected = false;
			};
		} catch (e) {
			isWsConnected = false;
		}
	}

	onMount(() => {
		loadNOC();
		connectWebSocket();

		// Resilient fallback: Poll every 5s to keep connections and queue depths fresh
		pollInterval = setInterval(() => {
			loadNOC();
			if (!isWsConnected && ws?.readyState !== WebSocket.CONNECTING) {
				connectWebSocket();
			}
		}, 5000);
	});

	onDestroy(() => {
		if (pollInterval) clearInterval(pollInterval);
		if (ws) {
			try {
				ws.close();
			} catch (e) {}
		}
	});
</script>

<svelte:head>
	<title>{i18n.t('nav_noc')} | {i18n.t('app_title')}</title>
</svelte:head>

<div class="space-y-6 p-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<div class="flex items-center gap-3">
				<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
					{i18n.t('nav_noc')}
				</h1>
				<!-- Live Connection Pill -->
				<span
					class="inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-semibold {isWsConnected
						? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-400'
						: 'bg-amber-100 text-amber-800 dark:bg-amber-950/50 dark:text-amber-400'}"
				>
					<span class="h-2 w-2 rounded-full {isWsConnected ? 'bg-emerald-500 animate-pulse' : 'bg-amber-500'}"></span>
					{isWsConnected ? 'Live Real-time WebSocket' : '5s Polling Fallback'}
				</span>
			</div>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Real-time switching throughput, queue depths, upstream connection latencies, and active alert rules.
			</p>
		</div>

		<button
			type="button"
			onclick={loadNOC}
			class="inline-flex items-center gap-1.5 rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-sm transition hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
		>
			<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
			</svg>
			<span>Refresh Telemetry</span>
		</button>
	</div>

	{#if loading}
		<Loading message="Connecting to NOC telemetry..." />
	{:else if error}
		<ErrorState title="Telemetry Error" message={error} retry={loadNOC} />
	{:else if data}
		<!-- Live Alert Ticker / Firing Alerts Banner -->
		{#if data.recent_alerts && data.recent_alerts.length > 0}
			<div class="rounded-xl border border-rose-300 bg-rose-50 p-4 shadow-sm dark:border-rose-900/50 dark:bg-rose-950/30">
				<div class="flex items-center gap-2">
					<svg class="h-5 w-5 text-rose-600 dark:text-rose-400 animate-bounce" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" />
					</svg>
					<span class="text-sm font-bold text-rose-900 dark:text-rose-200">
						{data.recent_alerts.length} Active System Alerts Firing
					</span>
				</div>
				<div class="mt-2 space-y-1.5 pl-7 text-xs text-rose-800 dark:text-rose-300">
					{#each data.recent_alerts as alert}
						<div class="flex items-center justify-between">
							<span><strong>[{alert.severity.toUpperCase()}]</strong> {alert.message}</span>
							<span class="text-[11px] text-rose-600 dark:text-rose-400">
								Started {new Date(alert.started_at).toLocaleTimeString()}
							</span>
						</div>
					{/each}
				</div>
			</div>
		{/if}

		<!-- Key Telemetry Gauges -->
		<div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
			<!-- Live TPS Meter -->
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="flex items-center justify-between text-xs font-medium text-slate-500">
					<span>{i18n.t('metric_tps')}</span>
					<span class="rounded bg-indigo-50 px-1.5 py-0.5 text-indigo-700 font-bold dark:bg-indigo-950/50 dark:text-indigo-400">REALTIME</span>
				</div>
				<div class="mt-3 flex items-baseline gap-2">
					<span class="text-4xl font-extrabold tracking-tight text-indigo-600 dark:text-indigo-400">
						{currentTps.toLocaleString()}
					</span>
					<span class="text-xs font-semibold text-slate-400">msg/sec</span>
				</div>
				<div class="mt-2 h-2 w-full rounded-full bg-slate-100 overflow-hidden dark:bg-slate-800">
					<div
						class="h-full bg-indigo-600 transition-all duration-500"
						style="width: {Math.min(Math.round((currentTps / 1000) * 100), 100)}%"
					></div>
				</div>
			</div>

			<!-- System Health -->
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="text-xs font-medium text-slate-500">Platform Health</div>
				<div class="mt-3 flex items-center gap-2">
					<span class="h-4 w-4 rounded-full {data.system_health === 'healthy' ? 'bg-emerald-500' : data.system_health === 'degraded' ? 'bg-amber-500' : 'bg-rose-500'}"></span>
					<span class="text-2xl font-bold uppercase tracking-wide {data.system_health === 'healthy' ? 'text-emerald-600 dark:text-emerald-400' : data.system_health === 'degraded' ? 'text-amber-600' : 'text-rose-600'}">
						{data.system_health}
					</span>
				</div>
				<div class="mt-2 text-xs text-slate-500">
					Firing alerts: <strong class="{data.firing_alerts_count > 0 ? 'text-rose-600' : 'text-emerald-600'}">{data.firing_alerts_count}</strong>
				</div>
			</div>

			<!-- Total Queued Messages -->
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="text-xs font-medium text-slate-500">Pipeline Backlog Depth</div>
				<div class="mt-3 text-3xl font-bold tracking-tight text-slate-900 dark:text-white">
					{data.total_queued_messages.toLocaleString()}
				</div>
				<div class="mt-2 text-xs text-slate-500">
					Unprocessed messages in dispatch queue
				</div>
			</div>

			<!-- Active Connections -->
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<div class="text-xs font-medium text-slate-500">{i18n.t('metric_connections')}</div>
				<div class="mt-3 text-3xl font-bold tracking-tight text-slate-900 dark:text-white">
					{data.active_connections_count}
				</div>
				<div class="mt-2 text-xs text-slate-500">
					Active SMPP & HTTP carrier channels
				</div>
			</div>
		</div>

		<!-- Connections Grid -->
		<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="flex items-center justify-between">
				<div>
					<h2 class="text-base font-semibold text-slate-900 dark:text-white">
						Active Carrier Connections Matrix
					</h2>
					<p class="text-xs text-slate-500">Real-time upstream link health, throughput, and queue depth</p>
				</div>
			</div>

			<div class="mt-4 overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 text-slate-500 dark:border-slate-800">
						<tr>
							<th class="pb-2 font-semibold">Connection</th>
							<th class="pb-2 font-semibold">Protocol</th>
							<th class="pb-2 font-semibold">Status</th>
							<th class="pb-2 font-semibold text-right">Live TPS</th>
							<th class="pb-2 font-semibold text-right">Latency</th>
							<th class="pb-2 font-semibold text-right">Queue Depth</th>
							<th class="pb-2 font-semibold text-right">Error Rate</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
						{#each data.connections as conn}
							<tr>
								<td class="py-3 font-semibold text-slate-900 dark:text-white">
									{conn.name}
								</td>
								<td class="py-3">
									<span class="rounded bg-slate-100 px-2 py-0.5 font-mono uppercase text-slate-700 dark:bg-slate-800 dark:text-slate-300">
										{conn.protocol}
									</span>
								</td>
								<td class="py-3">
									<span class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-semibold {conn.status === 'active' || conn.status === 'bound' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-400' : 'bg-rose-100 text-rose-800 dark:bg-rose-950/50 dark:text-rose-400'}">
										<span class="h-1.5 w-1.5 rounded-full {conn.status === 'active' || conn.status === 'bound' ? 'bg-emerald-500' : 'bg-rose-500'}"></span>
										{conn.status}
									</span>
								</td>
								<td class="py-3 text-right font-mono font-bold text-slate-900 dark:text-white">
									{conn.current_tps || 0}
								</td>
								<td class="py-3 text-right font-mono text-slate-600 dark:text-slate-300">
									{(conn.latency_ms || 0).toFixed(0)} ms
								</td>
								<td class="py-3 text-right font-mono text-slate-600 dark:text-slate-300">
									{conn.queue_depth || 0}
								</td>
								<td class="py-3 text-right font-mono font-semibold {(conn.error_rate || 0) > 0.05 ? 'text-rose-600' : 'text-emerald-600'}">
									{((conn.error_rate || 0) * 100).toFixed(1)}%
								</td>
							</tr>
						{:else}
							<tr>
								<td colspan="7" class="py-4 text-center text-slate-400">
									No active provider connections found.
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>
	{/if}
</div>
