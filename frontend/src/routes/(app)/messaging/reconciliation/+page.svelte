<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import type { SettlementException, ReconciliationRun, ReconciliationReport } from '$lib/types/api';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let exceptions = $state<SettlementException[]>([]);
	let runs = $state<ReconciliationRun[]>([]);
	let runningAudit = $state(false);
	let auditResult = $state<ReconciliationReport | null>(null);

	async function loadData() {
		loading = true;
		error = null;
		try {
			const [excRes, runsRes] = await Promise.all([
				apiClient.listSettlementExceptions({ resolved: false }),
				apiClient.listReconciliationRuns(10)
			]);
			exceptions = excRes.data.items || [];
			runs = runsRes.data.items || [];
		} catch (err: any) {
			error = err.message || 'Failed to load reconciliation data';
		} finally {
			loading = false;
		}
	}

	async function triggerReconciliation() {
		runningAudit = true;
		auditResult = null;
		try {
			const res = await apiClient.runReconciliation();
			auditResult = res.data;
			loadData();
		} catch (err: any) {
			error = err.message || 'Reconciliation run failed';
		} finally {
			runningAudit = false;
		}
	}

	async function handleResolve(id: string) {
		try {
			await apiClient.resolveSettlementException(id);
			loadData();
		} catch (err: any) {
			alert('Failed to resolve exception: ' + err.message);
		}
	}

	onMount(() => {
		loadData();
	});
</script>

<svelte:head>
	<title>Settlement & Reconciliation — Znero</title>
