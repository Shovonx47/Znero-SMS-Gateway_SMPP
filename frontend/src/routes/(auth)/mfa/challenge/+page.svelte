<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';

	let challengeToken = $state('');
	let email = $state('');
	let code = $state('');
	let isRecoveryMode = $state(false);
	let isLoading = $state(false);
	let errorMessage = $state<string | null>(null);

	onMount(() => {
		const storedToken = sessionStorage.getItem('znero_mfa_token');
		const storedEmail = sessionStorage.getItem('znero_mfa_email');
		if (!storedToken) {
			goto('/login');
			return;
		}
		challengeToken = storedToken;
		email = storedEmail || '';
	});

	async function handleVerify(e: SubmitEvent) {
		e.preventDefault();
		if (!code.trim()) {
			errorMessage = isRecoveryMode ? 'Please enter a 10-character recovery code.' : 'Please enter your 6-digit TOTP code.';
			return;
		}

		isLoading = true;
		errorMessage = null;

		try {
			await apiClient.verifyMFA(challengeToken, code.trim());
			sessionStorage.removeItem('znero_mfa_token');
			sessionStorage.removeItem('znero_mfa_email');
			await auth.init();
			goto('/');
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Verification failed. Please check your code and try again.';
			}
		} finally {
			isLoading = false;
		}
	}
</script>

<svelte:head>
	<title>Two-Factor Authentication - Znero International</title>
</svelte:head>

<div class="mx-auto max-w-md py-12">
	<div class="rounded-2xl border border-slate-200 bg-white p-8 shadow-xs dark:border-slate-800 dark:bg-slate-900">
		<div class="mb-6 text-center">
			<div class="mx-auto mb-3 flex h-12 w-12 items-center justify-center rounded-xl bg-purple-600 text-white font-bold text-xl shadow-sm">
				<svg class="h-6 w-6" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z" />
				</svg>
			</div>
			<h2 class="text-xl font-bold tracking-tight text-slate-900 dark:text-white">Two-Factor Authentication</h2>
			{#if email}
				<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">Verifying identity for <span class="font-semibold text-slate-700 dark:text-slate-300">{email}</span></p>
			{/if}
		</div>

		{#if errorMessage}
			<div class="mb-5 rounded-lg border border-rose-200 bg-rose-50 p-3.5 text-xs text-rose-700 dark:border-rose-900/50 dark:bg-rose-950/40 dark:text-rose-300">
				<div class="flex items-center gap-2 font-semibold">
					<svg class="h-4 w-4 shrink-0 text-rose-500" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
					</svg>
					Verification Error
				</div>
				<p class="mt-1 pl-6">{errorMessage}</p>
			</div>
		{/if}

		<form onsubmit={handleVerify} class="space-y-4">
			{#if !isRecoveryMode}
				<div>
					<label for="totp" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">
						6-Digit Authenticator Code
					</label>
					<input
						id="totp"
						type="text"
						bind:value={code}
						required
						maxlength="6"
						pattern="[0-9]{'{6}'}"
						placeholder="123456"
						autocomplete="one-time-code"
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-center font-mono text-lg tracking-widest text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white dark:placeholder-slate-500"
					/>
					<p class="mt-1.5 text-[11px] text-slate-500 dark:text-slate-400">
						Open Google Authenticator, Authy, or your password manager.
					</p>
				</div>
			{:else}
				<div>
					<label for="recovery" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">
						Single-Use Recovery Code
					</label>
					<input
						id="recovery"
						type="text"
						bind:value={code}
						required
						placeholder="e.g. 7k9m2x4p8q"
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-center font-mono text-sm tracking-wider text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white dark:placeholder-slate-500"
					/>
					<p class="mt-1.5 text-[11px] text-slate-500 dark:text-slate-400">
						Enter one of the 10 emergency recovery codes saved during MFA enrollment.
					</p>
				</div>
			{/if}

			<button
				type="submit"
				disabled={isLoading}
				class="flex w-full items-center justify-center rounded-lg bg-indigo-600 px-4 py-2.5 text-xs font-semibold text-white shadow-xs transition hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-500 disabled:opacity-50"
			>
				{#if isLoading}
					<svg class="mr-2 h-4 w-4 animate-spin" viewBox="0 0 24 24">
						<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" fill="none"></circle>
						<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v8H4z"></path>
					</svg>
					Verifying...
				{:else}
					Verify & Complete Sign In
				{/if}
			</button>
		</form>

		<div class="mt-6 flex items-center justify-between border-t border-slate-200 pt-4 text-xs dark:border-slate-800">
			<button
				type="button"
				onclick={() => {
					isRecoveryMode = !isRecoveryMode;
					code = '';
					errorMessage = null;
				}}
				class="font-medium text-indigo-600 hover:text-indigo-500 dark:text-indigo-400"
			>
				{isRecoveryMode ? 'Use Authenticator App' : 'Use a recovery code'}
			</button>

			<a href="/login" class="text-slate-500 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-200">
				Back to Sign In
			</a>
		</div>
	</div>
</div>
