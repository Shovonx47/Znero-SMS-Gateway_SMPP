<!--
  Sidebar.svelte
  App navigation sidebar.
  - All nav items are conditionally rendered based on auth.hasPermission().
  - Platform admins (is_platform_admin) see everything.
  - System Status requires 'system:status:view' permission.
  - Organization management requires 'org:list' permission.
  - User management requires 'user:list' permission.
  - Role management requires 'role:list' permission.
  - Audit logs require 'audit:logs:view' permission.
  - Sessions and Change Password are available to all authenticated users.
-->
<script lang="ts">
	import { page } from '$app/stores';
	import { auth } from '../auth.svelte';
	import { i18n } from '../i18n.svelte';

	interface Props {
		isOpen?: boolean;
		onClose?: () => void;
	}

	let { isOpen = false, onClose }: Props = $props();

	function isActive(pathname: string): boolean {
		if (pathname === '/dashboard') {
			return $page.url.pathname === '/dashboard' || $page.url.pathname === '/';
		}
		return $page.url.pathname.startsWith(pathname);
	}

	// Permission helpers — platform admins bypass all permission checks.
	const can = (perm: string): boolean => auth.hasPermission(perm);
</script>

<aside
	class="fixed inset-y-0 left-0 z-40 flex w-64 flex-col border-r border-slate-200 bg-white transition-transform duration-200 ease-in-out dark:border-slate-800 dark:bg-slate-900 lg:static lg:translate-x-0 {isOpen
		? 'translate-x-0'
		: '-translate-x-full'}"
