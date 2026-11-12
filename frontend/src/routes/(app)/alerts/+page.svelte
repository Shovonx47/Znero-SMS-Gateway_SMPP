<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { i18n } from '$lib/i18n.svelte';
	import type { AlertRule, FiringAlert } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let rules = $state<AlertRule[]>([]);
	let firing = $state<FiringAlert[]>([]);
	let selectedRule = $state<AlertRule | null>(null);
	let silenceMinutes = $state(60);
	let isSilencing = $state(false);

	async function loadAlerts() {
		try {
			loading = true;
			error = null;
			const [rulesRes, firingRes] = await Promise.all([
				apiClient.getAlertRules(),
				apiClient.getFiringAlerts()
			]);
			rules = rulesRes.data.rules || [];
			firing = firingRes.data.alerts || [];
		} catch (err: any) {
			error = err.message || 'Failed to load alert rules';
		} finally {
			loading = false;
		}
	}

	async function handleSilence() {
		if (!selectedRule) return;
		try {
			isSilencing = true;
			await apiClient.silenceAlertRule(selectedRule.id, silenceMinutes);
			selectedRule = null;
			await loadAlerts();
		} catch (err: any) {
			alert(err.message || 'Failed to silence alert rule');
		} finally {
			isSilencing = false;
		}
	}

	onMount(() => {
		loadAlerts();
	});
</script>

<svelte:head>
	<title>{i18n.t('nav_alerts')} | {i18n.t('app_title')}</title>
</svelte:head>

