<script lang="ts">
	import { apiClient, ApiClientError } from '$lib/api/client';

	let email = $state('');
	let isSubmitting = $state(false);
	let message = $state<string | null>(null);
	let errorMessage = $state<string | null>(null);

	async function handleSubmit(e: SubmitEvent) {
		e.preventDefault();
		if (!email.trim()) return;

		isSubmitting = true;
		errorMessage = null;

		try {
			await apiClient.requestPasswordReset(email.trim());
			message = 'If an active account exists for that email, password reset instructions have been dispatched. In local development, check the backend console for the reset link.';
		} catch (err) {
			if (err instanceof ApiClientError) {
				errorMessage = err.message;
			} else {
				errorMessage = 'Unable to send password reset request. Please check your connection.';
			}
		} finally {
			isSubmitting = false;
		}
	}
</script>

<svelte:head>
	<title>Reset Password - Znero International</title>
</svelte:head>

<div class="mx-auto max-w-md py-12">
	<div class="rounded-2xl border border-slate-200 bg-white p-8 shadow-xs dark:border-slate-800 dark:bg-slate-900">
		<div class="mb-6 text-center">
			<h2 class="text-xl font-bold tracking-tight text-slate-900 dark:text-white">Forgot Password</h2>
			<p class="mt-1 text-xs text-slate-500 dark:text-slate-400">
				Enter your verified email to receive a password recovery link.
			</p>
		</div>

		{#if message}
			<div class="mb-6 rounded-lg border border-emerald-200 bg-emerald-50 p-4 text-xs text-emerald-800 dark:border-emerald-900/50 dark:bg-emerald-950/40 dark:text-emerald-300">
				<p class="font-semibold">Instructions Dispatched</p>
				<p class="mt-1 text-slate-600 dark:text-slate-300">{message}</p>
				<div class="mt-4">
					<a href="/login" class="font-semibold text-emerald-700 underline dark:text-emerald-300">
						Return to Sign In
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
				<div>
					<label for="email" class="block text-xs font-semibold text-slate-700 dark:text-slate-300">
						Account Email
					</label>
					<input
						id="email"
						type="email"
						bind:value={email}
						required
						placeholder="user@znero.internal"
						class="mt-1 block w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-xs text-slate-900 placeholder-slate-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				<button
					type="submit"
					disabled={isSubmitting}
					class="flex w-full items-center justify-center rounded-lg bg-indigo-600 px-4 py-2.5 text-xs font-semibold text-white shadow-xs transition hover:bg-indigo-500 disabled:opacity-50"
				>
					{isSubmitting ? 'Sending Request...' : 'Send Recovery Link'}
				</button>
			</form>

			<div class="mt-6 border-t border-slate-200 pt-4 text-center text-xs dark:border-slate-800">
				<a href="/login" class="font-semibold text-indigo-600 hover:text-indigo-500 dark:text-indigo-400">
					Back to Sign In
				</a>
			</div>
		{/if}
	</div>
</div>
