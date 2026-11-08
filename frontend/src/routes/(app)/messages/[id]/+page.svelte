<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { apiClient } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type { MessageDetailView } from '$lib/types/api';

	let loading = $state(true);
	let error = $state<string | null>(null);
	let detail = $state<MessageDetailView | null>(null);

	const messageId = $derived($page.params.id);
	const isPlatform = $derived(auth.tenant?.actor_org_type === 'platform');

	async function loadMessage() {
		loading = true;
		error = null;
		try {
			const res = await apiClient.getMessage(messageId);
			detail = res.data;
		} catch (err: any) {
			error = err.message || 'Failed to load message journey';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		loadMessage();
	});

	function getStatusClass(status: string) {
		switch (status) {
			case 'delivered':
				return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300 border border-emerald-200 dark:border-emerald-800';
			case 'submitted':
				return 'bg-sky-50 text-sky-700 dark:bg-sky-950/40 dark:text-sky-300 border border-sky-200 dark:border-sky-800';
			case 'submitting':
			case 'queued':
			case 'accepted':
				return 'bg-indigo-50 text-indigo-700 dark:bg-indigo-950/40 dark:text-indigo-300 border border-indigo-200 dark:border-indigo-800';
			case 'unknown':
				return 'bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300 border border-amber-200 dark:border-amber-800';
			case 'undelivered':
			case 'failed':
			case 'rejected':
			case 'expired':
				return 'bg-rose-50 text-rose-700 dark:bg-rose-950/40 dark:text-rose-300 border border-rose-200 dark:border-rose-800';
			default:
				return 'bg-slate-100 text-slate-700 dark:bg-slate-800 dark:text-slate-300 border border-slate-200 dark:border-slate-700';
		}
	}
</script>

<svelte:head>
	<title>Message Journey — Znero</title>
</svelte:head>

