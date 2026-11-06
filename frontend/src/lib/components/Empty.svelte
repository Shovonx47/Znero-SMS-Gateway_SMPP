<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title?: string;
		description?: string;
		action?: Snippet;
	}

	let {
		title = 'No records found',
		description = 'There is currently no data available for this section.',
		action
	}: Props = $props();
</script>

<div class="flex flex-col items-center justify-center rounded-xl border border-dashed border-slate-300 bg-white/50 p-10 text-center dark:border-slate-800 dark:bg-slate-900/40">
	<div class="flex h-12 w-12 items-center justify-center rounded-full bg-slate-100 text-slate-400 dark:bg-slate-800 dark:text-slate-500">
		<svg class="h-6 w-6" fill="none" viewBox="0 0 24 24" stroke="currentColor">
			<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M20 13V6a2 2 0 00-2-2H6a2 2 0 00-2 2v7m16 0v5a2 2 0 01-2 2H6a2 2 0 01-2-2v-5m16 0h-2.586a1 1 0 00-.707.293l-2.414 2.414a1 1 0 01-.707.293h-3.172a1 1 0 01-.707-.293l-2.414-2.414A1 1 0 006.586 13H4" />
		</svg>
	</div>
	<h3 class="mt-4 text-base font-semibold text-slate-800 dark:text-slate-200">{title}</h3>
	{#if description}
		<p class="mt-1 max-w-sm text-sm text-slate-500 dark:text-slate-400">{description}</p>
	{/if}
	{#if action}
		<div class="mt-6">
			{@render action()}
		</div>
	{/if}
</div>
