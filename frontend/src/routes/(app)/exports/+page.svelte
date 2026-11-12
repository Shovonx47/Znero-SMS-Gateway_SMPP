<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { i18n } from '$lib/i18n.svelte';
	import type { ExportJob } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let jobs = $state<ExportJob[]>([]);

	// Form state
	let exportType = $state<'messages' | 'delivery_report' | 'financial_report'>('messages');
	let format = $state<'csv' | 'json' | 'xlsx'>('csv');
	let statusFilter = $state('');
	let isSubmitting = $state(false);

	let refreshTimer: ReturnType<typeof setInterval> | null = null;

	async function loadJobs() {
		try {
			error = null;
			const res = await apiClient.getExportJobs({ limit: 50 });
			jobs = res.data.jobs || [];
		} catch (err: any) {
			error = err.message || 'Failed to load export jobs';
		} finally {
			loading = false;
		}
	}

	async function handleSubmit() {
		try {
			isSubmitting = true;
			const filterParams: Record<string, any> = {};
			if (statusFilter) {
				filterParams.status = statusFilter;
			}
			await apiClient.createExportJob({
				export_type: exportType,
				format,
				filter_params: filterParams
			});
			await loadJobs();
		} catch (err: any) {
			alert(err.message || 'Failed to submit export job');
		} finally {
			isSubmitting = false;
		}
	}

	function getDownloadUrl(jobId: string): string {
		return `/api/v1/exports/${jobId}/download`;
	}

	onMount(() => {
		loadJobs();
		// Poll every 4 seconds if jobs are active
		refreshTimer = setInterval(() => {
			const hasActive = jobs.some((j) => j.status === 'pending' || j.status === 'processing');
			if (hasActive) {
				loadJobs();
			}
		}, 4000);
	});

	onDestroy(() => {
		if (refreshTimer) clearInterval(refreshTimer);
	});
</script>

<svelte:head>
	<title>{i18n.t('nav_exports')} | {i18n.t('app_title')}</title>
</svelte:head>

