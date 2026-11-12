<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { ImportJob, ImportJobRow } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let activeTab = $state<'upload' | 'jobs'>('upload');

	// Upload form state
	let entityType = $state<'country' | 'operator' | 'prefix' | 'provider_coverage' | 'sender_id'>('country');
	let selectedFile = $state<File | null>(null);
	let isUploading = $state(false);
	let uploadError = $state<string | null>(null);

	// Preview state
	let activeJob = $state<ImportJob | null>(null);
	let jobRows = $state<ImportJobRow[]>([]);
	let isLoadingRows = $state(false);
	let allowPartial = $state(false);
	let isConfirming = $state(false);
	let isRollingBack = $state(false);

	// Job history state
	let jobs = $state<ImportJob[]>([]);
	let totalJobs = $state(0);
	let jobPage = $state(1);
	let jobPageSize = 10;
	let isLoadingJobs = $state(false);

	const isPlatformAdmin = $derived(auth.tenant?.is_platform_admin ?? false);

	async function handleFileSelect(e: Event) {
		const target = e.target as HTMLInputElement;
		if (target.files && target.files.length > 0) {
			selectedFile = target.files[0];
			uploadError = null;
		}
	}

	function handleDrop(e: DragEvent) {
		e.preventDefault();
		if (e.dataTransfer?.files && e.dataTransfer.files.length > 0) {
			selectedFile = e.dataTransfer.files[0];
			uploadError = null;
		}
	}

	async function startUpload() {
		if (!selectedFile) return;
		isUploading = true;
		uploadError = null;
		activeJob = null;
		jobRows = [];

		try {
			const res = await apiClient.uploadImportFile(entityType, selectedFile);
			activeJob = res.data;
			toast(`Uploaded ${selectedFile.name} — Preview generated`, 'success');
			await loadJobRows(activeJob.id);
		} catch (err) {
			if (err instanceof ApiClientError) {
				uploadError = err.message;
			} else {
				uploadError = 'File upload failed. Ensure format is CSV, XLSX, or JSON and size < 50MB.';
			}
		} finally {
			isUploading = false;
		}
	}

	async function loadJobRows(jobId: string) {
		isLoadingRows = true;
		try {
			const res = await apiClient.getImportJobRows(jobId, { pageSize: 50 });
			jobRows = res.data.items || [];
		} catch {
			toast('Failed to load preview rows', 'error');
		} finally {
			isLoadingRows = false;
		}
	}

	async function confirmImport() {
		if (!activeJob) return;
		isConfirming = true;
		try {
			const res = await apiClient.confirmImport(activeJob.id, allowPartial);
			activeJob = res.data;
			toast(`Import executed: ${activeJob.imported_rows} rows imported`, 'success');
			await loadJobs();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Import confirmation failed', 'error');
		} finally {
			isConfirming = false;
		}
	}

	async function rollbackJob(jobId: string) {
		if (!confirm('Are you sure you want to rollback this import? This will restore entities to their pre-import versions.')) {
			return;
		}
		isRollingBack = true;
		try {
			await apiClient.rollbackImport(jobId);
			toast('Import successfully rolled back', 'success');
			if (activeJob && activeJob.id === jobId) {
				activeJob.status = 'rolled_back';
			}
			await loadJobs();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Rollback failed', 'error');
		} finally {
			isRollingBack = false;
		}
	}

	async function downloadErrors(jobId: string) {
		try {
			const blob = await apiClient.downloadImportErrors(jobId);
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = `import-errors-${jobId}.csv`;
			a.click();
			URL.revokeObjectURL(url);
		} catch {
			toast('Failed to download error report', 'error');
		}
	}

	async function loadJobs() {
		isLoadingJobs = true;
		try {
			const res = await apiClient.listImportJobs({ page: jobPage, pageSize: jobPageSize });
			jobs = res.data.items || [];
			totalJobs = res.data.total_count;
		} catch {
			// ignore fallback
		} finally {
			isLoadingJobs = false;
		}
	}

	onMount(() => {
		loadJobs();
	});

	function viewJob(j: ImportJob) {
		activeJob = j;
		activeTab = 'upload';
		loadJobRows(j.id);
	}
