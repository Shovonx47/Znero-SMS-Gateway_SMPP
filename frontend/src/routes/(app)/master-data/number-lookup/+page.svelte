<script lang="ts">
	import { apiClient, ApiClientError } from '$lib/api/client';
	import type { BulkLookupResult, NumberLookupResult } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let activeMode = $state<'single' | 'bulk'>('single');

	// Single lookup
	let inputNumber = $state('+1 (202) 555-0199');
	let direction = $state<'inbound' | 'outbound'>('outbound');
	let isSearching = $state(false);
	let singleResult = $state<NumberLookupResult | null>(null);
	let singleError = $state<string | null>(null);

	// Bulk lookup
	let bulkInput = $state('+1 202 555 0100\n+44 7911 123456\n+880 1712 345678\n+49 151 23456789\n01812345678');
	let isBulkSearching = $state(false);
	let bulkResult = $state<BulkLookupResult | null>(null);
	let bulkError = $state<string | null>(null);

	const parsedBulkCount = $derived(
		bulkInput
			.split(/[\n,]/)
			.map((s) => s.trim())
			.filter(Boolean).length
	);

	async function handleSingleLookup(e: SubmitEvent) {
		e.preventDefault();
		if (!inputNumber.trim()) return;
		isSearching = true;
		singleError = null;
		singleResult = null;

		try {
			const res = await apiClient.lookupNumber(inputNumber.trim(), direction);
			singleResult = res.data;
		} catch (err) {
			if (err instanceof ApiClientError) {
				singleError = err.message;
			} else {
				singleError = 'Number lookup failed.';
			}
		} finally {
			isSearching = false;
		}
	}

	async function handleBulkLookup(e: SubmitEvent) {
		e.preventDefault();
		const list = bulkInput
			.split(/[\n,]/)
			.map((s) => s.trim())
			.filter(Boolean);

		if (list.length === 0) return;
		if (list.length > 1000) {
			bulkError = 'Bulk lookup limit is 1,000 numbers per request.';
			return;
		}

		isBulkSearching = true;
		bulkError = null;
		bulkResult = null;

		try {
			const res = await apiClient.bulkLookupNumbers(list, direction);
			bulkResult = res.data;
			toast(`Analyzed ${res.data.total} numbers (${res.data.valid} valid)`, 'success');
		} catch (err) {
			if (err instanceof ApiClientError) {
				bulkError = err.message;
			} else {
				bulkError = 'Bulk number lookup failed.';
			}
		} finally {
			isBulkSearching = false;
		}
	}

	function exportBulkCSV() {
		if (!bulkResult) return;
		const headers = ['Original Number', 'Normalized E164', 'Valid', 'Country', 'ISO2', 'Operator', 'MCC', 'MNC', 'Number Type', 'Matched Prefix'];
		const rows = bulkResult.results.map((r) => [
			`"${r.original_number.replace(/"/g, '""')}"`,
			`"${r.normalized_e164 || ''}"`,
			r.is_valid ? 'true' : 'false',
			`"${r.country?.name || ''}"`,
			`"${r.country?.iso2 || ''}"`,
			`"${r.operator?.name || 'unknown'}"`,
			`"${r.operator?.mcc || ''}"`,
			`"${r.operator?.mnc || ''}"`,
			`"${r.number_type}"`,
			`"${r.matched_prefix || ''}"`
		]);
		const csvContent = [headers.join(','), ...rows.map((row) => row.join(','))].join('\r\n');
		const blob = new Blob([csvContent], { type: 'text/csv;charset=utf-8;' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = `number-lookup-results-${Date.now()}.csv`;
		a.click();
		URL.revokeObjectURL(url);
	}
</script>

<div class="space-y-6">
	<!-- Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Number Intelligence</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				High-performance E.164 normalization, longest-prefix trie matching, country identification, and operator routing.
			</p>
		</div>

		<!-- Mode selector -->
		<div class="flex rounded-lg border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800 p-0.5">
			<button
				type="button"
				onclick={() => activeMode = 'single'}
				class="rounded-md px-3 py-1.5 text-xs font-semibold transition {activeMode === 'single'
					? 'bg-indigo-600 text-white shadow-sm'
					: 'text-slate-600 dark:text-slate-300 hover:text-indigo-600'}"
			>
				Single Lookup
			</button>
			<button
				type="button"
				onclick={() => activeMode = 'bulk'}
				class="rounded-md px-3 py-1.5 text-xs font-semibold transition {activeMode === 'bulk'
					? 'bg-indigo-600 text-white shadow-sm'
					: 'text-slate-600 dark:text-slate-300 hover:text-indigo-600'}"
			>
				Bulk Lookup (Max 1,000)
			</button>
		</div>
	</div>

	{#if activeMode === 'single'}
		<div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
			<!-- Input Card -->
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900 space-y-4">
				<h2 class="text-sm font-bold text-slate-900 dark:text-white">Analyze Phone Number</h2>

				<form onsubmit={handleSingleLookup} class="space-y-4 text-xs">
					<div>
						<label for="lookup-field-1" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Phone Number (Any Format)</label>
						<input
							id="lookup-field-1"
							type="text"
							bind:value={inputNumber}
							required
							placeholder="+1 202 555 0199 or 01712345678"
							class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono text-sm text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>

					<div>
						<span id="lookup-direction-label" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">Direction / Route Scope</span>
						<div class="grid grid-cols-2 gap-2" role="group" aria-labelledby="lookup-direction-label">
							<button
								type="button"
								onclick={() => direction = 'outbound'}
								class="rounded-lg border py-2 font-semibold text-center transition {direction === 'outbound'
									? 'border-indigo-600 bg-indigo-50 text-indigo-700 dark:border-indigo-500 dark:bg-indigo-950/50 dark:text-indigo-300'
									: 'border-slate-200 dark:border-slate-700 text-slate-600 dark:text-slate-400'}"
							>
								Outbound (MT)
							</button>
							<button
								type="button"
								onclick={() => direction = 'inbound'}
								class="rounded-lg border py-2 font-semibold text-center transition {direction === 'inbound'
									? 'border-indigo-600 bg-indigo-50 text-indigo-700 dark:border-indigo-500 dark:bg-indigo-950/50 dark:text-indigo-300'
									: 'border-slate-200 dark:border-slate-700 text-slate-600 dark:text-slate-400'}"
							>
								Inbound (MO)
							</button>
						</div>
					</div>

					{#if singleError}
						<div class="rounded-lg bg-rose-50 p-3 text-rose-700 dark:bg-rose-950/50 dark:text-rose-300 border border-rose-200 dark:border-rose-800">
							{singleError}
						</div>
					{/if}

					<button
						type="submit"
						disabled={isSearching}
						class="w-full rounded-lg bg-indigo-600 py-2.5 font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50 transition"
					>
						{isSearching ? 'Resolving...' : 'Lookup & Normalize'}
					</button>
				</form>
			</div>

			<!-- Result Card -->
			<div class="lg:col-span-2 rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900 space-y-4">
				<h2 class="text-sm font-bold text-slate-900 dark:text-white">Lookup Intelligence Report</h2>

				{#if isSearching}
					<div class="flex justify-center py-16">
						<Loading message="Querying in-memory longest-prefix trie..." />
					</div>
				{:else if !singleResult}
					<div class="flex flex-col items-center justify-center py-16 text-center">
						<svg class="h-12 w-12 text-slate-300 dark:text-slate-700 mb-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M3 5a2 2 0 012-2h3.28a1 1 0 01.948.684l1.498 4.493a1 1 0 01-.502 1.21l-2.257 1.13a11.042 11.042 0 005.516 5.516l1.13-2.257a1 1 0 011.21-.502l4.493 1.498a1 1 0 01.684.949V19a2 2 0 01-2 2h-1C9.716 21 3 14.284 3 6V5z" />
						</svg>
						<p class="text-xs text-slate-500">Enter a phone number on the left to run intelligence analysis.</p>
					</div>
				{:else}
					<div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
						<!-- Normalized E.164 -->
						<div class="rounded-xl border border-indigo-100 bg-indigo-50/50 p-4 dark:border-indigo-900/40 dark:bg-indigo-950/20">
							<div class="text-[11px] font-semibold uppercase text-indigo-700 dark:text-indigo-400">Normalized (E.164)</div>
							<div class="text-xl font-bold font-mono text-indigo-950 dark:text-indigo-200 mt-1">
								{singleResult.normalized_e164 || 'Unparseable'}
							</div>
							<div class="text-[10px] text-slate-500 mt-1 font-mono">Original: {singleResult.original_number}</div>
						</div>

						<!-- Validation Status -->
						<div class="rounded-xl border border-slate-200 bg-slate-50 p-4 dark:border-slate-800 dark:bg-slate-800/40">
							<div class="text-[11px] font-semibold uppercase text-slate-500">Validity & Type</div>
							<div class="flex items-center gap-2 mt-1">
								{#if singleResult.is_valid}
									<span class="inline-flex items-center rounded-full bg-emerald-100 px-2 py-0.5 text-xs font-bold text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300">
										VALID E.164
									</span>
								{:else}
									<span class="inline-flex items-center rounded-full bg-rose-100 px-2 py-0.5 text-xs font-bold text-rose-800 dark:bg-rose-950 dark:text-rose-300">
										INVALID
									</span>
								{/if}
								<span class="rounded bg-slate-200 dark:bg-slate-700 px-2 py-0.5 text-xs font-bold uppercase text-slate-800 dark:text-slate-200">
									{singleResult.number_type}
								</span>
							</div>
						</div>
					</div>

					<!-- Details Grid -->
					<div class="grid grid-cols-1 sm:grid-cols-3 gap-4 pt-2">
						<!-- Country -->
						<div class="rounded-xl border border-slate-200 p-4 dark:border-slate-800 space-y-1">
							<div class="text-[11px] font-semibold uppercase text-slate-400">Matched Country</div>
							{#if singleResult.country}
								<div class="font-bold text-slate-900 dark:text-white flex items-center gap-2">
									<span class="rounded bg-slate-100 dark:bg-slate-800 px-1 py-0.5 font-mono text-xs border border-slate-200 dark:border-slate-700">
										{singleResult.country.iso2}
									</span>
									<span>{singleResult.country.name}</span>
								</div>
								<div class="text-[11px] text-slate-500 font-mono">
									Dial: +{singleResult.country.calling_code} • {singleResult.country.currency_code}
								</div>
							{:else}
								<div class="text-sm font-semibold text-slate-400">Unknown Country</div>
							{/if}
						</div>

						<!-- Operator -->
						<div class="rounded-xl border border-slate-200 p-4 dark:border-slate-800 space-y-1">
							<div class="text-[11px] font-semibold uppercase text-slate-400">Matched Operator</div>
							{#if singleResult.operator}
								<div class="font-bold text-slate-900 dark:text-white">
									{singleResult.operator.name}
								</div>
								<div class="text-[11px] text-slate-500 font-mono">
									MCC: {singleResult.operator.mcc} • MNC: {singleResult.operator.mnc}
								</div>
							{:else}
								<div class="text-sm font-bold text-amber-600 dark:text-amber-400">Unknown</div>
								<div class="text-[11px] text-slate-400">No specific operator prefix matched</div>
							{/if}
						</div>

						<!-- Prefix Match -->
						<div class="rounded-xl border border-slate-200 p-4 dark:border-slate-800 space-y-1">
							<div class="text-[11px] font-semibold uppercase text-slate-400">Longest Prefix Match</div>
							{#if singleResult.matched_prefix}
								<div class="font-mono font-bold text-indigo-600 dark:text-indigo-400 text-sm">
									+{singleResult.matched_prefix}
								</div>
								<div class="text-[11px] text-slate-400">Matched in Trie cache</div>
							{:else}
								<div class="text-sm text-slate-400">—</div>
							{/if}
						</div>
					</div>
				{/if}
			</div>
		</div>
	{:else}
		<!-- Bulk Lookup Mode -->
		<div class="space-y-6">
			<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900 space-y-4">
				<div class="flex items-center justify-between">
					<h2 class="text-sm font-bold text-slate-900 dark:text-white">Bulk Batch Resolution</h2>
					<span class="text-xs font-mono text-slate-500">Numbers queued: {parsedBulkCount} / 1000</span>
				</div>

				<form onsubmit={handleBulkLookup} class="space-y-4 text-xs">
					<div>
						<label for="lookup-field-2" class="block font-medium text-slate-700 dark:text-slate-300 mb-1">
							Enter phone numbers (one per line, up to 1,000 numbers):
						</label>
						<textarea
							id="lookup-field-2"
							rows="6"
							bind:value={bulkInput}
							placeholder="+1 202 555 0100&#10;+44 7911 123456&#10;+880 1712 345678"
							class="w-full rounded-lg border border-slate-300 p-3 font-mono text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						></textarea>
					</div>

					{#if bulkError}
						<div class="rounded-lg bg-rose-50 p-3 text-rose-700 dark:bg-rose-950/50 dark:text-rose-300 border border-rose-200 dark:border-rose-800">
							{bulkError}
						</div>
					{/if}

					<div class="flex items-center justify-between">
						<div class="flex items-center gap-2">
							<span class="text-slate-500 font-medium">Route Scope:</span>
							<select
								bind:value={direction}
								class="rounded-lg border border-slate-300 bg-white px-2.5 py-1.5 text-xs text-slate-900 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							>
								<option value="outbound">Outbound (MT)</option>
								<option value="inbound">Inbound (MO)</option>
							</select>
						</div>

						<button
							type="submit"
							disabled={isBulkSearching || parsedBulkCount === 0 || parsedBulkCount > 1000}
							class="rounded-lg bg-indigo-600 px-5 py-2 font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50 transition"
						>
							{isBulkSearching ? 'Processing Batch...' : `Run Bulk Intelligence (${parsedBulkCount})`}
						</button>
					</div>
				</form>
			</div>

			<!-- Bulk Results Table -->
			{#if bulkResult}
				<div class="rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900 overflow-hidden">
					<div class="flex items-center justify-between border-b border-slate-200 p-4 dark:border-slate-800">
						<div class="flex items-center gap-4 text-xs font-semibold">
							<span>Total: <strong class="font-mono">{bulkResult.total}</strong></span>
							<span class="text-emerald-600">Valid: <strong class="font-mono">{bulkResult.valid}</strong></span>
							<span class="text-rose-600">Invalid: <strong class="font-mono">{bulkResult.invalid}</strong></span>
						</div>

						<button
							type="button"
							onclick={exportBulkCSV}
							class="inline-flex items-center gap-1.5 rounded-lg border border-slate-300 px-3 py-1 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
						>
							<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" />
							</svg>
							<span>Export Results CSV</span>
						</button>
					</div>

					<div class="overflow-x-auto max-h-[500px]">
						<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-left text-xs">
							<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 uppercase font-semibold sticky top-0">
								<tr>
									<th class="px-4 py-3">Original Input</th>
									<th class="px-4 py-3">Normalized E.164</th>
									<th class="px-4 py-3">Status</th>
									<th class="px-4 py-3">Country</th>
									<th class="px-4 py-3">Operator</th>
									<th class="px-4 py-3">Type</th>
									<th class="px-4 py-3">Matched Prefix</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-200 dark:divide-slate-800 text-slate-700 dark:text-slate-300 font-mono text-[11px]">
								{#each bulkResult.results as r}
									<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40">
										<td class="px-4 py-2.5 font-medium">{r.original_number}</td>
										<td class="px-4 py-2.5 font-bold text-indigo-600 dark:text-indigo-400">{r.normalized_e164 || '—'}</td>
										<td class="px-4 py-2.5">
											{#if r.is_valid}
												<span class="rounded bg-emerald-100 text-emerald-800 px-1.5 py-0.5 text-[10px] font-bold">VALID</span>
											{:else}
												<span class="rounded bg-rose-100 text-rose-800 px-1.5 py-0.5 text-[10px] font-bold">INVALID</span>
											{/if}
										</td>
										<td class="px-4 py-2.5 font-sans">
											{r.country ? `${r.country.name} (${r.country.iso2})` : '—'}
										</td>
										<td class="px-4 py-2.5 font-sans">
											{#if r.operator}
												{r.operator.name} ({r.operator.mcc}-{r.operator.mnc})
											{:else}
												<span class="text-amber-600">Unknown</span>
											{/if}
										</td>
										<td class="px-4 py-2.5 uppercase text-[10px]">{r.number_type}</td>
										<td class="px-4 py-2.5 text-indigo-600 font-bold">
											{r.matched_prefix ? `+${r.matched_prefix}` : '—'}
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				</div>
			{/if}
		</div>
	{/if}
</div>
