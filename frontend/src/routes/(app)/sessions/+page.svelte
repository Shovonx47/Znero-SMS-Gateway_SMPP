<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { LoginHistoryItem, SessionInfo } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let sessions = $state<SessionInfo[]>([]);
	let loginHistory = $state<LoginHistoryItem[]>([]);
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	async function loadSessionsAndHistory() {
		isLoading = true;
		errorMessage = null;

		try {
			const [sessRes, historyRes] = await Promise.all([
				apiClient.getSessions(),
				apiClient.getLoginHistory(1, 20)
			]);
			sessions = sessRes.data;
			loginHistory = historyRes.data.items;
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to load session details.';
			}
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadSessionsAndHistory();
	});

	async function revokeSession(session: SessionInfo) {
		if (!confirm(`Are you sure you want to revoke session from ${session.ip_address}?`)) return;

		try {
			await apiClient.revokeSession(session.id);
			if (session.is_current) {
				await auth.logout();
				goto('/login');
				return;
			}
			await loadSessionsAndHistory();
		} catch (err) {
			alert(err instanceof Error ? err.message : 'Failed to revoke session');
		}
	}

	async function handleLogoutAll() {
		if (!confirm('Are you sure you want to sign out of all active devices? You will be redirected to log in.')) return;

		try {
			await apiClient.logoutAll();
			await auth.logout();
			goto('/login');
		} catch (err) {
			alert(err instanceof Error ? err.message : 'Failed to sign out all devices');
		}
	}
</script>

<svelte:head>
	<title>Sessions & Security - Znero International</title>
</svelte:head>

