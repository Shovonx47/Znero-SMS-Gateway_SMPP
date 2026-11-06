<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { apiClient } from '$lib/api/client';
	import Loading from '$lib/components/Loading.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	const invoiceId = $derived($page.params.id);

	let htmlContent = $state('');
	let loading = $state(true);
	let errorMessage = $state('');

	async function loadInvoiceHTML() {
		loading = true;
		errorMessage = '';
		try {
			htmlContent = await apiClient.getPrintableInvoiceHTML(invoiceId);
		} catch (err: any) {
			errorMessage = err.message || 'Failed to load printable invoice';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		loadInvoiceHTML();
	});
</script>

<div class="space-y-4">
	<!-- Top Bar -->
	<div class="flex items-center justify-between no-print">
		<div class="flex items-center gap-2 text-xs text-slate-500">
			<a href="/invoices" class="hover:text-indigo-600 font-medium">&larr; Back to Invoices</a>
			<span>/</span>
			<span class="font-mono text-slate-900 dark:text-white">{invoiceId.slice(0, 8)}...</span>
		</div>

		<button
			onclick={() => window.print()}
			class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-700"
		>
			Print / Save as PDF
		</button>
	</div>

	{#if loading}
		<Loading message="Rendering invoice document..." />
	{:else if errorMessage}
		<ErrorState message={errorMessage} retry={loadInvoiceHTML} />
	{:else}
		<!-- Printable HTML Container -->
		<div class="rounded-xl border border-slate-200 bg-white p-2 shadow-sm dark:border-slate-800">
			<iframe
				srcdoc={htmlContent}
				title="Invoice Document"
				class="w-full min-h-[850px] border-0 rounded-lg bg-white"
			></iframe>
		</div>
	{/if}
</div>

<style>
	@media print {
		:global(body *) {
			visibility: hidden;
		}
		:global(iframe) {
			visibility: visible;
			position: absolute;
			left: 0;
			top: 0;
			width: 100%;
			height: 100%;
		}
	}
</style>
