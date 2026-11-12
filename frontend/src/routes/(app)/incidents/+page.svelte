<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { i18n } from '$lib/i18n.svelte';
	import type { Incident, IncidentTimeline } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let incidents = $state<Incident[]>([]);
	let selectedIncident = $state<Incident | null>(null);

	// New incident modal
	let showNewModal = $state(false);
	let newTitle = $state('');
	let newSeverity = $state<'low' | 'medium' | 'high' | 'critical'>('medium');
	let newSummary = $state('');
	let isSubmitting = $state(false);

	// Timeline update modal
	let newEventNote = $state('');
	let newEventType = $state('status_change');

	async function loadIncidents() {
		try {
			loading = true;
			error = null;
			const res = await apiClient.getIncidents();
			incidents = res.data.incidents || [];
		} catch (err: any) {
			error = err.message || 'Failed to load incidents';
		} finally {
			loading = false;
		}
	}

	async function handleDeclare() {
		if (!newTitle.trim()) return;
		try {
			isSubmitting = true;
			await apiClient.createIncident({
				title: newTitle,
				severity: newSeverity,
				summary: newSummary
			});
			showNewModal = false;
			newTitle = '';
			newSummary = '';
			await loadIncidents();
		} catch (err: any) {
			alert(err.message || 'Failed to declare incident');
		} finally {
			isSubmitting = false;
		}
	}

	async function updateStatus(inc: Incident, nextStatus: string) {
		try {
			await apiClient.updateIncident(inc.id, { status: nextStatus });
			await loadIncidents();
			if (selectedIncident && selectedIncident.id === inc.id) {
				selectedIncident.status = nextStatus as any;
			}
		} catch (err: any) {
			alert(err.message || 'Failed to update status');
		}
	}

	async function handleAddTimeline() {
		if (!selectedIncident || !newEventNote.trim()) return;
		try {
			await apiClient.addIncidentTimeline(selectedIncident.id, {
				event_type: newEventType,
				notes: newEventNote
			});
			newEventNote = '';
			await loadIncidents();
		} catch (err: any) {
			alert(err.message || 'Failed to append timeline event');
		}
	}

	onMount(() => {
		loadIncidents();
	});
</script>

<svelte:head>
	<title>{i18n.t('nav_incidents')} | {i18n.t('app_title')}</title>
</svelte:head>