<div class="space-y-6 p-6">
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
				{i18n.t('nav_alerts')}
			</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Configured threshold rules, firing states, hysteresis durations, and silence controls.
			</p>
		</div>

		<button
			type="button"
			onclick={loadAlerts}
			class="inline-flex items-center gap-1.5 rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-sm transition hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
		>
			<span>Refresh Alerts</span>
		</button>
	</div>

	{#if loading}
		<Loading message="Loading alert rules & telemetry..." />
	{:else if error}
		<ErrorState title="Alerts Error" message={error} retry={loadAlerts} />
	{:else}
		<!-- Firing Alerts Banner -->
		{#if firing.length > 0}
			<div class="rounded-xl border border-rose-300 bg-rose-50 p-4 shadow-sm dark:border-rose-900/50 dark:bg-rose-950/30">
				<div class="flex items-center gap-2">
					<span class="h-2.5 w-2.5 rounded-full bg-rose-600 animate-ping"></span>
					<h2 class="text-sm font-bold text-rose-900 dark:text-rose-200">
						{firing.length} Alert{firing.length > 1 ? 's' : ''} Currently Firing
					</h2>
				</div>
				<div class="mt-3 space-y-2">
					{#each firing as f}
						<div class="flex items-center justify-between rounded-lg bg-white/80 p-3 text-xs shadow-sm dark:bg-slate-900/80">
							<div>
								<span class="font-bold text-rose-700 dark:text-rose-400">[{f.severity.toUpperCase()}]</span>
								<strong class="ml-1 text-slate-900 dark:text-white">{f.rule_name}:</strong>
								<span class="ml-1 text-slate-600 dark:text-slate-300">{f.message}</span>
							</div>
							<div class="flex items-center gap-3">
								<span class="font-mono text-slate-500">Val: {f.current_value.toFixed(2)} (limit: {f.threshold})</span>
								<span class="text-slate-400">{new Date(f.started_at).toLocaleTimeString()}</span>
							</div>
						</div>
					{/each}
				</div>
			</div>
		{/if}

		<!-- Alert Rules Table -->
		<div class="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="p-4 border-b border-slate-200 dark:border-slate-800">
				<h2 class="text-base font-semibold text-slate-900 dark:text-white">Alert Rules Catalog</h2>
			</div>

			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50/50 text-slate-500 dark:border-slate-800 dark:bg-slate-800/40">
						<tr>
							<th class="py-3 px-4 font-semibold">Rule Name</th>
							<th class="py-3 px-4 font-semibold">Metric</th>
							<th class="py-3 px-4 font-semibold">Condition</th>
							<th class="py-3 px-4 font-semibold">Duration</th>
							<th class="py-3 px-4 font-semibold">Severity</th>
							<th class="py-3 px-4 font-semibold">Status</th>
							<th class="py-3 px-4 font-semibold text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
						{#each rules as rule}
							<tr>
								<td class="py-3 px-4">
									<div class="font-semibold text-slate-900 dark:text-white">{rule.name}</div>
									<div class="text-[11px] text-slate-500">{rule.description || ''}</div>
								</td>
								<td class="py-3 px-4 font-mono text-slate-600 dark:text-slate-300">
									{rule.metric_name}
								</td>
								<td class="py-3 px-4 font-mono font-semibold text-slate-700 dark:text-slate-200">
									{rule.condition} {rule.threshold}
								</td>
								<td class="py-3 px-4 text-slate-600 dark:text-slate-400">
									{rule.duration_seconds}s
								</td>
								<td class="py-3 px-4">
									<span class="rounded px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider {rule.severity === 'critical' ? 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-400' : rule.severity === 'error' ? 'bg-orange-100 text-orange-800 dark:bg-orange-950/60 dark:text-orange-400' : 'bg-amber-100 text-amber-800 dark:bg-amber-950/60 dark:text-amber-400'}">
										{rule.severity}
									</span>
								</td>
								<td class="py-3 px-4">
									<span class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-semibold {rule.status === 'firing' ? 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-400' : rule.status === 'silenced' ? 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300' : 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-400'}">
										<span class="h-1.5 w-1.5 rounded-full {rule.status === 'firing' ? 'bg-rose-500 animate-pulse' : rule.status === 'silenced' ? 'bg-slate-400' : 'bg-emerald-500'}"></span>
										{rule.status}
									</span>
								</td>
								<td class="py-3 px-4 text-right">
									<button
										type="button"
										onclick={() => (selectedRule = rule)}
										class="rounded px-2.5 py-1 text-xs font-semibold text-indigo-600 hover:bg-indigo-50 dark:text-indigo-400 dark:hover:bg-indigo-950/50"
									>
										Silence
									</button>
								</td>
							</tr>
						{:else}
							<tr>
								<td colspan="7" class="py-6 text-center text-slate-400">
									No alert rules configured in system.
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>

		<!-- Silence Modal -->
		{#if selectedRule}
			<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
				<div class="w-full max-w-md rounded-xl bg-white p-6 shadow-xl dark:bg-slate-900">
					<h3 class="text-base font-bold text-slate-900 dark:text-white">
						Silence Alert: {selectedRule.name}
					</h3>
					<p class="mt-1 text-xs text-slate-500">
						Temporarily suppress notifications and firing alerts for this rule.
					</p>

					<div class="mt-4">
						<label class="text-xs font-semibold text-slate-700 dark:text-slate-300" for="silence-window-select">
							Silence Window
						</label>
						<select
							id="silence-window-select"
							bind:value={silenceMinutes}
							class="mt-1 w-full rounded-lg border border-slate-300 bg-white p-2 text-xs text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						>
							<option value={15}>15 Minutes</option>
							<option value={60}>1 Hour</option>
							<option value={240}>4 Hours</option>
							<option value={1440}>24 Hours</option>
						</select>
					</div>

					<div class="mt-6 flex justify-end gap-2">
						<button
							type="button"
							onclick={() => (selectedRule = null)}
							class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
						>
							Cancel
						</button>
						<button
							type="button"
							onclick={handleSilence}
							disabled={isSilencing}
							class="rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
						>
							{isSilencing ? 'Applying...' : 'Apply Silence'}
						</button>
					</div>
				</div>
			</div>
		{/if}
	{/if}
</div>