<div class="space-y-8">
	<!-- Page Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-xl font-bold tracking-tight text-slate-900 dark:text-white">Active Sessions & Security</h1>
			<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
				Inspect active authenticated devices, revoke suspicious sessions, and view recent login history.
			</p>
		</div>

		<button
			type="button"
			onclick={handleLogoutAll}
			class="inline-flex items-center gap-2 rounded-lg bg-rose-600 px-3.5 py-2 text-xs font-semibold text-white shadow-xs transition hover:bg-rose-500 focus:outline-none focus:ring-2 focus:ring-rose-500"
		>
			<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1" />
			</svg>
			Revoke All Devices
		</button>
	</div>

	<!-- MFA Status Card -->
	<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-2xs dark:border-slate-800 dark:bg-slate-900">
		<div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
			<div class="flex items-center gap-3">
				<div class="flex h-10 w-10 items-center justify-center rounded-lg {auth.user?.mfa_enabled ? 'bg-purple-100 text-purple-700 dark:bg-purple-900/50 dark:text-purple-300' : 'bg-slate-100 text-slate-500 dark:bg-slate-800'}">
					<svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z" />
					</svg>
				</div>
				<div>
					<h3 class="text-sm font-bold text-slate-900 dark:text-white">
						Two-Factor Authentication (TOTP)
					</h3>
					<p class="text-xs text-slate-500 dark:text-slate-400">
						{#if auth.user?.mfa_enabled}
							Active and protecting your account with time-based one-time passwords.
						{:else}
							Not configured. We strongly recommend enabling TOTP authentication.
						{/if}
					</p>
				</div>
			</div>

			<a
				href="/mfa/setup"
				class="inline-flex items-center gap-1.5 rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-2xs hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200 dark:hover:bg-slate-700"
			>
				{auth.user?.mfa_enabled ? 'Re-enroll / View Setup' : 'Set Up Two-Factor Authentication'}
			</a>
		</div>
	</div>

	<!-- Sessions Table -->
	<div>
		<h2 class="text-sm font-bold text-slate-900 dark:text-white mb-3">Active Sessions</h2>

		{#if isLoading}
			<Loading message="Loading sessions..." />
		{:else if errorMessage}
			<ErrorState title="Error Loading Sessions" message={errorMessage} retry={loadSessionsAndHistory} />
		{:else if sessions.length === 0}
			<Empty title="No Sessions Found" description="No active sessions registered." />
		{:else}
			<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-2xs dark:border-slate-800 dark:bg-slate-900">
				<div class="overflow-x-auto">
					<table class="w-full text-left text-xs">
						<thead class="border-b border-slate-200 bg-slate-50/75 text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/40 dark:text-slate-400">
							<tr>
								<th class="px-5 py-3">Device / User Agent</th>
								<th class="px-4 py-3">IP Address</th>
								<th class="px-4 py-3">Last Active</th>
								<th class="px-4 py-3">Created</th>
								<th class="px-5 py-3 text-right">Action</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
							{#each sessions as s}
								<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/30 transition">
									<td class="px-5 py-3.5">
										<div class="flex items-center gap-2">
											<span class="font-medium text-slate-800 dark:text-slate-200 truncate max-w-[280px]" title={s.user_agent}>
												{s.user_agent || 'Unknown Client'}
											</span>
											{#if s.is_current}
												<span class="rounded bg-indigo-50 px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider text-indigo-700 dark:bg-indigo-950/40 dark:text-indigo-300">
													Current Session
												</span>
											{/if}
										</div>
										<div class="font-mono text-[10px] text-slate-400">{s.id}</div>
									</td>
									<td class="px-4 py-3.5 font-mono text-slate-600 dark:text-slate-300">
										{s.ip_address}
									</td>
									<td class="px-4 py-3.5 text-slate-500 dark:text-slate-400">
										{new Date(s.last_used_at).toLocaleString()}
									</td>
									<td class="px-4 py-3.5 text-slate-500 dark:text-slate-400">
										{new Date(s.created_at).toLocaleString()}
									</td>
									<td class="px-5 py-3.5 text-right">
										<button
											type="button"
											onclick={() => revokeSession(s)}
											class="font-semibold text-rose-600 hover:text-rose-500"
										>
											Revoke
										</button>
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			</div>
		{/if}
	</div>

	<!-- Login History Table -->
	<div>
		<h2 class="text-sm font-bold text-slate-900 dark:text-white mb-3">Recent Login Activity</h2>

		<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-2xs dark:border-slate-800 dark:bg-slate-900">
			<div class="overflow-x-auto">
				<table class="w-full text-left text-xs">
					<thead class="border-b border-slate-200 bg-slate-50/75 text-[11px] font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/40 dark:text-slate-400">
						<tr>
							<th class="px-5 py-3">Timestamp</th>
							<th class="px-4 py-3">Outcome</th>
							<th class="px-4 py-3">Email</th>
							<th class="px-4 py-3">IP Address</th>
							<th class="px-4 py-3">Client Agent</th>
							<th class="px-5 py-3">Reason / Details</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
						{#each loginHistory as entry}
							<tr class="hover:bg-slate-50/50 dark:hover:bg-slate-800/30 transition">
								<td class="px-5 py-3 font-mono text-[11px] text-slate-500">
									{new Date(entry.created_at).toLocaleString()}
								</td>
								<td class="px-4 py-3">
									<span class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[10px] font-semibold {entry.success ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300' : 'bg-rose-50 text-rose-700 dark:bg-rose-950/40 dark:text-rose-300'}">
										<span class="h-1.5 w-1.5 rounded-full {entry.success ? 'bg-emerald-500' : 'bg-rose-500'}"></span>
										{entry.success ? 'Success' : 'Failed'}
									</span>
								</td>
								<td class="px-4 py-3 font-semibold text-slate-800 dark:text-slate-200">
									{entry.email}
								</td>
								<td class="px-4 py-3 font-mono text-[11px] text-slate-600 dark:text-slate-400">
									{entry.ip_address}
								</td>
								<td class="px-4 py-3 text-[11px] text-slate-500 truncate max-w-[180px]" title={entry.user_agent}>
									{entry.user_agent}
								</td>
								<td class="px-5 py-3 font-mono text-[11px] text-slate-600 dark:text-slate-300">
									{entry.reason}
								</td>
							</tr>
						{/each}
						{#if loginHistory.length === 0 && !isLoading}
							<tr>
								<td colspan="6" class="px-5 py-4 text-center text-slate-400">
									No login history records found.
								</td>
							</tr>
						{/if}
					</tbody>
				</table>
			</div>
		</div>
	</div>
</div>
