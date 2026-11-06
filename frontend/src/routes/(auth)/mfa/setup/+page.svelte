<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { MFASetupResponse } from '$lib/types/api';

	let setupData = $state<MFASetupResponse | null>(null);
	let confirmCode = $state('');
	let isLoading = $state(true);
	let isSubmitting = $state(false);
	let errorMessage = $state<string | null>(null);
	let successMessage = $state<string | null>(null);
	let copiedSecret = $state(false);
	let copiedCodes = $state(false);

	onMount(async () => {
		try {
			const res = await apiClient.setupMFA();
			setupData = res.data;
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to generate MFA enrollment details.';
			}
		} finally {
			isLoading = false;
		}
	});

	function copySecret() {
		if (!setupData) return;
		navigator.clipboard.writeText(setupData.secret);
		copiedSecret = true;
		setTimeout(() => (copiedSecret = false), 2000);
	}

	function copyCodes() {
		if (!setupData) return;
		navigator.clipboard.writeText(setupData.recovery_codes.join('\n'));
		copiedCodes = true;
		setTimeout(() => (copiedCodes = false), 2000);
	}

	async function handleConfirm(e: SubmitEvent) {
		e.preventDefault();
		if (!setupData || !confirmCode.trim()) return;

		isSubmitting = true;
		errorMessage = null;

		try {
			await apiClient.confirmMFA(setupData.secret, confirmCode.trim(), setupData.recovery_codes);
			successMessage = 'Two-factor authentication has been successfully activated on your account.';
			await auth.init();
			setTimeout(() => {
				goto('/sessions');
			}, 2500);
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Invalid code. Please ensure the code from your app is correct.';
			}
		} finally {
			isSubmitting = false;
		}
	}
</script>

<svelte:head>
	<title>Set Up Two-Factor Authentication - Znero</title>
</svelte:head>

<div class="mx-auto max-w-2xl py-8">
	<div class="rounded-2xl border border-slate-200 bg-white p-8 shadow-xs dark:border-slate-800 dark:bg-slate-900">
		<div class="mb-6">
			<h2 class="text-xl font-bold tracking-tight text-slate-900 dark:text-white">Configure Two-Factor Authentication</h2>
			<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
				Enhance your account security with RFC 6238 time-based one-time passwords (TOTP).
			</p>
		</div>

		{#if successMessage}
			<div class="mb-6 rounded-lg border border-emerald-200 bg-emerald-50 p-4 text-xs text-emerald-800 dark:border-emerald-900/50 dark:bg-emerald-950/40 dark:text-emerald-300">
				<div class="flex items-center gap-2 font-bold">
					<svg class="h-4 w-4 text-emerald-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7" />
					</svg>
					Setup Completed!
				</div>
				<p class="mt-1 pl-6">{successMessage} Redirecting...</p>
			</div>
		{/if}

		{#if errorMessage}
			<div class="mb-6 rounded-lg border border-rose-200 bg-rose-50 p-4 text-xs text-rose-700 dark:border-rose-900/50 dark:bg-rose-950/40 dark:text-rose-300">
				<div class="flex items-center gap-2 font-bold">
					<svg class="h-4 w-4 text-rose-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
					</svg>
					Enrollment Error
				</div>
				<p class="mt-1 pl-6">{errorMessage}</p>
			</div>
		{/if}

		{#if isLoading}
			<div class="flex h-48 items-center justify-center">
				<div class="flex items-center gap-2 text-xs text-slate-500">
					<svg class="h-5 w-5 animate-spin text-indigo-600" viewBox="0 0 24 24">
						<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" fill="none"></circle>
						<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v8H4z"></path>
					</svg>
					Generating cryptographic keys...
				</div>
			</div>
		{:else if setupData}
			<div class="space-y-6">
				<!-- Step 1: Secret Key -->
				<div class="rounded-xl border border-slate-200 bg-slate-50/50 p-5 dark:border-slate-800 dark:bg-slate-800/40">
					<div class="text-xs font-bold uppercase tracking-wider text-slate-500 dark:text-slate-400">Step 1: Add to Authenticator App</div>
					<p class="mt-1 text-xs text-slate-600 dark:text-slate-300">
						Enter the key below into Google Authenticator, Authy, 1Password, or Bitwarden:
					</p>

					<div class="mt-3 flex items-center justify-between rounded-lg border border-slate-300 bg-white px-3 py-2 dark:border-slate-700 dark:bg-slate-900">
						<code class="font-mono text-sm font-semibold tracking-wider text-indigo-600 dark:text-indigo-400">
							{setupData.secret}
						</code>
						<button
							type="button"
							onclick={copySecret}
							class="rounded bg-slate-100 px-2 py-1 text-[11px] font-semibold text-slate-700 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-200"
						>
							{copiedSecret ? 'Copied!' : 'Copy Key'}
						</button>
					</div>

					<div class="mt-2 text-[11px] text-slate-500 truncate" title={setupData.uri}>
						<span class="font-semibold">URI:</span> {setupData.uri}
					</div>
				</div>

				<!-- Step 2: Emergency Recovery Codes -->
				<div class="rounded-xl border border-slate-200 bg-slate-50/50 p-5 dark:border-slate-800 dark:bg-slate-800/40">
					<div class="flex items-center justify-between">
						<div>
							<div class="text-xs font-bold uppercase tracking-wider text-slate-500 dark:text-slate-400">Step 2: Save Emergency Recovery Codes</div>
							<p class="mt-1 text-xs text-slate-600 dark:text-slate-300">
								Each code can be used exactly once if you lose access to your authenticator app.
							</p>
						</div>
						<button
							type="button"
							onclick={copyCodes}
							class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 shadow-2xs hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
						>
							{copiedCodes ? 'Codes Copied!' : 'Copy All 10 Codes'}
						</button>
					</div>

					<div class="mt-4 grid grid-cols-2 sm:grid-cols-5 gap-2">
						{#each setupData.recovery_codes as code, i}
							<div class="rounded-md border border-slate-200 bg-white px-2 py-1.5 text-center font-mono text-xs text-slate-700 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300">
								<span class="text-[10px] text-slate-400 mr-1">{i + 1}.</span>{code}
							</div>
						{/each}
					</div>
				</div>

				<!-- Step 3: Confirmation -->
				<form onsubmit={handleConfirm} class="rounded-xl border border-indigo-200 bg-indigo-50/40 p-5 dark:border-indigo-900/40 dark:bg-indigo-950/20">
					<div class="text-xs font-bold uppercase tracking-wider text-indigo-700 dark:text-indigo-400">Step 3: Confirm Activation</div>
					<p class="mt-1 text-xs text-slate-600 dark:text-slate-300">
						Enter the 6-digit code currently displayed in your authenticator app to confirm setup:
					</p>

					<div class="mt-3 flex gap-3">
						<input
							type="text"
							bind:value={confirmCode}
							required
							maxlength="6"
							placeholder="123456"
							pattern="[0-9]{'{6}'}"
							class="w-36 rounded-lg border border-slate-300 bg-white px-3 py-2 text-center font-mono text-base tracking-widest text-slate-900 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
						<button
							type="submit"
							disabled={isSubmitting}
							class="flex-1 rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-xs transition hover:bg-indigo-500 focus:outline-none focus:ring-2 focus:ring-indigo-500 disabled:opacity-50"
						>
							{isSubmitting ? 'Confirming...' : 'Confirm & Enable 2FA'}
						</button>
					</div>
				</form>
			</div>
		{/if}
	</div>
</div>
