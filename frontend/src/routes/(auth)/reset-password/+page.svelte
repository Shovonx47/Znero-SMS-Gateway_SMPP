<script lang="ts">
	import { page } from '$app/stores';
	import { apiClient, ApiClientError } from '$lib/api/client';

	let token = $state($page.url.searchParams.get('token') || '');
	let newPassword = $state('');
	let confirmPassword = $state('');
	let isSubmitting = $state(false);
	let isSuccess = $state(false);
	let errorMessage = $state<string | null>(null);

	let passwordValid = $derived(newPassword.length >= 12);
	let passwordsMatch = $derived(newPassword === confirmPassword && confirmPassword.length > 0);

	async function handleSubmit(e: SubmitEvent) {
		e.preventDefault();
		if (!token.trim()) {
			errorMessage = 'Reset token is required.';
			return;
		}
		if (newPassword.length < 12) {
			errorMessage = 'Password must be at least 12 characters.';
			return;
		}
		if (newPassword !== confirmPassword) {
			errorMessage = 'Passwords do not match.';
			return;
		}

		isSubmitting = true;
		errorMessage = null;

		try {
			await apiClient.confirmPasswordReset(token.trim(), newPassword);
			isSuccess = true;
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to reset password. The link may have expired.';
			}
		} finally {
			isSubmitting = false;
		}
	}
</script>

<svelte:head>
	<title>Set New Password - Znero International</title>
</svelte:head>

<div class="mx-auto max-w-md py-12">
	<div class="rounded-2xl border border-slate-200 bg-white p-8 shadow-xs dark:border-slate-800 dark:bg-slate-900">
		<div class="mb-6 text-center">
			<h2 class="text-xl font-bold tracking-tight text-slate-900 dark:text-white">Set New Password</h2>
			<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
				Choose a strong password with at least 12 characters.
			</p>
		</div>

		{#if isSuccess}
			<div class="rounded-lg border border-emerald-200 bg-emerald-50 p-4 text-xs text-emerald-800 dark:border-emerald-900/50 dark:bg-emerald-950/40 dark:text-emerald-300">
				<p class="font-bold">Password Reset Successfully!</p>
				<p class="mt-1 text-slate-600 dark:text-slate-300">
					All previous sessions have been revoked for your security. You can now log in with your new credentials.
				</p>
				<div class="mt-4">
					<a href="/login" class="inline-block rounded-lg bg-indigo-600 px-4 py-2 font-semibold text-white hover:bg-indigo-500">
						Proceed to Sign In
					</a>
				</div>
			</div>
		{:else}
			{#if errorMessage}
				<div class="mb-5 rounded-lg border border-rose-200 bg-rose-50 p-3 text-xs text-rose-700 dark:border-rose-900/50 dark:bg-rose-950/40 dark:text-rose-300">
					{errorMessage}
				</div>
			{/if}

			<form onsubmit={handleSubmit} class="space-y-4">
				{#if !token}
					<div>
						<label for="token" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">
							Reset Token
						</label>
						<input
							id="token"
							type="text"
							bind:value={token}
							required
							placeholder="Enter reset token from email"
							class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
				{/if}

				<div>
					<label for="newpwd" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">
						New Password
					</label>
					<input
						id="newpwd"
						type="password"
						bind:value={newPassword}
						required
						minlength="12"
						placeholder="Minimum 12 characters"
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
					<div class="mt-1 flex items-center gap-1.5 text-[11px] {passwordValid ? 'text-emerald-600 dark:text-emerald-400' : 'text-slate-400'}">
						<span>{passwordValid ? '✓' : '•'}</span>
						<span>At least 12 characters ({newPassword.length}/12)</span>
					</div>
				</div>

				<div>
					<label for="confirmpwd" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">
						Confirm Password
					</label>
					<input
						id="confirmpwd"
						type="password"
						bind:value={confirmPassword}
						required
						placeholder="Re-enter password"
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
					{#if confirmPassword}
						<div class="mt-1 text-[11px] {passwordsMatch ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-500'}">
							{passwordsMatch ? '✓ Passwords match' : '✕ Passwords do not match'}
						</div>
					{/if}
				</div>

				<button
					type="submit"
					disabled={isSubmitting || !passwordValid || !passwordsMatch}
					class="flex w-full items-center justify-center rounded-lg bg-indigo-600 px-4 py-2.5 text-xs font-semibold text-white shadow-xs transition hover:bg-indigo-500 disabled:opacity-50"
				>
					{isSubmitting ? 'Updating Password...' : 'Reset Password'}
				</button>
			</form>
		{/if}
	</div>
</div>
