<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { Country, Operator, TransformRule, CreateTransformRuleRequest, UpdateTransformRuleRequest, TransformPreviewResult, TransformChainResult } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let rules = $state<TransformRule[]>([]);
	let countries = $state<Country[]>([]);
	let operators = $state<Operator[]>([]);
	let totalCount = $state(0);
	let page = $state(1);
	let pageSize = 15;
	let scopeFilter = $state('');
	let statusFilter = $state('');
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	const canEdit = $derived(auth.hasPermission('number.rule.manage') || auth.hasPermission('number.prefix.update'));

	// Create/Edit modal
	let isModalOpen = $state(false);
	let editingRule = $state<TransformRule | null>(null);
	let formName = $state('');
	let formDesc = $state('');
	let formOperation = $state<'add_prefix' | 'remove_prefix' | 'replace_prefix' | 'add_country_code' | 'remove_country_code'>('add_prefix');
	let formMatchPattern = $state('');
	let formPrefixParam = $state('');
	let formTargetParam = $state('');
	let formScope = $state<'global' | 'country' | 'operator'>('global');
	let formCountryId = $state('');
	let formOperatorId = $state('');
	let formDirection = $state<'inbound' | 'outbound'>('outbound');
	let formPriority = $state(100);
	let isSaving = $state(false);
	let modalError = $state<string | null>(null);

	// Chain Test Tool State
	let testNumber = $state('01712345678');
	let testDirection = $state<'inbound' | 'outbound'>('outbound');
	let isTestingChain = $state(false);
	let chainResult = $state<TransformChainResult | null>(null);

	const countryMap = $derived(new Map(countries.map((c) => [c.id, c])));
	const operatorMap = $derived(new Map(operators.map((o) => [o.id, o])));

	async function loadReferences() {
		try {
			const [cRes, oRes] = await Promise.all([
				apiClient.listCountries({ pageSize: 500 }),
				apiClient.listOperators({ pageSize: 500 })
			]);
			countries = cRes.data.items;
			operators = oRes.data.items;
		} catch {
			// ignore fallback
		}
	}

	async function loadRules() {
		isLoading = true;
		errorMessage = null;

		try {
			const res = await apiClient.listTransformRules({
				scope: scopeFilter || undefined,
				status: statusFilter || undefined,
				page,
				pageSize
			});
			rules = res.data.items;
			totalCount = res.data.total_count;
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to load transformation rules.';
			}
		} finally {
			isLoading = false;
		}
	}

	onMount(async () => {
		await loadReferences();
		await loadRules();
	});

	function openCreateModal() {
		editingRule = null;
		formName = '';
		formDesc = '';
		formOperation = 'add_prefix';
		formMatchPattern = '^0';
		formPrefixParam = '88';
		formTargetParam = '';
		formScope = 'global';
		formCountryId = '';
		formOperatorId = '';
		formDirection = 'outbound';
		formPriority = 100;
		modalError = null;
		isModalOpen = true;
	}

	function openEditModal(r: TransformRule) {
		editingRule = r;
		formName = r.name;
		formDesc = r.description || '';
		formOperation = r.operation as any;
		formMatchPattern = r.match_pattern;
		formPrefixParam = String(r.params?.prefix || '');
		formTargetParam = String(r.params?.replacement || r.params?.target || '');
		formScope = r.scope as any;
		formCountryId = r.country_id || '';
		formOperatorId = r.operator_id || '';
		formDirection = r.direction as any;
		formPriority = r.priority;
		modalError = null;
		isModalOpen = true;
	}

	async function saveRule(e: SubmitEvent) {
		e.preventDefault();
		isSaving = true;
		modalError = null;

		const params: Record<string, unknown> = {};
		if (formOperation === 'add_prefix' || formOperation === 'remove_prefix') {
			params.prefix = formPrefixParam.trim();
		} else if (formOperation === 'replace_prefix') {
			params.prefix = formPrefixParam.trim();
			params.replacement = formTargetParam.trim();
		}

		try {
			if (editingRule) {
				const req: UpdateTransformRuleRequest = {
					name: formName.trim(),
					description: formDesc.trim(),
					operation: formOperation,
					match_pattern: formMatchPattern.trim(),
					params,
					scope: formScope,
					country_id: formCountryId || undefined,
					operator_id: formOperatorId || undefined,
					direction: formDirection,
					priority: Number(formPriority)
				};
				await apiClient.updateTransformRule(editingRule.id, req);
				toast('Transformation rule updated', 'success');
			} else {
				const req: CreateTransformRuleRequest = {
					name: formName.trim(),
					description: formDesc.trim(),
					operation: formOperation,
					match_pattern: formMatchPattern.trim(),
					params,
					scope: formScope,
					country_id: formCountryId || undefined,
					operator_id: formOperatorId || undefined,
					direction: formDirection,
					priority: Number(formPriority)
				};
				await apiClient.createTransformRule(req);
				toast('Transformation rule created', 'success');
			}
			isModalOpen = false;
			await loadRules();
		} catch (err) {
			if (err instanceof ApiClientError) {
				modalError = err.message;
			} else {
				modalError = 'Failed to save transformation rule.';
			}
		} finally {
			isSaving = false;
		}
	}

	async function deleteRule(r: TransformRule) {
		if (!confirm(`Are you sure you want to archive transformation rule "${r.name}"?`)) {
			return;
		}
		try {
			await apiClient.deleteTransformRule(r.id);
			toast('Transformation rule archived', 'success');
			await loadRules();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Delete failed', 'error');
		}
	}

	async function runChainTest(e: SubmitEvent) {
		e.preventDefault();
		if (!testNumber.trim()) return;
		isTestingChain = true;
		chainResult = null;

		try {
			const res = await apiClient.testTransformRuleChain(testNumber.trim(), testDirection);
			chainResult = res.data;
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Chain test failed', 'error');
		} finally {
			isTestingChain = false;
		}
	}
