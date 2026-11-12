<script lang="ts">
	import { onMount } from 'svelte';
	import { auth } from '$lib/auth.svelte';
import { apiClient } from '$lib/api/client';
	import type { Organization, SimulateResult, SnapshotStatus } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let customers = $state<Organization[]>([]);
	let snapshotStatus = $state<SnapshotStatus | null>(null);

	// Simulator Inputs
	let customerOrgId = $state('');
	let destinationNumber = $state('+12025550100');
	let senderId = $state('VERIFY');
	let senderIdType = $state('alphanumeric');
	let messageType = $state('transactional');

	let simulating = $state(false);
	let result = $state<SimulateResult | null>(null);
	let errorMessage = $state('');

	async function loadInitData() {
		try {
			const [orgRes, snapRes] = await Promise.all([
				auth.hasPermission('org.organization.view')
					? apiClient.listOrganizations({ pageSize: 100, type: 'customer' })
					: Promise.resolve({ data: { items: [] as Organization[] } }),
				apiClient.getSnapshotStatus()
			]);
			customers = orgRes.data.items || [];
			if (customers.length > 0) {
				customerOrgId = customers[0].id;
			}
			snapshotStatus = snapRes.data;
		} catch (err: any) {
			errorMessage = err.message || 'Failed to initialize simulator';
		}
	}

	async function handleSimulate() {
		const selectEl = typeof document !== 'undefined' ? (document.getElementById('sim-cust') as HTMLSelectElement | null) : null;
		const effectiveCustId = customerOrgId || selectEl?.value || (customers[0]?.id ?? '');
		if (!effectiveCustId || !destinationNumber.trim()) {
			errorMessage = 'Customer organization and destination number are required.';
			return;
		}
		customerOrgId = effectiveCustId;
		simulating = true;
		errorMessage = '';
		result = null;
		try {
			const res = await apiClient.simulateRouting({
				customer_org_id: customerOrgId,
				destination_e164: destinationNumber.trim(),
				destination_number: destinationNumber.trim(),
				sender_id: senderId.trim() || undefined,
				sender_id_type: senderIdType,
				message_type: messageType
			});
			result = res.data;
		} catch (err: any) {
			errorMessage = err.message || 'Simulation failed';
		} finally {
			simulating = false;
		}
	}

	function getExclusionBadge(code?: string) {
		switch (code) {
			case 'kill_switched':
				return 'bg-rose-100 text-rose-800 dark:bg-rose-950/60 dark:text-rose-300';
			case 'no_coverage':
				return 'bg-amber-100 text-amber-800 dark:bg-amber-950/60 dark:text-amber-300';
			case 'entry_disabled':
			case 'connection_inactive':
				return 'bg-slate-100 text-slate-800 dark:bg-slate-800 dark:text-slate-300';
			default:
				return 'bg-purple-100 text-purple-800 dark:bg-purple-950/60 dark:text-purple-300';
		}
	}

	onMount(() => {
		loadInitData();
	});
</script>