</script>

<div class="space-y-6">
	<!-- Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Import Center</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Bulk batch upload reference data (CSV, XLSX, JSON) with strict preview, conflict validation, and rollback.
			</p>
		</div>

		<!-- Tab toggles -->
		<div class="flex rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800 p-0.5">
			<button
				type="button"
				onclick={() => activeTab = 'upload'}
				class="rounded-md px-3 py-1.5 text-xs font-semibold transition {activeTab === 'upload'
					? 'bg-indigo-600 text-white shadow-sm'
					: 'text-slate-600 dark:text-slate-300 hover:text-indigo-600'}"
			>
				Upload & Preview
			</button>
			<button
				type="button"
				onclick={() => { activeTab = 'jobs'; loadJobs(); }}
				class="rounded-md px-3 py-1.5 text-xs font-semibold transition {activeTab === 'jobs'
					? 'bg-indigo-600 text-white shadow-sm'
					: 'text-slate-600 dark:text-slate-300 hover:text-indigo-600'}"
			>
				Job History ({totalJobs})
			</button>
		</div>
	</div>

	{#if !isPlatformAdmin}
		<div class="rounded-xl border border-amber-200 bg-amber-50 p-4 text-xs text-amber-800 dark:border-amber-800 dark:bg-amber-950/40 dark:text-amber-300">
			<strong>Read-Only Notice:</strong> Import and master data mutations are strictly restricted to Platform Administrators.
		</div>
	{/if}

	{#if activeTab === 'upload'}
		<!-- Upload Form -->
		<div class="grid grid-cols-1 md:grid-cols-3 gap-6">
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900 space-y-4">
				<h2 class="text-sm font-bold text-slate-900 dark:text-white">1. Select Entity & File</h2>

				<div>
					<label for="import-field-1" class="block text-xs font-medium text-slate-700 dark:text-slate-300 mb-1">Target Entity Type</label>
					<select
						id="import-field-1"
						bind:value={entityType}
						class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					>
						<option value="country">Countries (ISO-3166-1 / ITU)</option>
						<option value="operator">Operators (MCC / MNC)</option>
						<option value="prefix">Number Prefixes</option>
						<option value="provider_coverage">Provider Coverage (Provider -> Country/Operator)</option>
						<option value="sender_id">Sender IDs (Customer Originator Registry)</option>
					</select>
				</div>

				<!-- Drag and drop dropzone -->
				<div
					role="group"
					aria-label="File drop zone"
					ondragover={(e) => e.preventDefault()}
					ondrop={handleDrop}
					class="flex flex-col items-center justify-center rounded-xl border-2 border-dashed border-slate-300 p-6 text-center hover:border-indigo-400 dark:border-slate-700 transition"
				>
					<svg class="h-8 w-8 text-slate-400 mb-2" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M7 16a4 4 0 01-.88-7.903A5 5 0 1115.9 6L16 6a5 5 0 011 9.9M15 13l-3-3m0 0l-3 3m3-3v12" />
					</svg>
					<p class="text-xs text-slate-600 dark:text-slate-400">
						{selectedFile ? selectedFile.name : 'Drag & drop file here, or click browse'}
					</p>
					<p class="text-[10px] text-slate-400 mt-1">Accepts CSV, XLSX, JSON (Max 50MB)</p>
					<label class="mt-3 inline-flex cursor-pointer items-center rounded-lg bg-slate-100 dark:bg-slate-800 px-3 py-1.5 text-xs font-semibold text-slate-700 dark:text-slate-300 hover:bg-slate-200">
						Browse File
						<input aria-label="Select input filter" type="file" accept=".csv,.xlsx,.json" onchange={handleFileSelect} class="hidden" />
					</label>
				</div>

				{#if uploadError}
					<div class="rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300 border border-rose-200 dark:border-rose-800">
						{uploadError}
					</div>
				{/if}

				<button
					type="button"
					onclick={startUpload}
					disabled={!selectedFile || isUploading || !isPlatformAdmin}
					class="w-full rounded-lg bg-indigo-600 py-2.5 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50 transition"
				>
					{isUploading ? 'Validating & Parsing...' : 'Upload & Generate Preview'}
				</button>
			</div>

			<!-- Pipeline Overview & Summary -->
			<div class="md:col-span-2 rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900 space-y-4">
				<h2 class="text-sm font-bold text-slate-900 dark:text-white">2. Import Preview & Validation Report</h2>

				{#if !activeJob}
					<div class="flex flex-col items-center justify-center py-12 text-center">
						<svg class="h-12 w-12 text-slate-300 dark:text-slate-700 mb-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z" />
						</svg>
						<p class="text-xs text-slate-500">No import preview loaded.</p>
						<p class="text-[11px] text-slate-400 mt-1">Upload a file or choose an existing preview job from Job History.</p>
					</div>
				{:else}
					<!-- Stats Cards -->
					<div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
						<div class="rounded-xl border border-slate-200 bg-slate-50 p-3 dark:border-slate-800 dark:bg-slate-800/40">
							<div class="text-[11px] font-medium text-slate-500">Total Rows</div>
							<div class="text-lg font-bold text-slate-900 dark:text-white font-mono">{activeJob.total_rows}</div>
						</div>
						<div class="rounded-xl border border-emerald-200 bg-emerald-50 p-3 dark:border-emerald-800/50 dark:bg-emerald-950/20">
							<div class="text-[11px] font-medium text-emerald-700 dark:text-emerald-400">Valid Rows</div>
							<div class="text-lg font-bold text-emerald-800 dark:text-emerald-300 font-mono">{activeJob.valid_rows}</div>
						</div>
						<div class="rounded-xl border border-rose-200 bg-rose-50 p-3 dark:border-rose-800/50 dark:bg-rose-950/20">
							<div class="text-[11px] font-medium text-rose-700 dark:text-rose-400">Invalid / Conflicts</div>
							<div class="text-lg font-bold text-rose-800 dark:text-rose-300 font-mono">{activeJob.invalid_rows}</div>
						</div>
						<div class="rounded-xl border border-indigo-200 bg-indigo-50 p-3 dark:border-indigo-800/50 dark:bg-indigo-950/20">
							<div class="text-[11px] font-medium text-indigo-700 dark:text-indigo-400">Status</div>
							<div class="text-sm font-bold uppercase text-indigo-800 dark:text-indigo-300 font-mono">{activeJob.status}</div>
						</div>
					</div>

					<!-- Error report action -->
					{#if activeJob.invalid_rows > 0}
						<div class="flex items-center justify-between rounded-lg border border-rose-200 bg-rose-50 p-3 text-xs text-rose-800 dark:border-rose-800 dark:bg-rose-950/30 dark:text-rose-300">
							<span>Found {activeJob.invalid_rows} row validation issues (duplicates, format errors, or DB conflicts).</span>
							<button
								type="button"
								onclick={() => downloadErrors(activeJob!.id)}
								class="inline-flex items-center gap-1.5 rounded-lg bg-rose-600 px-3 py-1 font-semibold text-white hover:bg-rose-500"
							>
								<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
									<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" />
								</svg>
								<span>Download Error CSV</span>
							</button>
						</div>
					{/if}

					<!-- Confirmation & Execution Bar -->
					{#if activeJob.status === 'preview_ready'}
						<div class="rounded-xl border border-indigo-200 bg-indigo-50/50 p-4 dark:border-indigo-900/60 dark:bg-indigo-950/30 space-y-3">
							<h3 class="text-xs font-bold text-indigo-950 dark:text-indigo-200">3. Confirm & Execute Import</h3>
							<p class="text-xs text-slate-600 dark:text-slate-400">
								All rows imported will automatically be tagged with verification status <span class="font-semibold">unverified</span> and data source <span class="font-semibold">{activeJob.file_name}</span>.
							</p>

							{#if activeJob.invalid_rows > 0}
								<label class="flex items-center gap-2 cursor-pointer text-xs font-medium text-slate-700 dark:text-slate-300">
									<input
										type="checkbox"
										bind:checked={allowPartial}
										class="rounded border-slate-300 text-indigo-600 focus:ring-indigo-500"
									/>
									<span>Allow partial import (import only the {activeJob.valid_rows} valid rows, skip invalid rows)</span>
								</label>
							{/if}

							<div class="flex items-center gap-3 pt-2">
								<button
									type="button"
									onclick={confirmImport}
									disabled={isConfirming || (activeJob.invalid_rows > 0 && !allowPartial) || activeJob.valid_rows === 0 || !isPlatformAdmin}
									class="rounded-lg bg-emerald-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-emerald-500 disabled:opacity-50 transition"
								>
									{isConfirming ? 'Importing Rows...' : `Confirm Import (${allowPartial ? activeJob.valid_rows : activeJob.total_rows} rows)`}
								</button>
							</div>
						</div>
					{:else if activeJob.status === 'completed'}
						<div class="flex items-center justify-between rounded-lg border border-emerald-200 bg-emerald-50 p-3 text-xs text-emerald-800 dark:border-emerald-800 dark:bg-emerald-950/30 dark:text-emerald-300">
							<span>Import completed! Successfully imported {activeJob.imported_rows} rows.</span>
							{#if isPlatformAdmin}
								<button
									type="button"
									onclick={() => rollbackJob(activeJob!.id)}
									disabled={isRollingBack}
									class="rounded-lg border border-emerald-300 bg-white px-3 py-1 font-semibold text-emerald-900 hover:bg-emerald-100 disabled:opacity-50 dark:border-emerald-700 dark:bg-emerald-950 dark:text-emerald-200"
								>
									{isRollingBack ? 'Rolling back...' : 'Rollback This Import'}
								</button>
							{/if}
						</div>
					{/if}

					<!-- Rows Preview Table -->
					<div class="overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-800">
						<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-left text-xs">
							<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 uppercase font-semibold">
								<tr>
									<th class="px-3 py-2 w-16">Row #</th>
									<th class="px-3 py-2">Parsed Content</th>
									<th class="px-3 py-2">Validation Status</th>
									<th class="px-3 py-2">Errors / Details</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-200 dark:divide-slate-800 font-mono text-[11px]">
								{#if isLoadingRows}
									<tr>
										<td colspan="4" class="py-6 text-center text-slate-400">Loading preview rows...</td>
									</tr>
								{:else if jobRows.length === 0}
									<tr>
										<td colspan="4" class="py-6 text-center text-slate-400">No rows found</td>
									</tr>
								{:else}
									{#each jobRows as r}
										<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40">
											<td class="px-3 py-2 text-slate-400">#{r.row_number}</td>
											<td class="px-3 py-2 truncate max-w-xs">{JSON.stringify(r.raw_data)}</td>
											<td class="px-3 py-2">
												{#if r.status === 'valid'}
													<span class="rounded bg-emerald-100 text-emerald-800 px-1.5 py-0.5 text-[10px] font-bold">VALID</span>
												{:else if r.status === 'duplicate'}
													<span class="rounded bg-amber-100 text-amber-800 px-1.5 py-0.5 text-[10px] font-bold">DUPLICATE</span>
												{:else if r.status === 'conflict'}
													<span class="rounded bg-orange-100 text-orange-800 px-1.5 py-0.5 text-[10px] font-bold">CONFLICT</span>
												{:else}
													<span class="rounded bg-rose-100 text-rose-800 px-1.5 py-0.5 text-[10px] font-bold">INVALID</span>
												{/if}
											</td>
											<td class="px-3 py-2 text-rose-600 dark:text-rose-400">
												{#if r.errors && r.errors.length > 0}
													{r.errors.map((e) => `${e.field}: ${e.message}`).join('; ')}
												{:else}
													<span class="text-slate-400">—</span>
												{/if}
											</td>
										</tr>
									{/each}
								{/if}
							</tbody>
						</table>
					</div>
				{/if}
			</div>
		</div>
	{:else}
		<!-- Job History Tab -->
		{#if isLoadingJobs}
			<div class="flex justify-center py-16">
				<Loading message="Loading import jobs..." />
			</div>
		{:else if jobs.length === 0}
			<Empty
				title="No import jobs yet"
				description="No batch imports have been uploaded."
			/>
		{:else}
			<div class="overflow-x-auto rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-left text-xs">
					<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 uppercase font-semibold">
						<tr>
							<th class="px-4 py-3">File Name</th>
							<th class="px-4 py-3">Entity Type</th>
							<th class="px-4 py-3">Status</th>
							<th class="px-4 py-3">Rows (Tot / Valid / Inv / Imp)</th>
							<th class="px-4 py-3">Uploaded At</th>
							<th class="px-4 py-3 text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800 text-slate-700 dark:text-slate-300">
						{#each jobs as j}
							<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40 transition">
								<td class="px-4 py-3 font-semibold text-slate-900 dark:text-white">
									{j.file_name}
									<div class="text-[10px] text-slate-400 font-mono">{(j.file_size / 1024).toFixed(1)} KB</div>
								</td>
								<td class="px-4 py-3 uppercase font-medium">
									{j.entity_type}
								</td>
								<td class="px-4 py-3">
									{#if j.status === 'completed'}
										<span class="inline-flex items-center rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-semibold text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300">
											Completed
										</span>
									{:else if j.status === 'preview_ready'}
										<span class="inline-flex items-center rounded-full bg-indigo-100 px-2 py-0.5 text-[10px] font-semibold text-indigo-800 dark:bg-indigo-950/60 dark:text-indigo-300">
											Preview Ready
										</span>
									{:else if j.status === 'rolled_back'}
										<span class="inline-flex items-center rounded-full bg-rose-100 px-2 py-0.5 text-[10px] font-semibold text-rose-800 dark:bg-rose-950/60 dark:text-rose-300">
											Rolled Back
										</span>
									{:else}
										<span class="inline-flex items-center rounded-full bg-slate-100 px-2 py-0.5 text-[10px] font-semibold text-slate-700">
											{j.status}
										</span>
									{/if}
								</td>
								<td class="px-4 py-3 font-mono">
									{j.total_rows} / <span class="text-emerald-600">{j.valid_rows}</span> / <span class="text-rose-600">{j.invalid_rows}</span> / <span class="font-bold text-indigo-600">{j.imported_rows}</span>
								</td>
								<td class="px-4 py-3 text-slate-500">
									{new Date(j.created_at).toLocaleString()}
								</td>
								<td class="px-4 py-3 text-right">
									<div class="flex items-center justify-end gap-2">
										<button
											type="button"
											onclick={() => viewJob(j)}
											class="rounded px-2 py-1 text-indigo-600 hover:bg-indigo-50 font-semibold"
										>
											View
										</button>

										{#if j.invalid_rows > 0}
											<button
												type="button"
												onclick={() => downloadErrors(j.id)}
												class="rounded px-2 py-1 text-rose-600 hover:bg-rose-50 font-semibold"
											>
												Errors
											</button>
										{/if}

										{#if j.status === 'completed' && isPlatformAdmin}
											<button
												type="button"
												onclick={() => rollbackJob(j.id)}
												class="rounded px-2 py-1 text-slate-500 hover:bg-slate-100 font-semibold"
											>
												Rollback
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
	{/if}
</div>