</script>

<div class="space-y-6">
	<!-- Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Transformation Rules</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Deterministic number mutation engine: prefix injection, stripping, replacement, and country code normalization ({totalCount} total).
			</p>
		</div>

		{#if canEdit}
			<button
				type="button"
				onclick={openCreateModal}
				class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-600 focus:ring-offset-2 transition"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
				</svg>
				<span>Add Rule</span>
			</button>
		{/if}
	</div>

	<!-- Chain Test Workbench -->
	<div class="rounded-xl border border-indigo-200 bg-indigo-50/40 p-5 dark:border-indigo-900/60 dark:bg-indigo-950/20 space-y-4">
		<div class="flex items-center justify-between">
			<div>
				<h2 class="text-sm font-bold text-indigo-950 dark:text-indigo-200">Interactive Rule Chain Simulator</h2>
				<p class="text-xs text-slate-600 dark:text-slate-400">
					Test how all active transformation rules execute sequentially in deterministic priority order.
				</p>
			</div>
		</div>

		<form onsubmit={runChainTest} class="flex flex-col sm:flex-row items-center gap-3 text-xs">
			<div class="flex-1 w-full">
				<input
					type="text"
					bind:value={testNumber}
					placeholder="Enter raw phone number to test (e.g. 01712345678)..."
					class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 font-mono text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				/>
			</div>

			<select
				bind:value={testDirection}
				class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
			>
				<option value="outbound">Outbound (MT)</option>
				<option value="inbound">Inbound (MO)</option>
			</select>

			<button
				type="submit"
				disabled={isTestingChain}
				class="rounded-lg bg-indigo-600 px-5 py-2 font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50 transition shrink-0"
			>
				{isTestingChain ? 'Simulating...' : 'Simulate Rule Chain'}
			</button>
		</form>

		{#if chainResult}
			<div class="rounded-lg border border-indigo-200 bg-white p-4 dark:border-indigo-900/80 dark:bg-slate-900 space-y-3">
				<div class="flex items-center justify-between text-xs border-b border-slate-100 pb-2 dark:border-slate-800">
					<span class="text-slate-500">Initial Input: <strong class="font-mono text-slate-900 dark:text-white">{chainResult.original}</strong></span>
					<span class="text-indigo-600 font-bold">Final Transformed: <strong class="font-mono text-indigo-700 dark:text-indigo-400 text-sm">{chainResult.final}</strong></span>
				</div>

				{#if chainResult.steps.length === 0}
					<p class="text-xs text-slate-500 italic py-1">No transformation rules matched this number.</p>
				{:else}
					<div class="space-y-1.5">
						<div class="text-[11px] font-semibold text-slate-400 uppercase">Execution Steps ({chainResult.steps.length}):</div>
						{#each chainResult.steps as step, idx}
							<div class="flex items-center justify-between rounded bg-slate-50 dark:bg-slate-800/60 px-3 py-1.5 text-xs font-mono">
								<span class="font-medium text-slate-700 dark:text-slate-300">
									#{idx + 1} {step.rule_name}
								</span>
								<div class="flex items-center gap-2">
									<span class="text-slate-400">{step.before}</span>
									<span class="text-indigo-500">→</span>
									<span class="font-bold text-indigo-600 dark:text-indigo-400">{step.after}</span>
								</div>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		{/if}
	</div>

	<!-- Filter Bar -->
	<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
		<div class="flex flex-wrap items-center gap-3">
			<select
				bind:value={scopeFilter}
				onchange={() => { page = 1; loadRules(); }}
				class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
			>
				<option value="">All Scopes</option>
				<option value="global">Global</option>
				<option value="country">Country</option>
				<option value="operator">Operator</option>
			</select>

			<select
				bind:value={statusFilter}
				onchange={() => { page = 1; loadRules(); }}
				class="rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
			>
				<option value="">All Statuses</option>
				<option value="active">Active</option>
				<option value="inactive">Inactive</option>
			</select>
		</div>
	</div>

	<!-- Content Table -->
	{#if isLoading}
		<div class="flex justify-center py-16">
			<Loading message="Loading transformation rules..." />
		</div>
	{:else if errorMessage}
		<ErrorState message={errorMessage} retry={loadRules} />
	{:else if rules.length === 0}
		<Empty
			title="No transformation rules configured"
			description="Configure number mutation rules to add, strip, or rewrite prefixes."
		/>
	{:else}
		<div class="overflow-x-auto rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-left text-xs">
				<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 uppercase font-semibold">
					<tr>
						<th class="px-4 py-3">Rule Name</th>
						<th class="px-4 py-3">Operation</th>
						<th class="px-4 py-3">Match Pattern</th>
						<th class="px-4 py-3">Scope & Target</th>
						<th class="px-4 py-3">Direction</th>
						<th class="px-4 py-3">Priority</th>
						<th class="px-4 py-3">Status</th>
						<th class="px-4 py-3 text-right">Actions</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-200 dark:divide-slate-800 text-slate-700 dark:text-slate-300">
					{#each rules as r}
						<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40 transition">
							<td class="px-4 py-3 font-semibold text-slate-900 dark:text-white">
								{r.name}
								{#if r.description}
									<div class="text-[10px] text-slate-400 font-normal">{r.description}</div>
								{/if}
							</td>
							<td class="px-4 py-3 font-mono font-medium text-indigo-600 dark:text-indigo-400">
								{r.operation}
							</td>
							<td class="px-4 py-3 font-mono text-[11px]">
								{r.match_pattern}
							</td>
							<td class="px-4 py-3">
								<span class="rounded bg-slate-100 dark:bg-slate-800 px-1.5 py-0.5 text-[10px] font-semibold uppercase">
									{r.scope}
								</span>
								{#if r.country_id}
									<span class="ml-1 text-[11px] text-slate-500">
										{countryMap.get(r.country_id)?.iso2 || ''}
									</span>
								{/if}
							</td>
							<td class="px-4 py-3 uppercase text-[10px] font-semibold">
								{r.direction}
							</td>
							<td class="px-4 py-3 font-mono font-bold">
								{r.priority}
							</td>
							<td class="px-4 py-3">
								{#if r.status === 'active'}
									<span class="rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-semibold text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300">
										Active
									</span>
								{:else}
									<span class="rounded-full bg-slate-100 px-2 py-0.5 text-[10px] font-semibold text-slate-600 dark:bg-slate-800 dark:text-slate-400">
										Inactive
									</span>
								{/if}
							</td>
							<td class="px-4 py-3 text-right">
								<div class="flex items-center justify-end gap-1">
									{#if canEdit}
										<button
											type="button"
											onclick={() => openEditModal(r)}
											class="rounded p-1 text-slate-400 hover:text-indigo-600 dark:hover:text-indigo-400"
											title="Edit Rule"
										>
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z" />
											</svg>
										</button>

										<button
											type="button"
											onclick={() => deleteRule(r)}
											class="rounded p-1 text-slate-400 hover:text-rose-600 dark:hover:text-rose-400"
											title="Archive Rule"
										>
											<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
												<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
											</svg>
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
</div>

<!-- Create / Edit Modal -->
{#if isModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm overflow-y-auto">
		<div class="w-full max-w-lg rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900 my-8">
			<div class="flex items-center justify-between border-b border-slate-200 pb-4 dark:border-slate-800">
				<h2 class="text-lg font-bold text-slate-900 dark:text-white">
					{editingRule ? `Edit Rule: ${editingRule.name}` : 'Add Transformation Rule'}
				</h2>
				<button
					type="button"
					aria-label="Close dialog"
					onclick={() => isModalOpen = false}
					class="rounded p-1 text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
				>
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			{#if modalError}
				<div class="mt-4 rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300 border border-rose-200 dark:border-rose-800">
					{modalError}
				</div>
			{/if}

			<form onsubmit={saveRule} class="mt-4 space-y-4 text-xs">
				<div>
					<label for="rule-field-1" class="block font-medium text-slate-700 dark:text-slate-300">Rule Name *</label>
					<input
						id="rule-field-1"
						type="text"
						bind:value={formName}
						required
						placeholder="e.g. BD Domestic to E.164 Normalize"
						class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div>
					<label for="rule-field-2" class="block font-medium text-slate-700 dark:text-slate-300">Description</label>
					<input
						id="rule-field-2"
						type="text"
						bind:value={formDesc}
						class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div class="grid grid-cols-2 gap-4">
					<div>
						<label for="rule-field-3" class="block font-medium text-slate-700 dark:text-slate-300">Operation *</label>
						<select
							id="rule-field-3"
							bind:value={formOperation}
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						>
							<option value="add_prefix">Add Prefix</option>
							<option value="remove_prefix">Remove Prefix</option>
							<option value="replace_prefix">Replace Prefix</option>
							<option value="add_country_code">Add Country Code</option>
							<option value="remove_country_code">Remove Country Code</option>
						</select>
					</div>

					<div>
						<label for="rule-field-4" class="block font-medium text-slate-700 dark:text-slate-300">Priority (Higher wins)</label>
						<input
							id="rule-field-4"
							type="number"
							bind:value={formPriority}
							min="1"
							max="1000"
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
				</div>

				<div>
					<label for="rule-field-5" class="block font-medium text-slate-700 dark:text-slate-300">Regex Match Pattern *</label>
					<input
						id="rule-field-5"
						type="text"
						bind:value={formMatchPattern}
						required
						placeholder="e.g. ^01[3-9]\d{8}$ or ^0"
						class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<div class="grid grid-cols-2 gap-4">
					<div>
						<label for="rule-field-6" class="block font-medium text-slate-700 dark:text-slate-300">Prefix / Match Target</label>
						<input
							id="rule-field-6"
							type="text"
							bind:value={formPrefixParam}
							placeholder="e.g. 0 or 88"
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>

					{#if formOperation === 'replace_prefix'}
						<div>
							<label for="rule-field-7" class="block font-medium text-slate-700 dark:text-slate-300">Replacement Prefix</label>
							<input
								id="rule-field-7"
								type="text"
								bind:value={formTargetParam}
								placeholder="e.g. 880"
								class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							/>
						</div>
					{/if}
				</div>

				<div class="grid grid-cols-2 gap-4">
					<div>
						<label for="rule-field-8" class="block font-medium text-slate-700 dark:text-slate-300">Scope</label>
						<select
							id="rule-field-8"
							bind:value={formScope}
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						>
							<option value="global">Global</option>
							<option value="country">Country</option>
							<option value="operator">Operator</option>
						</select>
					</div>

					<div>
						<label for="rule-field-9" class="block font-medium text-slate-700 dark:text-slate-300">Direction</label>
						<select
							id="rule-field-9"
							bind:value={formDirection}
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						>
							<option value="outbound">Outbound (MT)</option>
							<option value="inbound">Inbound (MO)</option>
						</select>
					</div>
				</div>

				{#if formScope === 'country' || formScope === 'operator'}
					<div>
						<label for="rule-field-10" class="block font-medium text-slate-700 dark:text-slate-300">Target Country</label>
						<select
							id="rule-field-10"
							bind:value={formCountryId}
							class="mt-1 w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						>
							<option value="">Select country...</option>
							{#each countries as c}
								<option value={c.id}>{c.name} ({c.iso2})</option>
							{/each}
						</select>
					</div>
				{/if}

				<div class="flex items-center justify-end gap-3 pt-4 border-t border-slate-200 dark:border-slate-800">
					<button
						type="button"
						onclick={() => isModalOpen = false}
						class="rounded-lg border border-slate-300 px-4 py-2 font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={isSaving}
						class="rounded-lg bg-indigo-600 px-4 py-2 font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
					>
						{isSaving ? 'Saving...' : editingRule ? 'Save Changes' : 'Create Rule'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}
