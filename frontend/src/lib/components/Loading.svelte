<script lang="ts">
	interface Props {
		message?: string;
		subtext?: string;
		size?: 'sm' | 'md' | 'lg';
	}

	let { message = 'Loading system state...', subtext, size = 'md' }: Props = $props();

	const spinnerSizes = {
		sm: 'h-4 w-4 border-2',
		md: 'h-8 w-8 border-3',
		lg: 'h-12 w-12 border-4'
	};
</script>

<div class="flex flex-col items-center justify-center p-8 text-center" role="status" aria-live="polite">
	<div
		class="{spinnerSizes[size]} animate-spin rounded-full border-indigo-600 border-t-transparent dark:border-indigo-400 dark:border-t-transparent"
		aria-hidden="true"
	></div>
	{#if message}
		<p class="mt-4 text-sm font-medium text-slate-700 dark:text-slate-300">{message}</p>
	{/if}
	{#if subtext}
		<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">{subtext}</p>
	{/if}
	<span class="sr-only">Loading</span>
</div>