<div class="space-y-6">
	<!-- Top Bar -->
	<div class="flex items-center justify-between">
		<div class="flex items-center gap-3">
			<a
				href="/messages"
				class="inline-flex items-center gap-1 text-xs font-semibold text-slate-600 hover:text-slate-900 dark:text-slate-400 dark:hover:text-white"
			>
				<svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10 19l-7-7m0 0l7-7m-7 7h18" />
				</svg>
				<span>Back to Messages</span>
			</a>
			<span class="text-slate-300 dark:text-slate-700">/</span>
			<h1 class="text-sm font-bold font-mono text-slate-900 dark:text-white">{messageId}</h1>
		</div>

		<button
			type="button"
			onclick={loadMessage}
			class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
		>
			Refresh
		</button>
	</div>

	{#if loading}
		<div class="p-12 text-center text-xs text-slate-500">Loading message journey...</div>
	{:else if error}
		<div class="rounded-xl border border-rose-200 bg-rose-50 p-6 text-center text-xs text-rose-800 dark:border-rose-900 dark:bg-rose-950/40 dark:text-rose-300">
			{error}
		</div>
	{:else if detail}
		{@const m = detail.message}
		<!-- Main Overview Card -->
		<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between border-b border-slate-200 pb-4 dark:border-slate-800">
				<div>
					<div class="flex items-center gap-3">
						<span class="text-lg font-bold font-mono text-slate-900 dark:text-white">{m.destination}</span>
						<span class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-bold uppercase {getStatusClass(m.status)}">
							{m.status}
						</span>
					</div>
					<p class="mt-1 text-xs text-slate-500">
						Sender: <span class="font-semibold text-slate-700 dark:text-slate-300">{m.sender_id}</span> ({m.sender_id_type})
						• Submitted: <span class="font-mono">{new Date(m.created_at).toLocaleString()}</span>
					</p>
				</div>

				<div class="text-right">
					<div class="text-lg font-bold font-mono text-slate-900 dark:text-white">
						${m.sell_price} {m.sell_currency}
					</div>
					<div class="text-xs text-slate-500">
						{m.total_parts} {m.total_parts === 1 ? 'part' : 'parts'} ({m.encoding}, {m.body_length} chars)
					</div>
				</div>
			</div>

			<!-- Meta Grid -->
			<div class="mt-6 grid grid-cols-2 gap-4 sm:grid-cols-4 text-xs">
				<div class="rounded-lg bg-slate-50 p-3 dark:bg-slate-800/40">
					<span class="block text-[11px] font-semibold uppercase text-slate-400">Client Reference</span>
					<span class="mt-1 font-mono font-medium text-slate-900 dark:text-white">{m.client_reference || 'None'}</span>
				</div>
				<div class="rounded-lg bg-slate-50 p-3 dark:bg-slate-800/40">
					<span class="block text-[11px] font-semibold uppercase text-slate-400">Carrier Submit</span>
					<span class="mt-1 font-mono text-slate-900 dark:text-white">
						{m.submitted_at ? new Date(m.submitted_at).toLocaleTimeString() : 'Pending'}
					</span>
				</div>
				<div class="rounded-lg bg-slate-50 p-3 dark:bg-slate-800/40">
					<span class="block text-[11px] font-semibold uppercase text-slate-400">Delivery Confirmation</span>
					<span class="mt-1 font-mono text-slate-900 dark:text-white">
						{m.delivered_at ? new Date(m.delivered_at).toLocaleTimeString() : 'Unconfirmed'}
					</span>
				</div>
				<div class="rounded-lg bg-slate-50 p-3 dark:bg-slate-800/40">
					<span class="block text-[11px] font-semibold uppercase text-slate-400">Attempts Count</span>
					<span class="mt-1 font-mono font-medium text-slate-900 dark:text-white">{m.attempts}</span>
				</div>
			</div>

			<!-- Content Preview -->
			<div class="mt-6 rounded-lg border border-slate-200 bg-slate-50/50 p-4 dark:border-slate-800 dark:bg-slate-800/20">
				<span class="block text-[11px] font-semibold uppercase text-slate-400">Message Content</span>
				<p class="mt-2 font-mono text-xs text-slate-800 dark:text-slate-200 whitespace-pre-wrap">{m.body}</p>
			</div>

			<!-- Platform Financial & Carrier Economics (Platform Only) -->
			{#if isPlatform}
				<div class="mt-6 border-t border-slate-200 pt-6 dark:border-slate-800">
					<h3 class="text-xs font-bold uppercase tracking-wider text-slate-400">Platform Carrier & Economics</h3>
					<div class="mt-3 grid grid-cols-2 gap-4 sm:grid-cols-4 text-xs">
						<div class="rounded-lg border border-slate-200 p-3 dark:border-slate-800">
							<span class="block text-[11px] font-semibold uppercase text-slate-400">Provider Message ID</span>
							<span class="mt-1 font-mono font-bold text-slate-900 dark:text-white">{m.provider_message_id || '—'}</span>
						</div>
						<div class="rounded-lg border border-slate-200 p-3 dark:border-slate-800">
							<span class="block text-[11px] font-semibold uppercase text-slate-400">Buy Cost</span>
							<span class="mt-1 font-mono text-slate-900 dark:text-white">
								{m.actual_buy_cost ? `$${m.actual_buy_cost}` : (m.estimated_buy_cost ? `est $${m.estimated_buy_cost}` : '—')}
							</span>
						</div>
						<div class="rounded-lg border border-slate-200 p-3 dark:border-slate-800">
							<span class="block text-[11px] font-semibold uppercase text-slate-400">Net Margin</span>
							<span class="mt-1 font-mono font-bold {m.margin && parseFloat(m.margin) >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-600'}">
								{m.margin ? `$${m.margin}` : '—'}
							</span>
						</div>
						<div class="rounded-lg border border-slate-200 p-3 dark:border-slate-800">
							<span class="block text-[11px] font-semibold uppercase text-slate-400">Connection ID</span>
							<span class="mt-1 font-mono text-[11px] text-slate-700 dark:text-slate-300 truncate block">
								{m.provider_connection_id || '—'}
							</span>
						</div>
					</div>
				</div>
			{/if}

			<!-- Ledger Hold & Settlement Status -->
			{#if detail.hold_info}
				{@const h = detail.hold_info}
				<div class="mt-6 border-t border-slate-200 pt-6 dark:border-slate-800">
					<h3 class="text-xs font-bold uppercase tracking-wider text-slate-400">Ledger Hold & Settlement</h3>
					<div class="mt-3 grid grid-cols-2 gap-4 sm:grid-cols-4 text-xs">
						<div class="rounded-lg bg-indigo-50/50 border border-indigo-100 p-3 dark:bg-indigo-950/20 dark:border-indigo-900/50">
							<span class="block text-[11px] font-semibold uppercase text-indigo-600 dark:text-indigo-400">Hold ID</span>
							<span class="mt-1 font-mono text-[11px] font-medium text-slate-900 dark:text-white truncate block">{h.hold_id}</span>
						</div>
						<div class="rounded-lg bg-indigo-50/50 border border-indigo-100 p-3 dark:bg-indigo-950/20 dark:border-indigo-900/50">
							<span class="block text-[11px] font-semibold uppercase text-indigo-600 dark:text-indigo-400">Reserved Amount</span>
							<span class="mt-1 font-mono font-bold text-slate-900 dark:text-white">${h.original_hold}</span>
						</div>
						<div class="rounded-lg bg-indigo-50/50 border border-indigo-100 p-3 dark:bg-indigo-950/20 dark:border-indigo-900/50">
							<span class="block text-[11px] font-semibold uppercase text-indigo-600 dark:text-indigo-400">Captured Amount</span>
							<span class="mt-1 font-mono font-bold text-slate-900 dark:text-white">{h.captured ? `$${h.captured}` : '—'}</span>
						</div>
						<div class="rounded-lg bg-indigo-50/50 border border-indigo-100 p-3 dark:bg-indigo-950/20 dark:border-indigo-900/50">
							<span class="block text-[11px] font-semibold uppercase text-indigo-600 dark:text-indigo-400">Hold State</span>
							<span class="mt-1 font-mono uppercase font-bold text-indigo-700 dark:text-indigo-300">{h.status}</span>
						</div>
					</div>
				</div>
			{/if}
		</div>

		<!-- Multi-Part Segments (if applicable) -->
		{#if detail.parts && detail.parts.length > 1}
			<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<h3 class="text-sm font-bold text-slate-900 dark:text-white">Multi-Part Segments ({detail.parts.length} parts)</h3>
				<div class="mt-4 overflow-x-auto">
					<table class="w-full text-left text-xs">
						<thead class="border-b border-slate-200 text-[11px] font-semibold uppercase text-slate-400">
							<tr>
								<th class="py-2">Segment</th>
								<th class="py-2">Status</th>
								<th class="py-2">Provider Message ID</th>
								<th class="py-2 text-right">Updated At</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-slate-100 dark:divide-slate-800">
							{#each detail.parts as part}
								<tr>
									<td class="py-2.5 font-semibold text-slate-800 dark:text-slate-200">
										Part {part.part_number} of {part.total_parts}
									</td>
									<td class="py-2.5">
										<span class="inline-flex items-center rounded px-2 py-0.5 text-[10px] font-semibold uppercase {getStatusClass(part.status)}">
											{part.status}
										</span>
									</td>
									<td class="py-2.5 font-mono text-slate-600 dark:text-slate-400">
										{part.provider_message_id || '—'}
									</td>
									<td class="py-2.5 text-right font-mono text-slate-500">
										{new Date(part.updated_at).toLocaleTimeString()}
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			</div>
		{/if}

		<!-- Journey Timeline -->
		<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<h3 class="text-sm font-bold text-slate-900 dark:text-white">Journey Timeline</h3>

			<div class="mt-6 space-y-6">
				<!-- Events -->
				{#if detail.events && detail.events.length > 0}
					<div class="space-y-4">
						<h4 class="text-xs font-bold uppercase tracking-wider text-slate-400">State Transitions & Events</h4>
						<div class="space-y-3">
							{#each detail.events as evt}
								<div class="flex items-start gap-3 text-xs border-l-2 border-indigo-500 pl-4 py-1">
									<div class="flex-1">
										<div class="flex items-center gap-2">
											<span class="font-bold text-slate-900 dark:text-white capitalize">{evt.event_type.replace(/_/g, ' ')}</span>
											{#if evt.from_status}
												<span class="text-slate-400">({evt.from_status} → {evt.to_status})</span>
											{:else}
												<span class="text-slate-400">(→ {evt.to_status})</span>
											{/if}
											<span class="rounded bg-slate-100 px-1.5 py-0.2 text-[10px] font-medium text-slate-600 dark:bg-slate-800 dark:text-slate-400 uppercase">
												{evt.actor_type}
											</span>
										</div>
										{#if evt.details}
											<pre class="mt-1 font-mono text-[11px] text-slate-600 dark:text-slate-400 bg-slate-50 dark:bg-slate-800/40 p-2 rounded max-w-xl overflow-x-auto">{JSON.stringify(evt.details, null, 2)}</pre>
										{/if}
									</div>
									<span class="font-mono text-slate-400 text-[11px] whitespace-nowrap">
										{new Date(evt.created_at).toLocaleTimeString()}
									</span>
								</div>
							{/each}
						</div>
					</div>
				{/if}

				<!-- Attempts -->
				{#if isPlatform && detail.attempts && detail.attempts.length > 0}
					<div class="border-t border-slate-200 pt-6 dark:border-slate-800 space-y-4">
						<h4 class="text-xs font-bold uppercase tracking-wider text-slate-400">Transmission Attempts</h4>
						<div class="space-y-3">
							{#each detail.attempts as att}
								<div class="flex items-start justify-between rounded-lg border border-slate-200 p-3 text-xs dark:border-slate-800">
									<div class="space-y-1">
										<div class="flex items-center gap-2">
											<span class="font-bold text-slate-900 dark:text-white">Attempt #{att.attempt_number}</span>
											<span class="inline-flex items-center rounded px-2 py-0.5 text-[10px] font-semibold uppercase {getStatusClass(att.status)}">
												{att.status}
											</span>
											{#if att.latency_ms}
												<span class="text-slate-500 font-mono">({att.latency_ms}ms)</span>
											{/if}
										</div>
										<div class="font-mono text-[11px] text-slate-500">
											Connection: {att.connection_id}
											{#if att.provider_message_id}
												• Provider Msg: {att.provider_message_id}
											{/if}
										</div>
										{#if att.error_message}
											<div class="text-rose-600 font-mono text-[11px]">
												Error: {att.error_code} — {att.error_message}
											</div>
										{/if}
									</div>
									<div class="text-right">
										{#if att.buy_cost}
											<div class="font-mono font-medium text-slate-900 dark:text-white">${att.buy_cost} {att.buy_currency}</div>
										{/if}
										<div class="font-mono text-[11px] text-slate-400">{new Date(att.started_at).toLocaleTimeString()}</div>
									</div>
								</div>
							{/each}
						</div>
					</div>
				{/if}

				<!-- DLR Receipts -->
				{#if detail.dlrs && detail.dlrs.length > 0}
					<div class="border-t border-slate-200 pt-6 dark:border-slate-800 space-y-4">
						<h4 class="text-xs font-bold uppercase tracking-wider text-slate-400">Raw Delivery Receipts (DLRs)</h4>
						<div class="space-y-3">
							{#each detail.dlrs as dlr}
								<div class="rounded-lg border border-slate-200 p-3 text-xs dark:border-slate-800 space-y-1">
									<div class="flex items-center justify-between">
										<div class="flex items-center gap-2">
											<span class="font-bold font-mono text-slate-900 dark:text-white">{dlr.status}</span>
											{#if dlr.error_code}
												<span class="font-mono text-rose-600">Err: {dlr.error_code}</span>
											{/if}
										</div>
										<span class="font-mono text-[11px] text-slate-400">{new Date(dlr.received_at).toLocaleTimeString()}</span>
									</div>
									{#if dlr.raw_receipt}
										<div class="font-mono text-[11px] text-slate-600 dark:text-slate-400 bg-slate-50 dark:bg-slate-800/40 p-2 rounded">
											{dlr.raw_receipt}
										</div>
									{/if}
								</div>
							{/each}
						</div>
					</div>
				{/if}
			</div>
		</div>
	{/if}
</div>
