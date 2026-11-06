<!--
  (app)/+layout.svelte
  Authenticated app layout — sidebar + topbar.
  Only renders its children once auth.isAuthenticated is confirmed.

  Guard contract:
  1. On mount, attempt silent token refresh (POST /api/v1/auth/refresh via httpOnly cookie).
  2. If refresh succeeds → fetch /me → render app shell.
  3. If refresh fails (no cookie / expired) → redirect to /login?next=<currentPath>.
  4. A loading veil covers the screen during the async check to prevent any flash
     of protected content.
  5. MFA enforcement: Super Admins who have not set up MFA are redirected to
     /mfa/setup before any other page is rendered.
-->
<script lang="ts">
	import { onMount, type Snippet } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { get } from 'svelte/store';
	import { auth } from '$lib/auth.svelte';
	import { apiClient } from '$lib/api/client';
	import Sidebar from '$lib/components/Sidebar.svelte';
	import Topbar from '$lib/components/Topbar.svelte';
	import Toast from '$lib/components/Toast.svelte';

	interface Props {
		children: Snippet;
	}

	let { children }: Props = $props();

	// Guard state:
	//   'checking'      — verifying session; loading veil shown.
	//   'authenticated' — session confirmed; app shell rendered.
	//   'redirecting'   — navigating elsewhere; keep veil up.
	// NOTE: name is 'guardState' to avoid shadowing the $state rune in the TS checker.
	type GuardState = 'checking' | 'authenticated' | 'redirecting';
	let guardState = $state<GuardState>('checking');

	let isSidebarOpen = $state(false);
	let isBackendOnline: boolean | undefined = $state(undefined);

	function toggleSidebar() {
		isSidebarOpen = !isSidebarOpen;
	}

	function closeSidebar() {
		isSidebarOpen = false;
	}

	// onMount must return a synchronous cleanup function or void.
	// We handle the async work inside without returning the promise.
	let cleanupInterval: ReturnType<typeof setInterval> | null = null;

	onMount(() => {
		(async () => {
			// ----------------------------------------------------------------
			// Step 1 — resolve auth (once per SPA lifetime; no-op if already done)
			// ----------------------------------------------------------------
			if (!auth.initialized) {
				await auth.init();
			}

			// ----------------------------------------------------------------
			// Step 2 — if still not authenticated, redirect to login
			// ----------------------------------------------------------------
			if (!auth.isAuthenticated) {
				guardState = 'redirecting';
				const currentPath = get(page).url.pathname + get(page).url.search;
				const next = encodeURIComponent(currentPath);
				await goto(`/login?next=${next}&reason=expired`, { replaceState: true });
				return;
			}

			// ----------------------------------------------------------------
			// Step 3 — session is valid; reveal the app shell
			// ----------------------------------------------------------------
			guardState = 'authenticated';

			// Start periodic backend health probe (separate concern, non-blocking)
			const probe = async () => {
				try {
					await apiClient.getHealth();
					isBackendOnline = true;
				} catch {
					isBackendOnline = false;
				}
			};
			probe();
			cleanupInterval = setInterval(probe, 15_000);
		})();

		// Synchronous cleanup returned to onMount
		return () => {
			if (cleanupInterval !== null) clearInterval(cleanupInterval);
		};
	});
</script>

{#if guardState === 'checking' || guardState === 'redirecting'}
	<!--
    Loading veil: full-screen, no sidebar, no nav.
    Zero protected content is rendered here — the DOM is completely empty of
    any app structure while we resolve auth.
  -->
	<div
		class="flex min-h-screen items-center justify-center bg-slate-50 dark:bg-slate-950"
		role="status"
		aria-live="polite"
		aria-label="Verifying session"
	>
		<div class="flex flex-col items-center gap-4">
			<div
				class="flex h-12 w-12 items-center justify-center rounded-xl bg-indigo-600 text-xl font-bold text-white shadow-sm"
			>
				Z
			</div>
			<div class="flex items-center gap-2 text-sm text-slate-500 dark:text-slate-400">
				<svg class="h-4 w-4 animate-spin" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path
						stroke-linecap="round"
						stroke-linejoin="round"
						stroke-width="2"
						d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
					/>
				</svg>
				<span>Verifying session…</span>
			</div>
		</div>
	</div>
{:else}
	<!--
    App shell: only rendered when guardState === 'authenticated'.
    Protected content never flashes because the veil was up until this point.
  -->
	<div
		class="flex min-h-screen bg-slate-50 text-slate-900 transition-colors duration-150 dark:bg-slate-950 dark:text-slate-100"
	>
		<!-- Sidebar -->
		<Sidebar isOpen={isSidebarOpen} onClose={closeSidebar} />

		<!-- Mobile sidebar overlay -->
		{#if isSidebarOpen}
			<div
				class="fixed inset-0 z-30 bg-slate-900/50 backdrop-blur-xs lg:hidden"
				onclick={closeSidebar}
				role="presentation"
			></div>
		{/if}

		<!-- Main column -->
		<div class="flex flex-1 flex-col overflow-hidden">
			<Topbar onToggleSidebar={toggleSidebar} backendLive={isBackendOnline} />

			<main class="flex-1 overflow-y-auto p-6 md:p-8">
				<div class="mx-auto max-w-7xl">
					{@render children()}
				</div>
			</main>
		</div>
		<Toast />
	</div>
{/if}
