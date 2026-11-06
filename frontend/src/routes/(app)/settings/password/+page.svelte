<script lang="ts">
	import { apiClient, ApiClientError } from '$lib/api/client';

	let currentPassword = $state('');
	let newPassword = $state('');
	let confirmPassword = $state('');
	let isSubmitting = $state(false);
	let successMessage = $state<string | null>(null);
	let errorMessage = $state<string | null>(null);

	let passwordValid = $derived(newPassword.length >= 12);
	let passwordsMatch = $derived(newPassword === confirmPassword && confirmPassword.length > 0);

	async function handleSubmit(e: SubmitEvent) {
		e.preventDefault();
		if (!currentPassword || !passwordValid || !passwordsMatch) {
			errorMessage = 'Please complete all fields with a valid password.';
			return;
		}

		isSubmitting = true;
		errorMessage = null;
		successMessage = null;

		try {
			await apiClient.changePassword(currentPassword, newPassword);
			successMessage = 'Your password has been changed successfully. Other active sessions have been invalidated.';
			currentPassword = '';
			newPassword = '';
			confirmPassword = '';
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Failed to change password.';
			}
		} finally {
			isSubmitting = false;
		}
	}
</script>

<svelte:head>
	<title>Change Password - Znero International</title>
</svelte:head>

<div class="mx-auto max-w-lg py-8">
	<div class="rounded-2xl border border-slate-200 bg-white p-8 shadow-xs dark:border-slate-800 dark:bg-slate-900">
		<div class="mb-6">
			<h1 class="text-xl font-bold tracking-tight text-slate-900 dark:text-white">Change Account Password</h1>
			<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
				Updating your password uses Argon2id hashing and automatically revokes all other active sessions.
			</p>
		</div>

		{#if successMessage}
			<div class="mb-5 rounded-lg border border-emerald-200 bg-emerald-50 p-4 text-xs text-emerald-800 dark:border-emerald-900/50 dark:bg-emerald-950/40 dark:text-emerald-300">
				<div class="flex items-center gap-2 font-bold">
					<svg class="h-4 w-4 text-emerald-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7" />
					</svg>
					Password Updated
				</div>
				<p class="mt-1 pl-6">{successMessage}</p>
			</div>
		{/if}

		{#if errorMessage}
			<div class="mb-5 rounded-lg border border-rose-200 bg-rose-50 p-3.5 text-xs text-rose-700 dark:border-rose-900/50 dark:bg-rose-950/40 dark:text-rose-300">
				{errorMessage}
			</div>
		{/if}

		<form onsubmit={handleSubmit} class="space-y-4">
			<div>
				<label for="curpwd" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Current Password</label>
				<input
					id="curpwd"
					type="password"
					bind:value={currentPassword}
					required
					autocomplete="current-password"
					class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				/>
			</div>

			<div>
				<label for="newpwd" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">New Password</label>
				<input
					id="newpwd"
					type="password"
					bind:value={newPassword}
					required
					minlength="12"
					placeholder="Minimum 12 characters"
					autocomplete="new-password"
					class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:bg-slate-800 dark:text-white"
				/>
				<div class="mt-1 flex items-center gap-1.5 text-[11px] {passwordValid ? 'text-emerald-600 dark:text-emerald-400' : 'text-slate-400'}">
					<span>{passwordValid ? '✓' : '•'}</span>
					<span>At least 12 characters ({newPassword.length}/12)</span>
				</div>
			</div>

			<div>
				<label for="confirmpwd" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">Confirm New Password</label>
				<input
					id="confirmpwd"
					type="password"
					bind:value={confirmPassword}
					required
					placeholder="Re-enter new password"
					autocomplete="new-password"
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
				disabled={isSubmitting || !passwordValid || !passwordsMatch || !currentPassword}
				class="flex w-full items-center justify-center rounded-lg bg-indigo-600 px-4 py-2.5 text-xs font-semibold text-white shadow-xs transition hover:bg-indigo-500 disabled:opacity-50"
			>
				{isSubmitting ? 'Updating...' : 'Update Password'}
			</button>
		</form>
	</div>
</div>
