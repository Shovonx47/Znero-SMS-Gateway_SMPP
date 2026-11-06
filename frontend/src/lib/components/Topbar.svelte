<script lang="ts">
	import { goto } from '$app/navigation';
	import { auth } from '../auth.svelte';
	import { themeManager } from '../theme.svelte';

	interface Props {
		onToggleSidebar?: () => void;
		backendLive?: boolean;
	}

	let { onToggleSidebar, backendLive }: Props = $props();

	async function handleLogout() {
		await auth.logout();
		goto('/login');
	}
</script>

<header class="sticky top-0 z-30 flex h-16 w-full items-center justify-between border-b border-slate-200 bg-white/80 px-6 backdrop-blur-md dark:border-slate-800 dark:bg-slate-900/80">
	<div class="flex items-center gap-4">
		{#if onToggleSidebar}
			<button
				type="button"
				onclick={onToggleSidebar}
				class="rounded-lg p-1.5 text-slate-500 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800 lg:hidden"
				aria-label="Toggle menu"
			>
				<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 6h16M4 12h16M4 18h16" />
				</svg>
			</button>
		{/if}

		<div>
			<nav class="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400" aria-label="Breadcrumb">
				<span class="font-medium text-slate-700 dark:text-slate-300">Znero International</span>
				<span>/</span>
				{#if auth.tenant?.actor_org_type}
					<span class="inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-bold uppercase tracking-wider {auth.tenant.actor_org_type === 'platform' ? 'bg-purple-100 text-purple-800 dark:bg-purple-900/50 dark:text-purple-300' : auth.tenant.actor_org_type === 'reseller' ? 'bg-blue-100 text-blue-800 dark:bg-blue-900/50 dark:text-blue-300' : 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/50 dark:text-emerald-300'}">
						{auth.tenant.actor_org_type}
					</span>
				{:else}
					<span class="text-indigo-600 font-semibold dark:text-indigo-400">Wholesale SMS Switching</span>
				{/if}
			</nav>
		</div>
	</div>

	<div class="flex items-center gap-3">
		<!-- Backend connection indicator -->
		<div class="hidden sm:flex items-center gap-2 text-xs font-medium mr-2">
			<span class="h-2 w-2 rounded-full {backendLive === undefined ? 'bg-amber-400 animate-pulse' : backendLive ? 'bg-emerald-500' : 'bg-rose-500'}"></span>
			<span class="text-slate-600 dark:text-slate-300 text-[11px]">
				{backendLive === undefined ? 'Probing' : backendLive ? 'Online' : 'Offline'}
			</span>
		</div>

		<!-- Theme toggle -->
		<button
			type="button"
			onclick={() => themeManager.toggle()}
			class="flex h-8 w-8 items-center justify-center rounded-lg border border-slate-200 bg-slate-50 text-slate-600 transition hover:bg-slate-100 hover:text-slate-900 focus:outline-none focus:ring-2 focus:ring-indigo-500 dark:border-slate-800 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700 dark:hover:text-white"
			aria-label="Toggle theme mode"
			title="Toggle theme (Light / Dark)"
		>
			{#if themeManager.resolvedDark}
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 3v1m0 16v1m9-9h-1M4 12H3m15.364 6.364l-.707-.707M6.343 6.343l-.707-.707m12.728 0l-.707.707M6.343 17.657l-.707.707M16 12a4 4 0 11-8 0 4 4 0 018 0z" />
				</svg>
			{:else}
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M20.354 15.354A9 9 0 018.646 3.646 9.003 9.003 0 0012 21a9.003 9.003 0 008.354-5.646z" />
				</svg>
			{/if}
		</button>

		<!-- Auth user state -->
		{#if auth.isAuthenticated && auth.user}
			<div class="flex items-center gap-2 pl-2 border-l border-slate-200 dark:border-slate-800">
				<div class="hidden md:block text-right">
					<div class="text-xs font-semibold text-slate-800 dark:text-slate-200 truncate max-w-[150px]">
						{auth.user.email}
					</div>
					<div class="text-[10px] text-slate-500 dark:text-slate-400">
						{auth.user.roles?.[0] || 'Member'}
					</div>
				</div>

				<button
					type="button"
					onclick={handleLogout}
					class="rounded-lg bg-slate-100 px-2.5 py-1.5 text-xs font-medium text-slate-700 transition hover:bg-rose-50 hover:text-rose-600 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-rose-950/40 dark:hover:text-rose-400"
					title="Sign out"
				>
					Sign out
				</button>
			</div>
		{:else if !auth.loading}
			<a
				href="/login"
				class="rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-semibold text-white shadow-xs transition hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-500"
			>
				Sign In
			</a>
		{/if}
	</div>
</header>
