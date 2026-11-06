<!--
  (auth)/login/+page.svelte
  Public login page — rendered inside (auth) layout which has NO sidebar/topbar.

  Behaviours:
  - On successful login → redirect to ?next= param if present, else /dashboard.
  - On MFA required → redirect to /mfa/challenge with the challenge token in sessionStorage.
  - Authenticated users are already sent away by the (auth)/+layout.svelte guard.
-->
<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { get } from 'svelte/store';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';

	let email = $state('');
	let password = $state('');
	let isLoading = $state(false);
	let errorMessage = $state<string | null>(null);
	let infoMessage = $state<string | null>(null);

	onMount(() => {
		const reason = get(page).url.searchParams.get('reason');
		if (reason === 'expired') {
			infoMessage = 'Your session has expired. Please sign in again.';
		}
	});

	async function handleLogin(e: SubmitEvent) {
		e.preventDefault();
		if (!email.trim() || !password) {
			errorMessage = 'Please enter both email and password.';
			return;
		}

		isLoading = true;
		errorMessage = null;

		try {
			const res = await apiClient.login(email.trim(), password);

			if (res.data.mfa_required && res.data.mfa_challenge_token) {
				// Store challenge token in sessionStorage and redirect to challenge page.
				// sessionStorage is cleared when the tab closes — acceptable for a temporary token.
				sessionStorage.setItem('znero_mfa_token', res.data.mfa_challenge_token);
				sessionStorage.setItem('znero_mfa_email', email.trim());
				await goto('/mfa/challenge');
				return;
			}

			// Login succeeded — populate auth state, then navigate.
			await auth.init();

			// Honour the ?next= redirect param (strip the leading / to avoid open-redirect).
			const nextParam = get(page).url.searchParams.get('next');
			const destination = nextParam && nextParam.startsWith('/') ? nextParam : '/dashboard';
			await goto(destination, { replaceState: true });
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Unable to connect to the server. Please verify your connection.';
			}
		} finally {
			isLoading = false;
		}
	}
</script>

<svelte:head>
	<title>Sign In — Znero International</title>
</svelte:head>

<div class="w-full max-w-md">
	<div class="rounded-2xl border border-slate-200 bg-white p-8 shadow-xs dark:border-slate-800 dark:bg-slate-900">
		<div class="mb-6 text-center">
			<h2 class="text-xl font-bold tracking-tight text-slate-900 dark:text-white">Sign In to Znero</h2>
			<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">Wholesale SMS Switching & Routing Platform</p>
		</div>

		{#if infoMessage}
			<div class="mb-5 rounded-lg border border-amber-200 bg-amber-50 p-3.5 text-xs text-amber-800 dark:border-amber-900/50 dark:bg-amber-950/40 dark:text-amber-300">
				<div class="flex items-center gap-2 font-semibold">
					<svg class="h-4 w-4 shrink-0 text-amber-500" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
					</svg>
					Session Expired
				</div>
				<p class="mt-1 pl-6">{infoMessage}</p>
			</div>
		{/if}

		{#if errorMessage}
			<div class="mb-5 rounded-lg border border-rose-200 bg-rose-50 p-3.5 text-xs text-rose-700 dark:border-rose-900/50 dark:bg-rose-950/40 dark:text-rose-300">
				<div class="flex items-center gap-2 font-semibold">
					<svg class="h-4 w-4 shrink-0 text-rose-500" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
					</svg>
					Authentication Error
				</div>
				<p class="mt-1 pl-6">{errorMessage}</p>
			</div>
		{/if}

		<form onsubmit={handleLogin} class="space-y-4">
			<div>
				<label for="email" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">
					Email Address
				</label>
				<input
					id="email"
					type="email"
					bind:value={email}
					required
					placeholder="admin@example.com"
					autocomplete="email"
					class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white dark:placeholder-slate-500"
				/>
			</div>

			<div>
				<div class="flex items-center justify-between">
					<label for="password" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">
						Password
					</label>
					<a href="/forgot-password" class="text-xs font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400">
						Forgot password?
					</a>
				</div>
				<input
					id="password"
					type="password"
					bind:value={password}
					required
					placeholder="••••••••••••"
					autocomplete="current-password"
					class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white dark:placeholder-slate-500"
				/>
			</div>

			<button
				type="submit"
				disabled={isLoading}
				class="mt-2 flex w-full items-center justify-center rounded-lg bg-indigo-600 px-4 py-2.5 text-xs font-semibold text-white shadow-xs transition hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-500 disabled:opacity-50"
			>
				{#if isLoading}
					<svg class="mr-2 h-4 w-4 animate-spin" viewBox="0 0 24 24">
						<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" fill="none"></circle>
						<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v8H4z"></path>
					</svg>
					Authenticating...
				{:else}
					Sign In
				{/if}
			</button>
		</form>

		<div class="mt-6 border-t border-slate-200 pt-4 text-center text-xs text-slate-500 dark:border-slate-800 dark:text-slate-400">
			Invited to an organization? <a href="/accept-invitation" class="font-semibold text-indigo-600 hover:text-indigo-500 dark:text-indigo-400">Accept Invitation</a>
		</div>
	</div>
</div>
