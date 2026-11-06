<script lang="ts">
	interface Props {
		title?: string;
		message?: string;
		code?: string;
		requestId?: string;
		retry?: () => void;
	}

	let {
		title = 'Failed to load data',
		message = 'An unexpected error occurred while processing the request.',
		code,
		requestId,
		retry
	}: Props = $props();
</script>

<div class="rounded-xl border border-rose-200 bg-rose-50/70 p-6 dark:border-rose-900/50 dark:bg-rose-950/20" role="alert">
	<div class="flex items-start gap-4">
		<div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-rose-100 text-rose-600 dark:bg-rose-900/50 dark:text-rose-400">
			<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" />
			</svg>
		</div>

		<div class="flex-1">
			<h4 class="text-base font-semibold text-rose-900 dark:text-rose-200">{title}</h4>
			<p class="mt-1 text-sm text-rose-700 dark:text-rose-300">{message}</p>

			{#if code || requestId}
				<div class="mt-3 flex flex-wrap items-center gap-2 text-xs font-mono text-rose-800 dark:text-rose-400">
					{#if code}
						<span class="rounded bg-rose-200/60 px-2 py-0.5 dark:bg-rose-900/40">CODE: {code}</span>
					{/if}
					{#if requestId}
						<span class="rounded bg-rose-200/60 px-2 py-0.5 dark:bg-rose-900/40">REQUEST-ID: {requestId}</span>
					{/if}
				</div>
			{/if}

			{#if retry}
				<div class="mt-4">
					<button
						type="button"
						onclick={retry}
						class="inline-flex items-center gap-2 rounded-lg bg-rose-600 px-4 py-2 text-xs font-semibold text-white shadow-sm transition hover:bg-rose-700 focus:outline-none focus:ring-2 focus:ring-rose-500 focus:ring-offset-2 dark:bg-rose-700 dark:hover:bg-rose-600"
					>
						<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
						</svg>
						Retry Request
					</button>
				</div>
			{/if}
		</div>
	</div>
</div>