<div class="space-y-6 p-6">
	<div>
		<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
			{i18n.t('nav_exports')}
		</h1>
		<p class="text-sm text-slate-500 dark:text-slate-400">
			Generate and download asynchronous CSV, XLSX, and JSON data extracts with formula injection protection.
		</p>
	</div>

	<!-- Submit Export Job Card -->
	<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
		<h2 class="text-base font-semibold text-slate-900 dark:text-white mb-4">
			{i18n.t('export_create')}
		</h2>

		<form onsubmit={(e) => { e.preventDefault(); handleSubmit(); }} class="grid grid-cols-1 gap-4 sm:grid-cols-4">
			<div>
				<label class="text-xs font-semibold text-slate-700 dark:text-slate-300" for="export-type-select">
					{i18n.t('export_type')}
				</label>
				<select
					id="export-type-select"
					bind:value={exportType}
					class="mt-1 w-full rounded-lg border border-slate-300 bg-white p-2 text-xs dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="messages">Messages Log</option>
					<option value="delivery_report">Delivery Summary</option>
					<option value="financial_report">Financial Breakdown</option>
				</select>
			</div>

			<div>
				<label class="text-xs font-semibold text-slate-700 dark:text-slate-300" for="export-format-select">
					{i18n.t('export_format')}
				</label>
				<select
					id="export-format-select"
					bind:value={format}
					class="mt-1 w-full rounded-lg border border-slate-300 bg-white p-2 text-xs dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="csv">CSV (Sanitized Formula Protected)</option>
					<option value="xlsx">Excel (XLSX Workbook)</option>
					<option value="json">JSON Array</option>
				</select>
			</div>

			<div>
				<label class="text-xs font-semibold text-slate-700 dark:text-slate-300" for="export-status-select">
					Filter by Status
				</label>
				<select
					id="export-status-select"
					bind:value={statusFilter}
					class="mt-1 w-full rounded-lg border border-slate-300 bg-white p-2 text-xs dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				>
					<option value="">All Statuses</option>
					<option value="delivered">Delivered Only</option>
					<option value="failed">Failed Only</option>
					<option value="accepted">Accepted / Queued</option>
				</select>
			</div>

			<div class="flex items-end">
				<button
					type="submit"
					disabled={isSubmitting}
					class="w-full rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50"
				>
					{isSubmitting ? 'Submitting...' : 'Queue Export Job'}
				</button>
			</div>
		</form>
	</div>

	{#if loading}
		<Loading message="Loading export jobs list..." />
	{:else if error}
		<ErrorState title="Export Error" message={error} retry={loadJobs} />
	{:else}
		<!-- Jobs Table -->
		<div class="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="flex items-center justify-between p-4 border-b border-slate-200 dark:border-slate-800">
				<h2 class="text-base font-semibold text-slate-900 dark:text-white">Export Jobs History</h2>
				<button
					type="button"
					onclick={loadJobs}
					class="text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
				>
					Refresh
				</button>
			</div>

			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50/50 text-slate-500 dark:border-slate-800 dark:bg-slate-800/40">
						<tr>
							<th class="py-3 px-4 font-semibold">Job ID</th>
							<th class="py-3 px-4 font-semibold">Report</th>
							<th class="py-3 px-4 font-semibold">Format</th>
							<th class="py-3 px-4 font-semibold">Status</th>
							<th class="py-3 px-4 font-semibold text-right">{i18n.t('export_rows')}</th>
							<th class="py-3 px-4 font-semibold">Created</th>
							<th class="py-3 px-4 font-semibold text-right">Download</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
						{#each jobs as job}
							<tr>
								<td class="py-3 px-4 font-mono text-slate-500 truncate max-w-[100px]" title={job.id}>
									{job.id.substring(0, 8)}...
								</td>
								<td class="py-3 px-4 font-semibold text-slate-900 dark:text-white">
									{job.export_type}
								</td>
								<td class="py-3 px-4">
									<span class="rounded bg-slate-100 px-2 py-0.5 font-mono uppercase text-slate-700 dark:bg-slate-800 dark:text-slate-300">
										{job.format}
									</span>
								</td>
								<td class="py-3 px-4">
									<span class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-semibold {job.status === 'completed' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-400' : job.status === 'failed' ? 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-400' : 'bg-amber-100 text-amber-800 dark:bg-amber-950/60 dark:text-amber-400'}">
										<span class="h-1.5 w-1.5 rounded-full {job.status === 'completed' ? 'bg-emerald-500' : job.status === 'failed' ? 'bg-rose-500' : 'bg-amber-500 animate-pulse'}"></span>
										{job.status}
									</span>
								</td>
								<td class="py-3 px-4 text-right font-mono text-slate-600 dark:text-slate-300">
									{job.row_count.toLocaleString()}
								</td>
								<td class="py-3 px-4 text-slate-500">
									{new Date(job.created_at).toLocaleTimeString()}
								</td>
								<td class="py-3 px-4 text-right">
									{#if job.status === 'completed'}
										<a
											href={getDownloadUrl(job.id)}
											download
											class="inline-flex items-center gap-1 rounded-lg bg-indigo-50 px-2.5 py-1 text-xs font-semibold text-indigo-700 hover:bg-indigo-100 dark:bg-indigo-950/50 dark:text-indigo-300 dark:hover:bg-indigo-900/60"
										>
											<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" />
											</svg>
											<span>{i18n.t('export_download')}</span>
										</a>
									{:else if job.status === 'failed'}
										<span class="text-xs text-rose-500" title={job.error_message || ''}>Failed</span>
									{:else}
										<span class="text-xs text-slate-400">Processing...</span>
									{/if}
								</td>
							</tr>
						{:else}
							<tr>
								<td colspan="7" class="py-6 text-center text-slate-400">
									No export jobs found. Use the form above to generate reports.
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>
	{/if}
</div>
