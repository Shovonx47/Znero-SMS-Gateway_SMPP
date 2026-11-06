<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { RoutingRule, ProviderConnection } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let rules = $state<RoutingRule[]>([]);
	let availableConnections = $state<ProviderConnection[]>([]);
	let loading = $state(true);
	let errorMessage = $state('');
	let actionMessage = $state('');
	let actionError = $state('');

	// Modal state
	let showModal = $state(false);
	let editingRule = $state<RoutingRule | null>(null);
	let ruleName = $state('');
	let rulePriority = $state(1);
	let ruleActionType = $state<'block' | 'force' | 'avoid' | 'prefer'>('prefer');
	let ruleActionConnId = $state('');
	let ruleBlockReason = $state('');
	let rulePrefixCondition = $state('');
	let ruleReason = $state('');
	let ruleSubmitting = $state(false);
	let ruleError = $state('');

	const canCreate = $derived(auth.hasPermission('routing.rule.create') || auth.hasPermission('routing.rule.override'));
	const canEdit = $derived(auth.hasPermission('routing.rule.edit') || auth.hasPermission('routing.rule.override'));

	async function loadRules() {
		loading = true;
		errorMessage = '';
		try {
			const [rRes, cRes] = await Promise.all([
				apiClient.listRoutingRules(),
				apiClient.listAllConnections()
			]);
			rules = rRes.data || [];
			availableConnections = cRes.data || [];
		} catch (err: any) {
			errorMessage = err.message || 'Failed to load routing rules';
		} finally {
			loading = false;
		}
	}

	function openCreateModal() {
		editingRule = null;
		ruleName = '';
		rulePriority = rules.length + 1;
		ruleActionType = 'prefer';
		ruleActionConnId = availableConnections[0]?.id || '';
		ruleBlockReason = '';
		rulePrefixCondition = '';
		ruleReason = '';
		ruleError = '';
		showModal = true;
	}

	function openEditModal(r: RoutingRule) {
		editingRule = r;
		ruleName = r.name;
		rulePriority = r.priority;
		ruleActionType = r.action_type;
		ruleActionConnId = r.action_connection_id || availableConnections[0]?.id || '';
		ruleBlockReason = r.action_block_reason_code || '';
		rulePrefixCondition = r.conditions?.prefix || '';
		ruleReason = '';
		ruleError = '';
		showModal = true;
	}

	async function handleSaveRule() {
		if (!ruleName.trim()) {
			ruleError = 'Rule name is required.';
			return;
		}
		if (ruleActionType === 'block' && !ruleBlockReason.trim()) {
			ruleError = 'Block reason code is required when action is block.';
			return;
		}
		if ((ruleActionType === 'force' || ruleActionType === 'prefer' || ruleActionType === 'avoid') && !ruleActionConnId) {
			ruleError = 'Target connection is required for this action.';
			return;
		}

		ruleSubmitting = true;
		ruleError = '';
		const conditions: Record<string, any> = {};
		if (rulePrefixCondition.trim()) {
			conditions.prefix = rulePrefixCondition.trim();
		}

		try {
			if (editingRule) {
				const res = await apiClient.updateRoutingRule(editingRule.id, {
					name: ruleName.trim(),
					priority: rulePriority,
					conditions,
					action_type: ruleActionType,
					action_connection_id: ruleActionType !== 'block' ? ruleActionConnId : undefined,
					action_block_reason_code: ruleActionType === 'block' ? ruleBlockReason.trim() : undefined,
					reason: ruleReason.trim() || undefined
				});
				if (res.data.change_request) {
					actionMessage = `Rule update submitted for approval (Change Request: ${res.data.change_request.id.slice(0, 8)}...)`;
				} else {
					actionMessage = 'Routing rule updated successfully.';
				}
			} else {
				const res = await apiClient.createRoutingRule({
					name: ruleName.trim(),
					priority: rulePriority,
					conditions,
					action_type: ruleActionType,
					action_connection_id: ruleActionType !== 'block' ? ruleActionConnId : undefined,
					action_block_reason_code: ruleActionType === 'block' ? ruleBlockReason.trim() : undefined,
					reason: ruleReason.trim() || undefined
				});
				if (res.data.change_request) {
					actionMessage = `Rule creation submitted for approval (Change Request: ${res.data.change_request.id.slice(0, 8)}...)`;
				} else {
					actionMessage = 'Routing rule created successfully.';
				}
			}
			showModal = false;
			loadRules();
		} catch (err: any) {
			ruleError = err.message || 'Failed to save routing rule';
		} finally {
			ruleSubmitting = false;
		}
	}

	async function handleDeleteRule(r: RoutingRule) {
		if (!confirm(`Are you sure you want to remove rule "${r.name}"?`)) return;
		try {
			const res = await apiClient.deleteRoutingRule(r.id, 'Rule deleted by user');
			if (res.data.change_request) {
				actionMessage = `Rule deletion submitted for approval (Change Request: ${res.data.change_request.id.slice(0, 8)}...)`;
			} else {
				actionMessage = 'Routing rule deleted successfully.';
			}
			loadRules();
		} catch (err: any) {
			actionError = err.message || 'Failed to delete routing rule';
		}
	}

	function getActionBadge(action: string) {
		switch (action) {
			case 'block':
				return 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-300';
			case 'force':
				return 'bg-amber-100 text-amber-800 dark:bg-amber-950/60 dark:text-amber-300';
			case 'prefer':
				return 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300';
			case 'avoid':
				return 'bg-slate-100 text-slate-800 dark:bg-slate-800 dark:text-slate-300';
			default:
				return 'bg-slate-100 text-slate-800 dark:bg-slate-800 dark:text-slate-300';
		}
	}

	onMount(() => {
		loadRules();
	});
