<script lang="ts">
	import { page } from '$app/stores';
	import { apiClient, ApiClientError } from '$lib/api/client';

	let token = $state($page.url.searchParams.get('token') || '');
	let password = $state('');
	let confirmPassword = $state('');
	let isSubmitting = $state(false);
	let isSuccess = $state(false);
	let errorMessage = $state<string | null>(null);

	let passwordValid = $derived(password.length >= 12);
	let passwordsMatch = $derived(password === confirmPassword && confirmPassword.length > 0);

	async function handleSubmit(e: SubmitEvent) {
		e.preventDefault();
		if (!token.trim()) {
			errorMessage = 'Invitation token is required.';
			return;
		}
		if (password.length < 12) {
			errorMessage = 'Password must be at least 12 characters.';
			return;
		}
		if (password !== confirmPassword) {
			errorMessage = 'Passwords do not match.';
			return;
		}

		isSubmitting = true;
		errorMessage = null;

		try {
			await apiClient.acceptInvitation(token.trim(), password);
			isSuccess = true;
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to accept invitation. The invitation may be invalid or expired.';
			}
		} finally {
			isSubmitting = false;
		}
	}
</script>

<svelte:head>
	<title>Accept Organization Invitation - Znero</title>
</svelte:head>

<div class="mx-auto max-w-md py-12">
	<div class="rounded-2xl border border-slate-200 bg-white p-8 shadow-xs dark:border-slate-800 dark:bg-slate-900">
		<div class="mb-6 text-center">
			<div class="mx-auto mb-3 flex h-12 w-12 items-center justify-center rounded-xl bg-indigo-600 text-white font-bold text-xl shadow-sm">
				Z
			</div>
			<h2 class="text-xl font-bold tracking-tight text-slate-900 dark:text-white">Accept Invitation</h2>
			<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
				Welcome to Znero International. Set your initial password to activate your account.
			</p>
		</div>

		{#if isSuccess}
			<div class="rounded-lg border border-emerald-200 bg-emerald-50 p-4 text-xs text-emerald-800 dark:border-emerald-900/50 dark:bg-emerald-950/40 dark:text-emerald-300">
				<p class="font-bold">Account Activated Successfully!</p>
				<p class="mt-1 text-slate-600 dark:text-slate-300">
					Your password has been set and your account is active. You can now sign in.
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
							Invitation Token
						</label>
						<input
							id="token"
							type="text"
							bind:value={token}
							required
							placeholder="Enter invitation token from email"
							class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
						/>
					</div>
				{/if}

				<div>
					<label for="pwd" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">
						Set Password
					</label>
					<input
						id="pwd"
						type="password"
						bind:value={password}
						required
						minlength="12"
						placeholder="Minimum 12 characters"
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
					<div class="mt-1 flex items-center gap-1.5 text-[11px] {passwordValid ? 'text-emerald-600 dark:text-emerald-400' : 'text-slate-400'}">
						<span>{passwordValid ? '✓' : '•'}</span>
						<span>At least 12 characters ({password.length}/12)</span>
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
					{isSubmitting ? 'Activating Account...' : 'Set Password & Activate'}
				</button>
			</form>
		{/if}
	</div>
</div>
