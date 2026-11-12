<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import { i18n } from '$lib/i18n.svelte';
	import type { SettingItem } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let settings = $state<SettingItem[]>([]);
	let category = $state('platform');
	let visibleSecrets = $state<Record<string, boolean>>({});
	let editingValues = $state<Record<string, any>>({});
	let saveState = $state<Record<string, 'idle' | 'saving' | 'saved' | 'error'>>({});

	async function loadSettings() {
		try {
			loading = true;
			error = null;
			const res = await apiClient.getSettings(category);
			settings = res.data.settings || [];
			editingValues = {};
			for (const s of settings) {
				editingValues[s.key] = s.value;
			}
		} catch (err: any) {
			error = err.message || 'Failed to load system settings';
		} finally {
			loading = false;
		}
	}

	function toggleSecret(key: string) {
		visibleSecrets[key] = !visibleSecrets[key];
	}

	async function handleSave(item: SettingItem) {
		try {
			saveState[item.key] = 'saving';
			let val = editingValues[item.key];
			if (item.value_type === 'integer') {
				val = parseInt(val, 10);
			} else if (item.value_type === 'float') {
				val = parseFloat(val);
			} else if (item.value_type === 'boolean') {
				val = Boolean(val);
			} else if (item.value_type === 'json' && typeof val === 'string') {
				try {
					val = JSON.parse(val);
				} catch (e) {
					alert('Invalid JSON syntax');
					saveState[item.key] = 'error';
					return;
				}
			}

			await apiClient.updateSetting(item.key, val);
			saveState[item.key] = 'saved';
			setTimeout(() => {
				saveState[item.key] = 'idle';
			}, 2000);
		} catch (err: any) {
			saveState[item.key] = 'error';
			alert(err.message || 'Failed to save setting');
		}
	}

	async function handleReset(item: SettingItem) {
		if (!confirm(`Reset setting ${item.key} to factory default?`)) return;
		try {
			await apiClient.resetSetting(item.key);
			await loadSettings();
		} catch (err: any) {
			alert(err.message || 'Failed to reset setting');
		}
	}

	onMount(() => {
		loadSettings();
	});
</script>

<svelte:head>
	<title>{i18n.t('nav_settings')} | {i18n.t('app_title')}</title>
</svelte:head>

<div class="space-y-6 p-6">
	<div>
		<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
			{i18n.t('settings_title')}
		</h1>
		<p class="text-sm text-slate-500 dark:text-slate-400">
			Manage typed platform parameters, switching engine defaults, billing controls, and secrets.
		</p>
	</div>

	<!-- Category Navigation Tabs -->
	<div class="flex border-b border-slate-200 dark:border-slate-800">
		{#each ['platform', 'billing', 'routing', 'alerts'] as cat}
			<button
				type="button"
				onclick={() => { category = cat; loadSettings(); }}
				class="border-b-2 px-4 py-2.5 text-xs font-semibold uppercase tracking-wider transition {category === cat
					? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400'
					: 'border-transparent text-slate-500 hover:border-slate-300 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200'}"
			>
				{cat}
			</button>
		{/each}
	</div>

	{#if loading}
		<Loading message="Loading registry settings..." />
	{:else if error}
		<ErrorState title="Settings Error" message={error} retry={loadSettings} />
	{:else}
		<div class="divide-y divide-slate-100 rounded-xl border border-slate-200 bg-white shadow-sm dark:divide-slate-800 dark:border-slate-800 dark:bg-slate-900">
			{#each settings as item}
				<div class="p-6">
					<div class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
						<div class="max-w-md">
							<div class="flex items-center gap-2">
								<span class="font-mono text-xs font-bold text-slate-900 dark:text-white">{item.key}</span>
								<span class="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[10px] text-slate-600 dark:bg-slate-800 dark:text-slate-300">
									{item.value_type}
								</span>
								{#if item.is_secret}
									<span class="rounded bg-purple-100 px-1.5 py-0.5 text-[10px] font-semibold text-purple-700 dark:bg-purple-950/50 dark:text-purple-400">
										Secret
									</span>
								{/if}
							</div>
							<p class="mt-1 text-xs text-slate-500">{item.description || 'System setting'}</p>
						</div>

						<!-- Input Control based on value_type -->
						<div class="flex flex-1 max-w-sm flex-col items-end gap-2">
							{#if item.value_type === 'boolean'}
								<label class="flex items-center gap-2 cursor-pointer">
									<input
										type="checkbox"
										bind:checked={editingValues[item.key]}
										disabled={!auth.isPlatformAdmin && item.is_read_only}
										class="h-4 w-4 rounded border-slate-300 text-indigo-600 focus:ring-indigo-500"
									/>
									<span class="text-xs font-semibold text-slate-700 dark:text-slate-300">
										{editingValues[item.key] ? 'Enabled' : 'Disabled'}
									</span>
								</label>
							{:else if item.value_type === 'integer' || item.value_type === 'float'}
								<input
									type="number"
									bind:value={editingValues[item.key]}
									disabled={!auth.isPlatformAdmin && item.is_read_only}
									class="w-full rounded-lg border border-slate-300 p-2 text-xs dark:border-slate-700 dark:bg-slate-800 dark:text-white"
								/>
							{:else if item.value_type === 'json'}
								<textarea
									rows={3}
									value={typeof editingValues[item.key] === 'object' ? JSON.stringify(editingValues[item.key], null, 2) : editingValues[item.key]}
									oninput={(e: any) => (editingValues[item.key] = e.target.value)}
									disabled={!auth.isPlatformAdmin && item.is_read_only}
									class="w-full font-mono rounded-lg border border-slate-300 p-2 text-xs dark:border-slate-700 dark:bg-slate-800 dark:text-white"
								></textarea>
							{:else if item.is_secret}
								<div class="flex w-full items-center gap-1">
									<input
										type={visibleSecrets[item.key] ? 'text' : 'password'}
										bind:value={editingValues[item.key]}
										disabled={!auth.isPlatformAdmin && item.is_read_only}
										class="w-full rounded-lg border border-slate-300 p-2 text-xs font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white"
									/>
									<button
										type="button"
										onclick={() => toggleSecret(item.key)}
										class="rounded-lg border border-slate-300 p-2 text-slate-500 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-400"
									>
										{visibleSecrets[item.key] ? 'Hide' : 'Show'}
									</button>
								</div>
							{:else}
								<input
									type="text"
									bind:value={editingValues[item.key]}
									disabled={!auth.isPlatformAdmin && item.is_read_only}
									class="w-full rounded-lg border border-slate-300 p-2 text-xs dark:border-slate-700 dark:bg-slate-800 dark:text-white"
								/>
							{/if}

							<!-- Action Buttons -->
							<div class="flex items-center gap-2">
								<button
									type="button"
									onclick={() => handleReset(item)}
									class="text-[11px] font-medium text-slate-400 hover:text-slate-600 dark:hover:text-slate-300"
								>
									{i18n.t('settings_reset')}
								</button>
								<button
									type="button"
									onclick={() => handleSave(item)}
									disabled={saveState[item.key] === 'saving'}
									class="rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50"
								>
									{saveState[item.key] === 'saving' ? 'Saving...' : saveState[item.key] === 'saved' ? 'Saved!' : i18n.t('settings_save')}
								</button>
							</div>
						</div>
					</div>
				</div>
			{:else}
				<div class="p-8 text-center text-xs text-slate-400">
					No settings registered under category "{category}".
				</div>
			{/each}
		</div>
	{/if}
</div>