</script>

<div class="space-y-6">
	<!-- Page Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Routing Rules</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				High-priority policy overrides to force, block, prefer, or avoid specific egress routes.
			</p>
		</div>

		{#if canCreate}
			<button
				type="button"
				onclick={openCreateModal}
				class="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white shadow-sm hover:bg-indigo-500 transition"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
				</svg>
				<span>Create Routing Rule</span>
			</button>
		{/if}
	</div>

	<!-- Alerts -->
	{#if actionMessage}
		<div class="flex items-center justify-between rounded-xl bg-emerald-50 p-4 text-xs font-medium text-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-300">
			<span>{actionMessage}</span>
			<button type="button" onclick={() => actionMessage = ''} class="text-emerald-600 hover:text-emerald-800">Dismiss</button>
		</div>
	{/if}
	{#if actionError}
		<div class="flex items-center justify-between rounded-xl bg-rose-50 p-4 text-xs font-medium text-rose-800 dark:bg-rose-950/50 dark:text-rose-300">
			<span>{actionError}</span>
			<button type="button" onclick={() => actionError = ''} class="text-rose-600 hover:text-rose-800">Dismiss</button>
		</div>
	{/if}

	<!-- Content -->
	{#if loading}
		<Loading message="Loading routing rules..." />
	{:else if errorMessage}
		<ErrorState title="Error Loading Rules" message={errorMessage} retry={loadRules} />
	{:else if rules.length === 0}
		<Empty title="No routing rules defined" description="Create an override rule to force, block, or prefer specific routes." />
	{:else}
		<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-xs dark:border-slate-800 dark:bg-slate-900">
			<table class="w-full text-left text-sm text-slate-600 dark:text-slate-400">
				<thead class="border-b border-slate-200 bg-slate-50 text-xs font-semibold uppercase text-slate-500 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-400">
					<tr>
						<th class="px-4 py-3">Order</th>
						<th class="px-4 py-3">Rule Name</th>
						<th class="px-4 py-3">Action</th>
						<th class="px-4 py-3">Target / Reason</th>
						<th class="px-4 py-3">Conditions</th>
						<th class="px-4 py-3">Status</th>
						<th class="px-4 py-3 text-right">Actions</th>
					</tr>
				</thead>
				<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
					{#each rules as r}
						<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/40">
							<td class="px-4 py-3 font-mono text-xs font-bold text-slate-900 dark:text-white">
								#{r.priority}
							</td>
							<td class="px-4 py-3 font-semibold text-slate-900 dark:text-white">
								{r.name}
							</td>
							<td class="px-4 py-3">
								<span class="inline-flex rounded-full px-2.5 py-0.5 text-xs font-bold uppercase {getActionBadge(r.action_type)}">
									{r.action_type}
								</span>
							</td>
							<td class="px-4 py-3 text-xs">
								{#if r.action_type === 'block'}
									<span class="font-mono text-rose-600 dark:text-rose-400">Reason: {r.action_block_reason_code || 'BLOCKED'}</span>
								{:else if r.action_connection_id}
									<span class="font-medium text-slate-900 dark:text-white">{r.action_connection_name || r.action_connection_id.slice(0, 8) + '...'}</span>
								{/if}
							</td>
							<td class="px-4 py-3 text-xs">
								{#if r.conditions && Object.keys(r.conditions).length > 0}
									<div class="font-mono text-[11px] text-slate-500">
										{JSON.stringify(r.conditions)}
									</div>
								{:else}
									<span class="text-slate-400">All Traffic</span>
								{/if}
							</td>
							<td class="px-4 py-3">
								<span class="inline-flex rounded-full px-2 py-0.5 text-[11px] font-semibold {r.status === 'active' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950/60 dark:text-emerald-300' : 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300'}">
									{r.status.toUpperCase()}
								</span>
							</td>
							<td class="px-4 py-3 text-right space-x-1">
								{#if canEdit}
									<button
										type="button"
										onclick={() => openEditModal(r)}
										class="rounded px-2.5 py-1 text-xs font-semibold text-indigo-600 hover:bg-indigo-50 dark:text-indigo-400 dark:hover:bg-indigo-950/50"
									>
										Edit
									</button>
									<button
										type="button"
										onclick={() => handleDeleteRule(r)}
										class="rounded px-2.5 py-1 text-xs font-semibold text-rose-600 hover:bg-rose-50 dark:text-rose-400 dark:hover:bg-rose-950/50"
									>
										Delete
									</button>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>

<!-- Modal -->
{#if showModal}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/60 backdrop-blur-xs p-4">
		<div class="w-full max-w-lg rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900 space-y-4">
			<div class="flex items-center justify-between">
				<h2 class="text-lg font-bold text-slate-900 dark:text-white">
					{editingRule ? 'Edit Routing Rule' : 'Create Routing Rule'}
				</h2>
				<button type="button" onclick={() => showModal = false} aria-label="Close modal" class="rounded p-1 text-slate-400 hover:text-slate-600">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
					</svg>
				</button>
			</div>

			{#if ruleError}
				<div class="rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300">
					{ruleError}
				</div>
			{/if}

			<div class="space-y-3">
				<div>
					<label for="rule-name" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Rule Name *</label>
					<input
						id="rule-name"
						type="text"
						bind:value={ruleName}
						placeholder="e.g. Prefer Direct Route for OTP Traffic"
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					/>
				</div>

				<div class="grid grid-cols-2 gap-3">
					<div>
						<label for="rule-priority" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Priority Order *</label>
						<input
							id="rule-priority"
							type="number"
							min="1"
							bind:value={rulePriority}
							class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
						/>
					</div>
					<div>
						<label for="rule-action" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Action Type *</label>
						<select
							id="rule-action"
							bind:value={ruleActionType}
							class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
						>
							<option value="prefer">Prefer (Promote to top)</option>
							<option value="force">Force (Pin strictly to connection)</option>
							<option value="avoid">Avoid (Demote to bottom)</option>
							<option value="block">Block (Deny dispatch immediately)</option>
						</select>
					</div>
				</div>

				{#if ruleActionType === 'block'}
					<div>
						<label for="rule-block-reason" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Block Reason Code *</label>
						<input
							id="rule-block-reason"
							type="text"
							bind:value={ruleBlockReason}
							placeholder="e.g. COMPLIANCE_SANCTION or FRAUD_RESTRICTED"
							class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-sm uppercase text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
						/>
					</div>
				{:else}
					<div>
						<label for="rule-target-conn" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Target Connection *</label>
						<select
							id="rule-target-conn"
							bind:value={ruleActionConnId}
							class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
						>
							{#each availableConnections as c}
								<option value={c.id}>{c.name}</option>
							{/each}
						</select>
					</div>
				{/if}

				<div>
					<label for="rule-prefix-cond" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Prefix Match Condition (Optional)</label>
					<input
						id="rule-prefix-cond"
						type="text"
						bind:value={rulePrefixCondition}
						placeholder="e.g. 1202 or 44"
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					/>
				</div>

				<div>
					<label for="rule-audit-note" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Change Reason / Audit Note</label>
					<input
						id="rule-audit-note"
						type="text"
						bind:value={ruleReason}
						placeholder="Optional note for maker-checker review..."
						class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
					/>
				</div>
			</div>

			<div class="flex justify-end gap-2 pt-2">
				<button
					type="button"
					onclick={() => showModal = false}
					class="rounded-lg border border-slate-200 px-4 py-2 text-sm font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
				>
					Cancel
				</button>
				<button
					type="button"
					disabled={ruleSubmitting}
					onclick={handleSaveRule}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					{ruleSubmitting ? 'Saving...' : 'Save Rule'}
				</button>
			</div>
		</div>
	</div>
{/if}
