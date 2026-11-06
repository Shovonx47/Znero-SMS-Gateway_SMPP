<!--
  (auth)/+layout.svelte
  Public auth layout — NO sidebar, NO topbar, NO nav.
  Used by: /login, /forgot-password, /reset-password, /accept-invitation, /mfa/challenge, /mfa/setup

  Guard: if the user is already authenticated (has a valid session), redirect them
  away from auth pages to the dashboard immediately.
-->
<script lang="ts">
	import { onMount, type Snippet } from 'svelte';
	import { goto } from '$app/navigation';
	import { auth } from '$lib/auth.svelte';
	import { themeManager } from '$lib/theme.svelte';

	interface Props {
		children: Snippet;
	}

	let { children }: Props = $props();

	// Track whether we've resolved the session check yet.
	// We must not render the page slot (which could flash login form) until
	// we know the user isn't already authenticated.
	let ready = $state(false);

	onMount(() => {
		(async () => {
			// Wait for auth to finish its silent-refresh check (runs once per page load)
			if (!auth.initialized) {
				await auth.init();
			}

			// Already logged in → send away from auth pages to dashboard
			if (auth.isAuthenticated) {
				await goto('/dashboard', { replaceState: true });
				return;
			}

			ready = true;
		})();
	});
</script>

<!--
  Full-viewport centered container for auth forms.
  Inherits the dark/light class from <html> (set by themeManager in root layout).
-->
<div
	class="flex min-h-screen flex-col items-center justify-center bg-slate-50 px-4 dark:bg-slate-950"
>
	<!-- Minimal branded header strip -->
	<div class="mb-8 flex flex-col items-center gap-2">
		<div
			class="flex h-12 w-12 items-center justify-center rounded-xl bg-indigo-600 text-xl font-bold text-white shadow-sm"
		>
			Z
		</div>
		<div class="text-center">
			<h1 class="text-sm font-bold tracking-tight text-slate-900 dark:text-white">
				Znero International
			</h1>
			<p class="text-[11px] font-medium uppercase tracking-wider text-slate-400 dark:text-slate-500">
				Wholesale SMS Switching
			</p>
		</div>
	</div>

	<!-- Page slot — only rendered after session check is complete -->
	{#if ready}
		{@render children()}
	{:else}
		<!-- Slim loading state while we probe the refresh endpoint -->
		<div class="flex items-center gap-2 text-xs text-slate-400 dark:text-slate-500">
			<svg class="h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24" stroke="currentColor">
				<path
					stroke-linecap="round"
					stroke-linejoin="round"
					stroke-width="2"
					d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
				/>
			</svg>
			Verifying session…
		</div>
	{/if}

	<!-- Theme toggle (accessible on auth pages too) -->
	<div class="mt-8">
		<button
			type="button"
			onclick={() => themeManager.toggle()}
			class="flex items-center gap-1.5 rounded-lg border border-slate-200 bg-white px-3 py-1.5 text-[11px] font-medium text-slate-500 transition hover:bg-slate-50 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400 dark:hover:bg-slate-800"
			title="Toggle light / dark mode"
		>
			{#if themeManager.resolvedDark}
				<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path
						stroke-linecap="round"
						stroke-linejoin="round"
						stroke-width="2"
						d="M12 3v1m0 16v1m9-9h-1M4 12H3m15.364 6.364l-.707-.707M6.343 6.343l-.707-.707m12.728 0l-.707.707M6.343 17.657l-.707.707M16 12a4 4 0 11-8 0 4 4 0 018 0z"
					/>
				</svg>
				Light mode
			{:else}
				<svg class="h-3.5 w-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path
						stroke-linecap="round"
						stroke-linejoin="round"
						stroke-width="2"
						d="M20.354 15.354A9 9 0 018.646 3.646 9.003 9.003 0 0012 21a9.003 9.003 0 008.354-5.646z"
					/>
				</svg>
				Dark mode
			{/if}
		</button>
	</div>
</div>