<div class="space-y-6">
	<!-- Page Header -->
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Route Simulator</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Dry-run route resolution decisions with explainability traces, exclusion reasons, and cost economics.
			</p>
		</div>

		{#if snapshotStatus}
			<div class="flex items-center gap-2 rounded-xl border border-slate-200 bg-white px-3 py-1.5 text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-900">
				<span class="inline-flex h-2 w-2 rounded-full bg-emerald-500"></span>
				<span>In-Memory Snapshot:</span>
				<span class="font-mono font-bold text-slate-900 dark:text-white">{snapshotStatus.plans_count} plans</span>
				<span>•</span>
				<span class="font-mono">{snapshotStatus.age_ms ?? Math.round(((snapshotStatus as any).age_seconds ?? 0) * 1000)}ms old</span>
			</div>
		{/if}
	</div>

	<!-- Form Card -->
	<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900 space-y-4">
		<h2 class="text-sm font-bold text-slate-900 dark:text-white uppercase tracking-wider">Simulation Parameters</h2>

		{#if errorMessage}
			<div class="rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300">
				{errorMessage}
			</div>
		{/if}

		<div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
			<div>
				<label for="sim-cust" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Customer Organization *</label>
				<select
					id="sim-cust"
					bind:value={customerOrgId}
					class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
				>
					{#each customers as cust}
						<option value={cust.id}>{cust.name}</option>
					{/each}
				</select>
			</div>

			<div>
				<label for="sim-dest" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Destination Number (E.164) *</label>
				<input
					id="sim-dest"
					type="text"
					bind:value={destinationNumber}
					placeholder="+12025550100"
					class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 font-mono text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
				/>
			</div>

			<div>
				<label for="sim-sender-type" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Sender ID Type</label>
				<select
					id="sim-sender-type"
					bind:value={senderIdType}
					class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
				>
					<option value="alphanumeric">Alphanumeric (e.g. BRAND)</option>
					<option value="numeric">Numeric (e.g. Shortcode or Longcode)</option>
					<option value="shortcode">Shortcode</option>
				</select>
			</div>

			<div>
				<label for="sim-sender-id" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Sender ID Value</label>
				<input
					id="sim-sender-id"
					type="text"
					bind:value={senderId}
					placeholder="e.g. AUTH"
					class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
				/>
			</div>

			<div>
				<label for="sim-msg-type" class="block text-xs font-semibold text-slate-700 dark:text-slate-300 mb-1">Message Type</label>
				<select
					id="sim-msg-type"
					bind:value={messageType}
					class="w-full rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-900 focus:border-indigo-500 focus:bg-white focus:outline-none dark:border-slate-800 dark:bg-slate-950 dark:text-white"
				>
					<option value="transactional">Transactional</option>
					<option value="otp">OTP / Verification</option>
					<option value="promotional">Promotional</option>
				</select>
			</div>

			<div class="flex items-end">
				<button
					type="button"
					disabled={simulating}
					onclick={handleSimulate}
					class="w-full rounded-lg bg-indigo-600 px-4 py-2 text-sm font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50 transition"
				>
					{simulating ? 'Simulating...' : 'Run Simulation'}
				</button>
			</div>
		</div>
	</div>

	<!-- Simulation Results -->
	{#if simulating}
		<Loading message="Executing in-memory route decision engine..." />
	{:else if result}
		<div class="space-y-6">
			<!-- Decision Summary Header -->
			<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900 space-y-4">
				<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
					<div>
						<div class="text-xs font-semibold text-slate-500 uppercase tracking-wider">Top Selected Route</div>
						{#if result.resolve.candidate}
							<div class="mt-1 flex items-center gap-3">
								<h2 class="text-2xl font-bold text-slate-900 dark:text-white">
									{result.resolve.candidate.connection_name || result.resolve.candidate.connection_id.slice(0, 8)}
								</h2>
								<span class="rounded bg-indigo-50 px-2 py-0.5 text-xs font-semibold text-indigo-700 dark:bg-indigo-950/50 dark:text-indigo-300">
									Priority #{result.resolve.candidate.priority}
								</span>
							</div>
							<div class="mt-1 text-xs text-slate-500">
								Provider: <span class="font-semibold text-slate-700 dark:text-slate-300">{result.resolve.candidate.provider_name || 'Provider'}</span>
							</div>
						{:else}
							<div class="mt-1 text-lg font-bold text-rose-600 dark:text-rose-400">
								No eligible route found (Delivery Impossible)
							</div>
						{/if}
					</div>

					<div class="flex flex-wrap gap-4 text-xs sm:text-right">
						<div>
							<div class="text-slate-500">Plan Used</div>
							<div class="font-mono font-bold text-slate-900 dark:text-white">{result.resolve.plan_used || 'None'}</div>
						</div>
						<div>
							<div class="text-slate-500">Strategy</div>
							<div class="font-semibold uppercase text-indigo-600 dark:text-indigo-400">{result.resolve.strategy_used || 'Default'}</div>
						</div>
						<div>
							<div class="text-slate-500">Candidates</div>
							<div class="font-bold text-slate-900 dark:text-white">{(result.resolve.candidates ?? []).length} eligible</div>
						</div>
						<div>
							<div class="text-slate-500">Exclusions</div>
							<div class="font-bold text-rose-600 dark:text-rose-400">{(result.resolve.excluded ?? []).length} disqualified</div>
						</div>
					</div>
				</div>

				<!-- Rules Applied Banner -->
				{#if result.resolve.rules_applied && result.resolve.rules_applied.length > 0}
					<div class="flex items-center gap-2 border-t border-slate-100 pt-3 text-xs dark:border-slate-800">
						<span class="font-semibold text-amber-700 dark:text-amber-400">Rules Applied:</span>
						{#each result.resolve.rules_applied as ruleName}
							<span class="rounded bg-amber-50 px-2 py-0.5 text-xs font-semibold text-amber-800 dark:bg-amber-950/50 dark:text-amber-300">
								{ruleName}
							</span>
						{/each}
					</div>
				{/if}
			</div>

			<!-- Candidates Table -->
			{#if (result.resolve.candidates ?? []).length > 0}
				<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900 space-y-4">
					<h3 class="text-sm font-bold text-slate-900 dark:text-white">Ordered Candidates (Primary + Failovers)</h3>
					<div class="overflow-x-auto">
						<table class="w-full text-left text-sm text-slate-600 dark:text-slate-400">
							<thead class="border-b border-slate-200 bg-slate-50 text-xs font-semibold uppercase text-slate-500 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-400">
								<tr>
									<th class="px-4 py-2">Rank</th>
									<th class="px-4 py-2">Connection</th>
									<th class="px-4 py-2">Provider</th>
									<th class="px-4 py-2">Priority</th>
									<th class="px-4 py-2">Weight</th>
									<th class="px-4 py-2">Buy Rate</th>
									<th class="px-4 py-2">Sell Rate</th>
									<th class="px-4 py-2">Margin</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
								{#each result.resolve.candidates ?? [] as c, idx}
									<tr class={idx === 0 ? 'bg-indigo-50/40 dark:bg-indigo-950/20 font-semibold' : ''}>
										<td class="px-4 py-2.5 font-mono text-xs">
											#{idx + 1}
											{#if idx === 0}
												<span class="ml-1 rounded bg-indigo-600 px-1.5 py-0.2 text-[10px] text-white">CHOSEN</span>
											{/if}
										</td>
										<td class="px-4 py-2.5 text-xs text-slate-900 dark:text-white">
											{c.connection_name || c.connection_id.slice(0, 8)}
										</td>
										<td class="px-4 py-2.5 text-xs text-slate-500">
											{c.provider_name || 'Provider'}
										</td>
										<td class="px-4 py-2.5 font-mono text-xs">
											#{c.priority}
										</td>
										<td class="px-4 py-2.5 font-mono text-xs">
											{c.weight}%
										</td>
										<td class="px-4 py-2.5 font-mono text-xs">
											{c.estimated_buy_rate ? `$${c.estimated_buy_rate}` : 'N/A'}
										</td>
										<td class="px-4 py-2.5 font-mono text-xs">
											{c.estimated_sell_rate ? `$${c.estimated_sell_rate}` : 'N/A'}
										</td>
										<td class="px-4 py-2.5 font-mono text-xs text-emerald-600 dark:text-emerald-400">
											{c.margin_percent ? `${c.margin_percent}%` : 'N/A'}
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				</div>
			{/if}

			<!-- Excluded Connections Table -->
			{#if (result.resolve.excluded ?? []).length > 0}
				<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900 space-y-4">
					<h3 class="text-sm font-bold text-slate-900 dark:text-white">Disqualified Connections ({(result.resolve.excluded ?? []).length})</h3>
					<div class="overflow-x-auto">
						<table class="w-full text-left text-sm text-slate-600 dark:text-slate-400">
							<thead class="border-b border-slate-200 bg-slate-50 text-xs font-semibold uppercase text-slate-500 dark:border-slate-800 dark:bg-slate-950 dark:text-slate-400">
								<tr>
									<th class="px-4 py-2">Connection</th>
									<th class="px-4 py-2">Exclusion Reason</th>
									<th class="px-4 py-2">Detail</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
								{#each result.resolve.excluded ?? [] as ex}
									<tr>
										<td class="px-4 py-2.5 text-xs font-mono text-slate-900 dark:text-white">
											{ex.connection_name || (ex.connection_id ? ex.connection_id.slice(0, 8) + '...' : 'Plan Level')}
										</td>
										<td class="px-4 py-2.5">
											<span class="inline-flex rounded-full px-2 py-0.5 text-xs font-semibold uppercase {getExclusionBadge(ex.reason_code || ex.reason)}">
												{ex.reason_code || ex.reason}
											</span>
										</td>
										<td class="px-4 py-2.5 text-xs text-slate-500">
											{ex.detail || 'Did not meet hard eligibility criteria'}
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				</div>
			{/if}

			<!-- Explainability Trace Steps -->
			{#if result.trace && result.trace.length > 0}
				<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-xs dark:border-slate-800 dark:bg-slate-900 space-y-4">
					<h3 class="text-sm font-bold text-slate-900 dark:text-white">Decision Trace Pipeline</h3>
					<div class="space-y-2 rounded-xl bg-slate-50 p-4 font-mono text-xs dark:bg-slate-950">
						{#each result.trace as step, idx}
							<div class="flex items-start gap-2 text-slate-700 dark:text-slate-300">
								<span class="text-slate-400">[{idx + 1}]</span>
								<span>{step}</span>
							</div>
						{/each}
					</div>
				</div>
			{/if}
		</div>
	{/if}
</div>
