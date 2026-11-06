<!--
  VersionHistory.svelte
  Displays a timeline of entity versions with diffs and rollback capability.
-->
<script lang="ts">
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { toast } from './Toast.svelte';

	interface VersionEntry {
		id: string;
		version: number;
		changed_at: string;
		actor_user_id?: string;
		reason: string;
		snapshot: any;
	}

	interface Props {
		entityType: 'country' | 'operator' | 'prefix';
		entityId: string;
		history: any[];
		onRollback?: () => void;
	}

	let { entityType, entityId, history, onRollback }: Props = $props();

	let expandedVersion = $state<number | null>(null);
	let confirmVersion = $state<number | null>(null);
	let rollbackReason = $state('');
	let isRollingBack = $state(false);

	function toggle(v: number) {
		expandedVersion = expandedVersion === v ? null : v;
	}

	function getDiff(current: any, previous: any) {
		const cObj = (current && typeof current === 'object') ? (current as Record<string, unknown>) : {};
		const pObj = (previous && typeof previous === 'object') ? (previous as Record<string, unknown>) : {};
		const keys = new Set([...Object.keys(cObj), ...Object.keys(pObj)]);
		const changes: Array<{ key: string; before: unknown; after: unknown; changed: boolean }> = [];
		keys.forEach((k) => {
			const before = pObj[k];
			const after = cObj[k];
			const changed = JSON.stringify(before) !== JSON.stringify(after);
			changes.push({ key: k, before, after, changed });
		});
		return changes;
	}

	async function handleRollback() {
		if (confirmVersion === null || !rollbackReason.trim()) return;
		isRollingBack = true;
		try {
			if (entityType === 'country') {
				await apiClient.rollbackCountry(entityId, confirmVersion, rollbackReason.trim());
			} else if (entityType === 'operator') {
				await apiClient.rollbackOperator(entityId, confirmVersion, rollbackReason.trim());
			} else if (entityType === 'prefix') {
				await apiClient.rollbackPrefix(entityId, confirmVersion, rollbackReason.trim());
			}
			toast(`Rolled back to version ${confirmVersion}`, 'success');
			confirmVersion = null;
			rollbackReason = '';
			onRollback?.();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Rollback failed', 'error');
		} finally {
			isRollingBack = false;
		}
	}

	function fmt(dt: string) {
		return new Date(dt).toLocaleString();
	}

	function display(v: unknown): string {
		if (v === null || v === undefined) return '—';
		if (typeof v === 'object') return JSON.stringify(v);
		return String(v);
	}

	// Sort newest first
	const sorted = $derived([...history].sort((a, b) => b.version - a.version));
</script>

<div class="space-y-2">
	{#if sorted.length === 0}
		<p class="text-xs text-slate-500 py-4 text-center">No history available.</p>
	{:else}
		{#each sorted as entry, i}
			{@const prev = sorted[i + 1] ?? null}
			<div class="rounded-xl border border-slate-200 dark:border-slate-800 overflow-hidden">
				<!-- Header row -->
				<div class="w-full flex items-center justify-between px-4 py-3 hover:bg-slate-50 dark:hover:bg-slate-800/40 transition">
					<button
						type="button"
						onclick={() => toggle(entry.version)}
						class="flex-1 flex items-center gap-3 text-left min-w-0"
					>
						<span class="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-indigo-100 text-indigo-700 text-[10px] font-bold dark:bg-indigo-950/60 dark:text-indigo-300">
							v{entry.version}
						</span>
						<div class="flex-1 min-w-0">
							<div class="text-xs font-semibold text-slate-800 dark:text-slate-200 truncate">
								{entry.reason || 'No reason provided'}
							</div>
							<div class="text-[10px] text-slate-400 mt-0.5">{fmt(entry.changed_at)}{entry.actor_user_id ? ` · ${entry.actor_user_id}` : ''}</div>
						</div>
						<svg class="h-3.5 w-3.5 text-slate-400 transition-transform {expandedVersion === entry.version ? 'rotate-180' : ''}" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7" />
						</svg>
					</button>

					<div class="ml-3 shrink-0">
						{#if i === 0}
							<span class="rounded bg-emerald-100 px-1.5 py-0.5 text-[10px] font-semibold text-emerald-700 dark:bg-emerald-950/50 dark:text-emerald-300">Latest</span>
						{:else}
							<button
								type="button"
								onclick={() => { confirmVersion = entry.version; rollbackReason = ''; }}
								class="rounded bg-amber-100 px-2 py-1 text-[10px] font-semibold text-amber-700 hover:bg-amber-200 dark:bg-amber-950/50 dark:text-amber-300 dark:hover:bg-amber-900/60 transition"
							>
								Rollback
							</button>
						{/if}
					</div>
				</div>

				<!-- Diff panel -->
				{#if expandedVersion === entry.version}
					<div class="border-t border-slate-200 dark:border-slate-800 bg-slate-50/50 dark:bg-slate-900/40 overflow-x-auto">
						<table class="w-full text-[11px]">
							<thead>
								<tr class="border-b border-slate-200 dark:border-slate-800 text-slate-400 uppercase tracking-wide">
									<th class="px-4 py-2 text-left">Field</th>
									<th class="px-4 py-2 text-left">{prev ? `v${prev.version}` : 'Before'}</th>
									<th class="px-4 py-2 text-left">v{entry.version}</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
								{#each getDiff(entry.snapshot as Record<string, unknown>, prev?.snapshot as Record<string, unknown> ?? null) as row}
									<tr class="{row.changed ? 'bg-amber-50/50 dark:bg-amber-950/10' : ''} font-mono">
										<td class="px-4 py-1.5 text-slate-500 dark:text-slate-400">{row.key}</td>
										<td class="px-4 py-1.5 text-slate-500 dark:text-slate-500 max-w-[200px] truncate" title={display(row.before)}>{display(row.before)}</td>
										<td class="px-4 py-1.5 {row.changed ? 'text-slate-900 dark:text-white font-semibold' : 'text-slate-600 dark:text-slate-400'} max-w-[200px] truncate" title={display(row.after)}>{display(row.after)}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/if}
			</div>
		{/each}
	{/if}
</div>

<!-- Rollback Confirmation Modal -->
{#if confirmVersion !== null}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 backdrop-blur-xs p-4">
		<div class="w-full max-w-sm rounded-2xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h3 class="text-sm font-bold text-slate-900 dark:text-white">Rollback to Version {confirmVersion}</h3>
			<p class="mt-1 text-xs text-slate-500">This will restore the entity to its state at version {confirmVersion}. A new history entry will be created.</p>
			<div class="mt-4">
				<label for="rollback-field-1" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Reason <span class="text-rose-500">*</span></label>
				<textarea
					id="rollback-field-1"
					bind:value={rollbackReason}
					rows="2"
					placeholder="Reason for rollback..."
					class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				></textarea>
			</div>
			<div class="mt-4 flex justify-end gap-2">
				<button
					type="button"
					onclick={() => { confirmVersion = null; rollbackReason = ''; }}
					class="rounded-lg px-3 py-2 text-xs font-semibold text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
				>Cancel</button>
				<button
					type="button"
					disabled={isRollingBack || !rollbackReason.trim()}
					onclick={handleRollback}
					class="rounded-lg bg-amber-600 px-4 py-2 text-xs font-semibold text-white hover:bg-amber-500 disabled:opacity-50"
				>
					{isRollingBack ? 'Rolling back...' : 'Confirm Rollback'}
				</button>
			</div>
		</div>
	</div>
{/if}