<div class="space-y-6 p-6">
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
				{i18n.t('nav_incidents')}
			</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Platform service disruptions, outage investigation timeline, and operational resolutions.
			</p>
		</div>

		<div class="flex items-center gap-2">
			<button
				type="button"
				onclick={() => (showNewModal = true)}
				class="inline-flex items-center gap-1.5 rounded-lg bg-rose-600 px-3 py-1.5 text-xs font-semibold text-white shadow-sm hover:bg-rose-500"
			>
				<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
				</svg>
				<span>{i18n.t('incident_declare')}</span>
			</button>

			<button
				type="button"
				onclick={loadIncidents}
				class="inline-flex items-center gap-1.5 rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-sm hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
			>
				<span>Refresh</span>
			</button>
		</div>
	</div>

	{#if loading}
		<Loading message="Loading incidents log..." />
	{:else if error}
		<ErrorState title="Incidents Error" message={error} retry={loadIncidents} />
	{:else}
		<div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
			<!-- Incident List (2 cols) -->
			<div class="lg:col-span-2 space-y-3">
				{#each incidents as inc}
					<div
						role="button"
						tabindex="0"
						onclick={() => (selectedIncident = inc)}
						onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); selectedIncident = inc; } }}
						class="w-full text-left rounded-xl border p-5 transition shadow-sm cursor-pointer {selectedIncident?.id === inc.id
							? 'border-indigo-500 bg-indigo-50/20 dark:border-indigo-500 dark:bg-indigo-950/20'
							: 'border-slate-200 bg-white hover:border-slate-300 dark:border-slate-800 dark:bg-slate-900'}"
					>
						<div class="flex items-center justify-between">
							<div class="flex items-center gap-2">
								<span class="rounded px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider {inc.severity === 'critical' ? 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-400' : inc.severity === 'high' ? 'bg-orange-100 text-orange-800 dark:bg-orange-950/60 dark:text-orange-400' : 'bg-amber-100 text-amber-800 dark:bg-amber-950/60 dark:text-amber-400'}">
									{inc.severity}
								</span>
								<h3 class="text-sm font-bold text-slate-900 dark:text-white">{inc.title}</h3>
							</div>

							<span class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-semibold {inc.status === 'resolved' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-400' : 'bg-rose-100 text-rose-800 dark:bg-rose-950/50 dark:text-rose-400'}">
								<span class="h-1.5 w-1.5 rounded-full {inc.status === 'resolved' ? 'bg-emerald-500' : 'bg-rose-500 animate-pulse'}"></span>
								{inc.status}
							</span>
						</div>

						<p class="mt-2 text-xs text-slate-600 dark:text-slate-300">{inc.summary || 'No summary provided.'}</p>

						<div class="mt-3 flex items-center justify-between text-[11px] text-slate-400">
							<span>Declared: {new Date(inc.declared_at).toLocaleString()}</span>
							{#if inc.resolved_at}
								<span class="text-emerald-600 dark:text-emerald-400">Resolved: {new Date(inc.resolved_at).toLocaleString()}</span>
							{/if}
						</div>
					</div>
				{:else}
					<div class="rounded-xl border border-slate-200 bg-white p-12 text-center text-sm text-slate-400 dark:border-slate-800 dark:bg-slate-900">
						No platform incidents reported. All systems operational.
					</div>
				{/each}
			</div>

			<!-- Incident Details & Timeline Drawer (1 col) -->
			<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				{#if selectedIncident}
					<div class="space-y-4">
						<div>
							<span class="text-[10px] font-bold uppercase tracking-wider text-slate-400">Incident Details</span>
							<h2 class="text-base font-bold text-slate-900 dark:text-white mt-1">
								{selectedIncident.title}
							</h2>
						</div>

						<!-- Status Transition Actions -->
						<div>
							<div class="text-xs font-semibold text-slate-500 mb-2">Transition Workflow State</div>
							<div class="flex flex-wrap gap-1.5">
								{#each ['open', 'investigating', 'identified', 'monitoring', 'resolved'] as st}
									<button
										type="button"
										onclick={() => selectedIncident && updateStatus(selectedIncident, st)}
										class="rounded px-2.5 py-1 text-xs font-semibold uppercase tracking-wider transition {selectedIncident.status === st
											? 'bg-indigo-600 text-white shadow-sm'
											: 'bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300'}"
									>
										{st}
									</button>
								{/each}
							</div>
						</div>

						<!-- Add Timeline Update -->
						<div class="pt-3 border-t border-slate-200 dark:border-slate-800">
							<div class="text-xs font-semibold text-slate-700 dark:text-slate-300 mb-2">
								Append Timeline Update
							</div>
							<textarea
								bind:value={newEventNote}
								placeholder="Enter incident update / mitigation step..."
								rows={2}
								class="w-full rounded-lg border border-slate-300 p-2 text-xs dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							></textarea>
							<div class="mt-2 flex justify-end">
								<button
									type="button"
									onclick={handleAddTimeline}
									class="rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-semibold text-white hover:bg-indigo-500"
								>
									Post Update
								</button>
							</div>
						</div>
					</div>
				{:else}
					<div class="flex h-48 items-center justify-center text-center text-xs text-slate-400">
						Select an incident to view details and post timeline updates.
					</div>
				{/if}
			</div>
		</div>

		<!-- Declare Modal -->
		{#if showNewModal}
			<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
				<div class="w-full max-w-lg rounded-xl bg-white p-6 shadow-xl dark:bg-slate-900">
					<h3 class="text-base font-bold text-slate-900 dark:text-white">
						{i18n.t('incident_declare')}
					</h3>

					<div class="mt-4 space-y-3">
						<div>
							<label class="text-xs font-semibold text-slate-700 dark:text-slate-300" for="incident-title-input">Incident Title</label>
							<input
								id="incident-title-input"
								type="text"
								bind:value={newTitle}
								placeholder="e.g. Inbound DLR Processing Latency Spike"
								class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-xs dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							/>
						</div>

						<div>
							<label class="text-xs font-semibold text-slate-700 dark:text-slate-300" for="incident-severity-select">Severity Level</label>
							<select
								id="incident-severity-select"
								bind:value={newSeverity}
								class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-xs dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							>
								<option value="low">Low</option>
								<option value="medium">Medium</option>
								<option value="high">High</option>
								<option value="critical">Critical</option>
							</select>
						</div>

						<div>
							<label class="text-xs font-semibold text-slate-700 dark:text-slate-300" for="incident-summary-textarea">Incident Summary & Impact</label>
							<textarea
								id="incident-summary-textarea"
								bind:value={newSummary}
								rows={3}
								placeholder="Describe the initial observed symptoms and affected carrier or customer paths..."
								class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-xs dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							></textarea>
						</div>
					</div>

					<div class="mt-6 flex justify-end gap-2">
						<button
							type="button"
							onclick={() => (showNewModal = false)}
							class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
						>
							Cancel
						</button>
						<button
							type="button"
							onclick={handleDeclare}
							disabled={isSubmitting || !newTitle.trim()}
							class="rounded-lg bg-rose-600 px-3 py-1.5 text-xs font-semibold text-white hover:bg-rose-500 disabled:opacity-50"
						>
							{isSubmitting ? 'Declaring...' : 'Declare Platform Incident'}
						</button>
					</div>
				</div>
			</div>
		{/if}
	{/if}
</div>