>
	<!-- Brand header -->
	<div class="flex h-16 items-center justify-between border-b border-slate-200 px-6 dark:border-slate-800">
		<a href="/dashboard" class="flex items-center gap-3">
			<div class="flex h-9 w-9 items-center justify-center rounded-lg bg-indigo-600 text-white font-bold text-base shadow-sm">
				Z
			</div>
			<div>
				<h1 class="text-sm font-bold tracking-tight text-slate-900 dark:text-white">ZNERO</h1>
				<p class="text-[10px] font-medium tracking-wider uppercase text-slate-500 dark:text-slate-400">SMS Switching</p>
			</div>
		</a>

		{#if onClose}
			<button
				type="button"
				onclick={onClose}
				class="rounded p-1 text-slate-400 hover:text-slate-600 dark:hover:text-slate-200 lg:hidden"
				aria-label="Close sidebar"
			>
				<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
				</svg>
			</button>
		{/if}
	</div>

	<!-- Navigation links -->
	<nav class="flex-1 space-y-6 overflow-y-auto p-4">

		<!-- Phase 9 Visibility — Analytics & Intelligence -->
		<div class="space-y-1">
			<div class="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-400 dark:text-slate-500">
				{i18n.t('nav_analytics')}
			</div>

			<!-- Executive Dashboard -->
			{#if can('analytics.dashboard.view')}
				<a
					href="/analytics"
					class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/analytics') && !isActive('/analytics/noc') && !isActive('/analytics/reseller') && !isActive('/analytics/customer')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M16 8v8m-4-5v5m-4-2v2m-2 4h12a2 2 0 002-2V6a2 2 0 00-2-2H6a2 2 0 00-2 2v12a2 2 0 002 2z" />
					</svg>
					<span>{i18n.t('nav_executive')}</span>
				</a>
			{/if}

			<!-- NOC & Live TPS (Platform Only) -->
			{#if auth.isPlatformAdmin || can('analytics.noc.view')}
				<a
					href="/analytics/noc"
					class="flex items-center justify-between rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/analytics/noc')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<div class="flex items-center gap-3">
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z" />
						</svg>
						<span>{i18n.t('nav_noc')}</span>
					</div>
					<span class="h-2 w-2 rounded-full bg-emerald-500 animate-pulse"></span>
				</a>
			{/if}

			<!-- Reseller Dashboard (Resellers & Platform) -->
			{#if auth.isPlatformAdmin || auth.tenant?.actor_org_type === 'reseller'}
				<a
					href="/analytics/reseller"
					class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/analytics/reseller')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0zm6 3a2 2 0 11-4 0 2 2 0 014 0zM7 10a2 2 0 11-4 0 2 2 0 014 0z" />
					</svg>
					<span>{i18n.t('nav_reseller')}</span>
				</a>
			{/if}

			<!-- Customer Dashboard (Customers & Platform) -->
			{#if auth.isPlatformAdmin || auth.tenant?.actor_org_type === 'customer'}
				<a
					href="/analytics/customer"
					class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/analytics/customer')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 19v-6a2 2 0 00-2-2H5a2 2 0 00-2 2v6a2 2 0 002 2h2a2 2 0 002-2zm0 0V9a2 2 0 012-2h2a2 2 0 012 2v10m-6 0a2 2 0 002 2h2a2 2 0 002-2m0 0V5a2 2 0 012-2h2a2 2 0 012 2v14a2 2 0 01-2 2h-2a2 2 0 01-2-2z" />
					</svg>
					<span>{i18n.t('nav_customer')}</span>
				</a>
			{/if}

			<!-- Alert Rules -->
			{#if can('alert.view')}
				<a
					href="/alerts"
					class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/alerts')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9" />
					</svg>
					<span>{i18n.t('nav_alerts')}</span>
				</a>
			{/if}

			<!-- Incidents -->
			{#if can('incident.view')}
				<a
					href="/incidents"
					class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/incidents')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z" />
					</svg>
					<span>{i18n.t('nav_incidents')}</span>
				</a>
			{/if}

			<!-- Settings Center -->
			{#if can('setting.view')}
				<a
					href="/settings"
					class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/settings') && !isActive('/settings/password')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
					</svg>
					<span>{i18n.t('nav_settings')}</span>
				</a>
			{/if}

			<!-- Export Center -->
			{#if can('analytics.export.view')}
				<a
					href="/exports"
					class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/exports')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 10v6m0 0l-3-3m3 3l3-3m2 8H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z" />
					</svg>
					<span>{i18n.t('nav_exports')}</span>
				</a>
			{/if}
		</div>

		<!-- Platform Core — System Status (requires system.info.view or platform admin) -->
		{#if can('system.info.view')}
			<div class="space-y-1">
				<div class="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-400 dark:text-slate-500">
					Platform Core
				</div>

				<a
					href="/dashboard"
					class="flex items-center justify-between rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/dashboard')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<div class="flex items-center gap-3">
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 19v-6a2 2 0 00-2-2H5a2 2 0 00-2 2v6a2 2 0 002 2h2a2 2 0 002-2zm0 0V9a2 2 0 012-2h2a2 2 0 012 2v10m-6 0a2 2 0 002 2h2a2 2 0 002-2m0 0V5a2 2 0 012-2h2a2 2 0 012 2v14a2 2 0 01-2 2h-2a2 2 0 01-2-2z" />
						</svg>
						<span>System Status</span>
					</div>
					<span class="rounded bg-slate-200/70 px-1.5 py-0.5 text-[10px] font-medium text-slate-700 dark:bg-slate-800 dark:text-slate-300">Infra</span>
				</a>
			</div>
		{/if}

		<!-- Access & Tenancy (org, user, role, audit — all gated separately) -->
		{#if can('org.organization.view') || can('user.account.view') || can('role.role.view') || can('audit.log.view')}
			<div class="space-y-1">
				<div class="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-400 dark:text-slate-500">
					Access & Tenancy
				</div>

				{#if can('org.organization.view')}
					<a
						href="/organizations"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/organizations')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 21V5a2 2 0 00-2-2H7a2 2 0 00-2 2v16m14 0h2m-2 0h-5m-9 0H3m2 0h5M9 7h1m-1 4h1m4-4h1m-1 4h1m-5 10v-5a1 1 0 011-1h2a1 1 0 011 1v5m-4 0h4" />
						</svg>
						<span>Organizations</span>
					</a>
				{/if}

				{#if can('user.account.view')}
					<a
						href="/users"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/users')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4.354a4 4 0 110 5.292M15 21H3v-1a6 6 0 0112 0v1zm0 0h6v-1a6 6 0 00-9-5.197M13 7a4 4 0 11-8 0 4 4 0 018 0z" />
						</svg>
						<span>Users & Invites</span>
					</a>
				{/if}

				{#if can('role.role.view')}
					<a
						href="/roles"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/roles')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016z" />
						</svg>
						<span>Roles & Permissions</span>
					</a>
				{/if}

				{#if can('audit.log.view')}
					<a
						href="/audit"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/audit')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-3 7h3m-3 4h3m-6-4h.01M9 16h.01" />
						</svg>
						<span>Audit Logs</span>
					</a>
				{/if}
			</div>
		{/if}

		<!-- Master Data -->
		{#if can('country.country.view') || can('operator.operator.view') || can('number.prefix.view') || can('number.lookup.use') || can('import.job.view') || can('number.rule.view')}
			<div class="space-y-1">
				<div class="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-400 dark:text-slate-500">
					Master Data
				</div>

				{#if can('country.country.view')}
					<a
						href="/master-data/countries"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/master-data/countries')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3.055 11H5a2 2 0 012 2v1a2 2 0 002 2 2 2 0 012 2v2.945M8 3.935V5.5A2.5 2.5 0 0010.5 8h.5a2 2 0 012 2 2 2 0 104 0 2 2 0 012-2h1.064M15 20.488V18a2 2 0 012-2h3.064M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
						</svg>
						<span>Countries</span>
					</a>
				{/if}

				{#if can('operator.operator.view')}
					<a
						href="/master-data/operators"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/master-data/operators')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8.111 16.404a5.5 5.5 0 017.778 0M12 20h.01m-7.08-7.071c3.904-3.905 10.236-3.905 14.141 0M1.394 9.393c5.857-5.857 15.355-5.857 21.213 0" />
						</svg>
						<span>Operators</span>
					</a>
				{/if}

				{#if can('number.prefix.view')}
					<a
						href="/master-data/prefixes"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/master-data/prefixes')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 5a2 2 0 012-2h3.28a1 1 0 01.948.684l1.498 4.493a1 1 0 01-.502 1.21l-2.257 1.13a11.042 11.042 0 005.516 5.516l1.13-2.257a1 1 0 011.21-.502l4.493 1.498a1 1 0 01.684.949V19a2 2 0 01-2 2h-1C9.716 21 3 14.284 3 6V5z" />
						</svg>
						<span>Number Prefixes</span>
					</a>
				{/if}

				{#if can('import.job.view')}
					<a
						href="/master-data/import"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/master-data/import')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-8l-4-4m0 0L8 8m4-4v12" />
						</svg>
						<span>Import Center</span>
					</a>
				{/if}

				{#if can('number.lookup.use')}
					<a
						href="/master-data/number-lookup"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/master-data/number-lookup')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
						</svg>
						<span>Number Lookup</span>
					</a>
				{/if}

				{#if can('number.rule.view')}
					<a
						href="/master-data/transform-rules"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/master-data/transform-rules')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 7h12m0 0l-4-4m4 4l-4 4m0 6H4m0 0l4 4m-4-4l4-4" />
						</svg>
						<span>Transform Rules</span>
					</a>
				{/if}
			</div>
		{/if}

		<!-- Upstream Suppliers (Platform-only) -->
		{#if auth.tenant?.actor_org_type === 'platform' && (can('provider.provider.view') || can('provider.coverage.view'))}
			<div class="space-y-1">
				<div class="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-400 dark:text-slate-500">
					Upstream Suppliers
				</div>

				{#if can('provider.provider.view')}
					<a
						href="/providers"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/providers')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 12h14M5 12a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v4a2 2 0 01-2 2M5 12a2 2 0 00-2 2v4a2 2 0 002 2h14a2 2 0 002-2v-4a2 2 0 00-2-2" />
						</svg>
						<span>Providers</span>
					</a>
				{/if}

				{#if can('provider.coverage.view')}
					<a
						href="/provider-coverage"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/provider-coverage')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 20l-5.447-2.724A1 1 0 013 16.382V5.618a1 1 0 011.447-.894L9 7m0 13l6-3m-6 3V7m6 10l4.553 2.276A1 1 0 0021 18.382V7.618a1 1 0 00-.553-.894L15 4m0 13V4m0 0L9 7" />
						</svg>
						<span>Provider Coverage</span>
					</a>
				{/if}
			</div>
		{/if}

		<!-- Commercial & Customers -->
		{#if can('customer.profile.view') || can('customer.apikey.view') || can('customer.smppaccount.view') || can('customer.senderid.view')}
			<div class="space-y-1">
				<div class="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-400 dark:text-slate-500">
					Customers & Credentials
				</div>

				{#if can('customer.profile.view') || can('customer.apikey.view') || can('customer.smppaccount.view')}
					<a
						href="/customers"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/customers')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0z" />
						</svg>
						<span>Customers</span>
					</a>
				{/if}

				{#if can('customer.senderid.view')}
					<a
						href="/sender-ids"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/sender-ids')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M7 8h10M7 12h4m1 8l-4-4H5a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v8a2 2 0 01-2 2h-3l-4 4z" />
						</svg>
						<span>Sender IDs</span>
					</a>
				{/if}

				<a
					href="/integration"
					class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/integration')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10 20l4-16m4 4l4 4-4 4M6 16l-4-4 4-4" />
					</svg>
					<span>API & Integration</span>
				</a>
			</div>
		{/if}

		<!-- Rating & Rates -->
		{#if can('rate.buycard.read') || can('rate.sellcard.read') || can('rate.assignment.read') || can('rate.margin.view') || auth.tenant?.actor_org_type === 'reseller' || auth.tenant?.actor_org_type === 'customer'}
			<div class="space-y-1">
				<div class="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-400 dark:text-slate-500">
					Rates & Rating
				</div>

				{#if auth.tenant?.actor_org_type === 'platform' && can('rate.buycard.read')}
					<a
						href="/rates/buy-cards"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/rates/buy-cards')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 11H5m14 0a2 2 0 012 2v6a2 2 0 01-2 2H5a2 2 0 01-2-2v-6a2 2 0 012-2m14 0V9a2 2 0 00-2-2M5 11V9a2 2 0 012-2m0 0V5a2 2 0 012-2h6a2 2 0 012 2v2M7 7h10" />
						</svg>
						<span>Provider Buy Cards</span>
					</a>
				{/if}

				{#if can('rate.sellcard.read')}
					<a
						href="/rates/sell-cards"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/rates/sell-cards')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 10h18M7 15h1m4 0h1m-7 4h12a3 3 0 003-3V8a3 3 0 00-3-3H6a3 3 0 00-3 3v8a3 3 0 003 3z" />
						</svg>
						<span>Customer Sell Cards</span>
					</a>
				{/if}

				{#if auth.tenant?.actor_org_type === 'reseller'}
					<a
						href="/rates/my-cost"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/rates/my-cost')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8c-1.657 0-3 .895-3 2s1.343 2 3 2 3 .895 3 2-1.343 2-3 2m0-8c1.11 0 2.08.402 2.599 1M12 8V7m0 1v8m0 0v1m0-1c-1.11 0-2.08-.402-2.599-1M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
						</svg>
						<span>My Cost Rate Card</span>
					</a>
				{/if}

				{#if auth.tenant?.actor_org_type === 'customer'}
					<a
						href="/rates/my-rates"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/rates/my-rates')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8c-1.657 0-3 .895-3 2s1.343 2 3 2 3 .895 3 2-1.343 2-3 2m0-8c1.11 0 2.08.402 2.599 1M12 8V7m0 1v8m0 0v1m0-1c-1.11 0-2.08-.402-2.599-1M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
						</svg>
						<span>My SMS Rates</span>
					</a>
				{/if}

				{#if auth.tenant?.actor_org_type === 'platform' && can('rate.assignment.read')}
					<a
						href="/rates/assignments"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/rates/assignments')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13.828 10.172a4 4 0 00-5.656 0l-4 4a4 4 0 105.656 5.656l1.102-1.101m-.758-4.899a4 4 0 005.656 0l4-4a4 4 0 00-5.656-5.656l-1.1 1.1" />
						</svg>
						<span>Rate Card Assignments</span>
					</a>
				{/if}

				{#if auth.tenant?.actor_org_type === 'platform' && can('rate.margin.view')}
					<a
						href="/rates/margin-report"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/rates/margin-report')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 19v-6a2 2 0 00-2-2H5a2 2 0 00-2 2v6a2 2 0 002 2h2a2 2 0 002-2zm0 0V9a2 2 0 012-2h2a2 2 0 012 2v10m-6 0a2 2 0 002 2h2a2 2 0 002-2m0 0V5a2 2 0 012-2h2a2 2 0 012 2v14a2 2 0 01-2 2h-2a2 2 0 01-2-2z" />
						</svg>
						<span>Margin & Deficit Report</span>
					</a>
				{/if}

				<a
					href="/rates/lookup"
					class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/rates/lookup')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
					</svg>
					<span>Rate Calculator & Lookup</span>
				</a>

				<a
					href="/rates/upcoming"
					class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/rates/upcoming')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 7V3m8 4V3m-9 8h10M5 21h14a2 2 0 002-2V7a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z" />
					</svg>
					<span>Upcoming Rate Changes</span>
				</a>
			</div>
		{/if}

		<!-- Wallets & Billing -->
		{#if can('wallet.wallet.view') || can('billing.invoice.view') || can('ledger.reconcile') || auth.tenant?.actor_org_type === 'reseller' || auth.tenant?.actor_org_type === 'customer'}
			<div class="space-y-1">
				<div class="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-400 dark:text-slate-500">
					Wallets & Billing
				</div>

				{#if can('wallet.wallet.view') || auth.tenant?.actor_org_type === 'reseller' || auth.tenant?.actor_org_type === 'customer'}
					<a
						href="/wallets"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/wallets')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 10h18M7 15h1m4 0h1m-7 4h12a3 3 0 003-3V8a3 3 0 00-3-3H6a3 3 0 00-3 3v8a3 3 0 003 3z" />
						</svg>
						<span>{auth.tenant?.actor_org_type === 'customer' ? 'My Wallet' : 'Wallets & Balances'}</span>
					</a>
				{/if}

				{#if can('billing.invoice.view') || auth.tenant?.actor_org_type === 'reseller' || auth.tenant?.actor_org_type === 'customer'}
					<a
						href="/invoices"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/invoices')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12h6m-6 4h6m2 5H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z" />
						</svg>
						<span>{auth.tenant?.actor_org_type === 'customer' ? 'My Invoices' : 'Invoices & Payments'}</span>
					</a>
				{/if}

				{#if auth.tenant?.actor_org_type === 'platform' && can('ledger.reconcile')}
					<a
						href="/reconciliation"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/reconciliation')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-6 9l2 2 4-4" />
						</svg>
						<span>Ledger Reconciliation</span>
					</a>
				{/if}
			</div>
		{/if}

		<!-- Routing Engine (Platform-only) -->
		{#if can('routing.plan.view') || can('routing.assignment.view') || can('routing.rule.view') || can('routing.simulate') || can('routing.killswitch')}
			<div class="space-y-1">
				<div class="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-400 dark:text-slate-500">
					Routing Engine
				</div>

				{#if can('routing.plan.view')}
					<a
						href="/routing/plans"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/routing/plans')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 20l-5.447-2.724A1 1 0 013 16.382V5.618a1 1 0 011.447-.894L9 7m0 13l6-3m-6 3V7m6 10l4.553 2.276A1 1 0 0021 18.382V7.618a1 1 0 00-.553-.894L15 4m0 13V4m0 0L9 7" />
						</svg>
						<span>Route Plans</span>
					</a>
				{/if}

				{#if can('routing.assignment.view')}
					<a
						href="/routing/assignments"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/routing/assignments')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 7h12m0 0l-4-4m4 4l-4 4m0 6H4m0 0l4 4m-4-4l4-4" />
						</svg>
						<span>Customer Assignments</span>
					</a>
				{/if}

				{#if can('routing.rule.view')}
					<a
						href="/routing/rules"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/routing/rules')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 6V4m0 2a2 2 0 100 4m0-4a2 2 0 110 4m-6 8a2 2 0 100-4m0 4a2 2 0 110-4m0 4v2m0-6V4m6 6v10m6-2a2 2 0 100-4m0 4a2 2 0 110-4m0 4v2m0-6V4" />
						</svg>
						<span>Routing Rules</span>
					</a>
				{/if}

				{#if can('routing.simulate')}
					<a
						href="/routing/simulator"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/routing/simulator')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M14.752 11.168l-3.197-2.132A1 1 0 0010 9.87v4.263a1 1 0 001.555.832l3.197-2.132a1 1 0 000-1.664z" />
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
						</svg>
						<span>Route Simulator</span>
					</a>
				{/if}

				{#if can('routing.killswitch')}
					<a
						href="/routing/kill-switch"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/routing/kill-switch')
							? 'bg-rose-50 text-rose-700 dark:bg-rose-950/50 dark:text-rose-300'
							: 'text-rose-600 hover:bg-rose-50 dark:text-rose-400 dark:hover:bg-rose-950/30'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M18.364 18.364A9 9 0 005.636 5.636m12.728 12.728A9 9 0 015.636 5.636m12.728 12.728L5.636 5.636" />
						</svg>
						<span>Kill Switch</span>
					</a>
				{/if}
			</div>
		{/if}

		<!-- Messaging & Traffic -->
		{#if can('message.view') || auth.tenant?.actor_org_type === 'reseller' || auth.tenant?.actor_org_type === 'customer'}
			<div class="space-y-1">
				<div class="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-400 dark:text-slate-500">
					Messaging & Traffic
				</div>

				<a
					href="/messages"
					class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/messages')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 12h.01M12 12h.01M16 12h.01M21 12c0 4.418-4.03 8-9 8a9.863 9.863 0 01-4.255-.949L3 20l1.395-3.72C3.512 15.042 3 13.574 3 12c0-4.418 4.03-8 9-8s9 3.582 9 8z" />
					</svg>
					<span>{auth.tenant?.actor_org_type === 'customer' ? 'My Messages' : 'Messages Explorer'}</span>
				</a>

				{#if auth.tenant?.actor_org_type === 'platform' || can('message.platform.view')}
					<a
						href="/messaging/smpp-sessions"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/messaging/smpp-sessions')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z" />
						</svg>
						<span>SMPP Sessions</span>
					</a>
				{/if}

				{#if auth.tenant?.actor_org_type === 'platform' || can('message.queue.view')}
					<a
						href="/messaging/queue-monitor"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/messaging/queue-monitor')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 11H5m14 0a2 2 0 012 2v6a2 2 0 01-2 2H5a2 2 0 01-2-2v-6a2 2 0 012-2m14 0V9a2 2 0 00-2-2M5 11V9a2 2 0 012-2m0 0V5a2 2 0 012-2h6a2 2 0 012 2v2M7 7h10" />
						</svg>
						<span>Queue Backlog</span>
					</a>
				{/if}

				{#if auth.tenant?.actor_org_type === 'platform' || can('message.platform.view')}
					<a
						href="/messaging/unmatched-dlrs"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/messaging/unmatched-dlrs')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" />
						</svg>
						<span>Unmatched DLRs</span>
					</a>
				{/if}

				{#if auth.tenant?.actor_org_type === 'platform' || can('message.reconcile')}
					<a
						href="/messaging/reconciliation"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/messaging/reconciliation')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-6 9l2 2 4-4" />
						</svg>
						<span>Reconciliation & Settlement</span>
					</a>
				{/if}

				{#if auth.tenant?.actor_org_type === 'platform' || can('message.dlr_mapping.manage')}
					<a
						href="/messaging/dlr-mappings"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/messaging/dlr-mappings')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z" />
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 12a3 3 0 11-6 0 3 3 0 016 0z" />
						</svg>
						<span>DLR Mappings</span>
					</a>
				{/if}

				{#if auth.tenant?.actor_org_type === 'platform' || can('message.platform.view')}
					<a
						href="/messaging/customer-sessions"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/messaging/customer-sessions')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0z" />
						</svg>
						<span>Customer SMPP</span>
					</a>

					<a
						href="/messaging/webhooks"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/messaging/webhooks')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z" />
						</svg>
						<span>Webhook Logs</span>
					</a>

					<a
						href="/messaging/inbound-numbers"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/messaging/inbound-numbers')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M3 5a2 2 0 012-2h3.28a1 1 0 01.948.684l1.498 4.493a1 1 0 01-.502 1.21l-2.257 1.13a11.042 11.042 0 005.516 5.516l1.13-2.257a1 1 0 011.21-.502l4.493 1.498a1 1 0 01.684.949V19a2 2 0 01-2 2h-1C9.716 21 3 14.284 3 6V5z" />
						</svg>
						<span>Inbound Numbers</span>
					</a>

					<a
						href="/messaging/inbound-messages"
						class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/messaging/inbound-messages')
							? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
							: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
					>
						<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 4H6a2 2 0 00-2 2v12a2 2 0 002 2h12a2 2 0 002-2V6a2 2 0 00-2-2h-2m-4-1v8m0 0l3-3m-3 3L9 8m-5 5h2.586a1 1 0 01.707.293l2.414 2.414a1 1 0 00.707.293h3.172a1 1 0 00.707-.293l2.414-2.414a1 1 0 01.707-.293H20" />
						</svg>
						<span>Inbound Messages</span>
					</a>
				{/if}
			</div>
		{/if}

		<!-- Approvals -->
		{#if can('country.country.approve') || can('operator.operator.approve') || can('number.prefix.approve') || can('provider.provider.approve') || can('provider.connection.approve') || can('provider.coverage.approve') || can('customer.profile.approve') || can('customer.senderid.approve') || can('import.job.approve') || can('rate.buycard.approve') || can('rate.sellcard.approve') || can('rate.buyrate.approve') || can('rate.sellrate.approve') || can('rate.assignment.approve') || can('wallet.approve') || can('routing.plan.approve') || can('routing.entry.approve') || can('routing.assignment.approve') || can('routing.rule.approve')}
			<div class="space-y-1">
				<div class="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-400 dark:text-slate-500">
					Approvals
				</div>
				<a
					href="/master-data/pending-changes"
					class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/master-data/pending-changes')
						? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
						: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
				>
					<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z" />
					</svg>
					<span>Pending Changes</span>
				</a>
			</div>
		{/if}

		<!-- My Account — always visible to authenticated users -->
		<div class="space-y-1">
			<div class="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-slate-400 dark:text-slate-500">
				My Account
			</div>

			<a
				href="/sessions"
				class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/sessions')
					? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
					: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z" />
				</svg>
				<span>Sessions & Security</span>
			</a>

			<a
				href="/settings/password"
				class="flex items-center gap-3 rounded-lg px-3 py-2 text-xs font-semibold transition {isActive('/settings/password')
					? 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300'
					: 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/60'}"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 7a2 2 0 012 2m4 0a6 6 0 01-7.743 5.743L11 17H9v2H7v2H4a1 1 0 01-1-1v-2.586a1 1 0 01.293-.707l5.964-5.964A6 6 0 1121 9z" />
				</svg>
				<span>Change Password</span>
			</a>
		</div>
	</nav>

	<!-- Footer metadata -->
	<div class="border-t border-slate-200 p-4 text-[11px] text-slate-500 dark:border-slate-800 dark:text-slate-400">
		<div class="flex items-center justify-between">
			<span class="font-medium">User:</span>
			<span class="font-mono text-slate-700 dark:text-slate-300 truncate max-w-[140px]" title={auth.user?.email || ''}>
				{auth.user?.email || ''}
			</span>
		</div>
		<div class="mt-1 flex items-center justify-between">
			<span class="font-medium">Tenant:</span>
			<span class="font-mono text-indigo-600 dark:text-indigo-400">
				{auth.tenant?.actor_org_type ? auth.tenant.actor_org_type.toUpperCase() : ''}
			</span>
		</div>
	</div>
</aside>