</svelte:head>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-xl font-bold text-slate-900 dark:text-white">Settlement & Reconciliation</h1>
			<p class="text-xs text-slate-500 dark:text-slate-400">
				Double-entry general ledger reconciliation against message delivery states and exposure audit flags.
			</p>
		</div>

		<button
			type="button"
			disabled={runningAudit}
			onclick={triggerReconciliation}
			class="inline-flex items-center gap-1.5 rounded-lg bg-indigo-600 px-3.5 py-1.5 text-xs font-semibold text-white shadow-sm hover:bg-indigo-700 disabled:opacity-50"
		>
			<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
			</svg>
			<span>{runningAudit ? 'Running Audit...' : 'Run Reconciliation Audit'}</span>
		</button>
	</div>

	<!-- Live Audit Result Alert -->
	{#if auditResult}
		<div class="rounded-xl border border-indigo-200 bg-indigo-50/50 p-6 dark:border-indigo-900 dark:bg-indigo-950/20 text-xs space-y-3">
			<div class="flex items-center justify-between">
				<h3 class="font-bold text-indigo-950 dark:text-indigo-200">Reconciliation Audit Completed</h3>
				<span class="rounded bg-indigo-100 px-2 py-0.5 font-mono font-bold text-indigo-800 dark:bg-indigo-900 dark:text-indigo-300">
					{auditResult.messages_checked} messages scanned
				</span>
			</div>
			<div class="grid grid-cols-2 gap-4 sm:grid-cols-4 font-mono">
				<div>Terminal Count: <strong>{auditResult.terminal_count}</strong></div>
				<div>In-Flight Count: <strong>{auditResult.in_flight_count}</strong></div>
				<div>Unresolved Unknown: <strong>{auditResult.unresolved_unknown}</strong></div>
				<div>Discrepancies: <strong class="{auditResult.discrepancy_count > 0 ? 'text-rose-600' : 'text-emerald-600'}">{auditResult.discrepancy_count}</strong></div>
			</div>
		</div>
	{/if}

	<!-- Unresolved Settlement Exceptions -->
	<div class="rounded-2xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900 overflow-hidden">
		<div class="border-b border-slate-200 px-6 py-4 dark:border-slate-800 flex items-center justify-between">
			<div>
				<h2 class="text-sm font-bold text-slate-900 dark:text-white">Active Settlement Exceptions</h2>
				<p class="text-xs text-slate-500">Unresolved financial exposure flags and conflicting DLR events.</p>
			</div>
			<span class="rounded-full bg-slate-100 px-2.5 py-0.5 text-xs font-bold text-slate-700 dark:bg-slate-800 dark:text-slate-300">
				{exceptions.length} open
			</span>
		</div>

		{#if loading}
			<div class="p-8 text-center text-xs text-slate-500">Loading settlement exceptions...</div>
		{:else if error}
			<div class="p-8 text-center text-xs text-rose-600">{error}</div>
		{:else if exceptions.length === 0}
			<div class="p-12 text-center text-xs text-slate-500">
				No active settlement exceptions. Financial ledger and messaging states are 100% in sync.
			</div>
		{:else}
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50 text-[11px] font-semibold uppercase text-slate-400">
						<tr>
							<th class="px-4 py-3">Created At</th>
							<th class="px-4 py-3">Severity</th>
							<th class="px-4 py-3">Exception Type</th>
							<th class="px-4 py-3">Message ID</th>
							<th class="px-4 py-3">Details</th>
							<th class="px-4 py-3 text-right">Action</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
						{#each exceptions as exc}
							<tr>
								<td class="px-4 py-3 whitespace-nowrap font-mono text-slate-500">
									{new Date(exc.created_at).toLocaleString()}
								</td>
								<td class="px-4 py-3">
									<span class="rounded px-2 py-0.5 text-[10px] font-bold uppercase {exc.severity === 'critical' ? 'bg-rose-100 text-rose-800' : 'bg-amber-100 text-amber-800'}">
										{exc.severity}
									</span>
								</td>
								<td class="px-4 py-3 font-semibold text-slate-900 dark:text-white">
									{exc.exception_type}
								</td>
								<td class="px-4 py-3 font-mono text-[11px]">
									<a href="/messages/{exc.message_id}" class="text-indigo-600 hover:underline">
										{exc.message_id}
									</a>
								</td>
								<td class="px-4 py-3 font-mono text-[11px] text-slate-500 max-w-sm truncate">
									{JSON.stringify(exc.details)}
								</td>
								<td class="px-4 py-3 text-right">
									<button
										type="button"
										onclick={() => handleResolve(exc.id)}
										class="rounded border border-slate-300 px-2 py-1 text-[11px] font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
									>
										Resolve
									</button>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</div>

	<!-- Past Reconciliation Runs -->
	<div class="rounded-2xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900 overflow-hidden">
		<div class="border-b border-slate-200 px-6 py-4 dark:border-slate-800">
			<h2 class="text-sm font-bold text-slate-900 dark:text-white">Past Audit Runs</h2>
			<p class="text-xs text-slate-500">History of automated and on-demand reconciliation executions.</p>
		</div>

		{#if runs.length === 0}
			<div class="p-8 text-center text-xs text-slate-500">No reconciliation runs recorded yet.</div>
		{:else}
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50 text-[11px] font-semibold uppercase text-slate-400">
						<tr>
							<th class="px-4 py-3">Started At</th>
							<th class="px-4 py-3">Completed At</th>
							<th class="px-4 py-3">Messages Checked</th>
							<th class="px-4 py-3">Discrepancies</th>
							<th class="px-4 py-3">Run ID</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
						{#each runs as r}
							<tr>
								<td class="px-4 py-3 font-mono text-slate-600 dark:text-slate-400">
									{new Date(r.started_at).toLocaleString()}
								</td>
								<td class="px-4 py-3 font-mono text-slate-500">
									{r.completed_at ? new Date(r.completed_at).toLocaleTimeString() : 'In progress'}
								</td>
								<td class="px-4 py-3 font-mono font-bold text-slate-900 dark:text-white">
									{r.messages_checked}
								</td>
								<td class="px-4 py-3 font-mono {r.discrepancy_count > 0 ? 'text-rose-600 font-bold' : 'text-emerald-600'}">
									{r.discrepancy_count}
								</td>
								<td class="px-4 py-3 font-mono text-[11px] text-slate-400 truncate max-w-xs">
									{r.id}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</div>
</div>
