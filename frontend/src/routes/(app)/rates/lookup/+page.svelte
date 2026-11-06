<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import type { Organization, Country, Operator, RateMatch } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	let activeTab = $state<'phone' | 'destination'>('phone');

	// Common references
	let organizations = $state<Organization[]>([]);
	let countries = $state<Country[]>([]);
	let operators = $state<Operator[]>([]);
	let isLoadingRefs = $state(true);
	let errorRefMessage = $state<string | null>(null);

	// Tab 1: Phone lookup
	let phoneInput = $state('+14155552671');
	let phoneCustomerOrgId = $state('');
	let phoneMsgType = $state('default');
	let phoneTimestamp = $state('');
	let isPhoneLoading = $state(false);
	let phoneResult = $state<RateMatch | null>(null);
	let phoneError = $state<string | null>(null);

	// Tab 2: Destination lookup
	let destCustomerOrgId = $state('');
	let destCountryId = $state('');
	let destOperatorId = $state('');
	let destMsgType = $state('default');
	let destTimestamp = $state('');
	let isDestLoading = $state(false);
	let destResult = $state<RateMatch | null>(null);
	let destError = $state<string | null>(null);

	async function loadReferences() {
		isLoadingRefs = true;
		errorRefMessage = null;
		try {
			const [orgsRes, cRes, opRes] = await Promise.all([
				apiClient.listOrganizations({ pageSize: 500 }),
				apiClient.listCountries({ pageSize: 300 }),
				apiClient.listOperators({ pageSize: 500 })
			]);
			organizations = orgsRes.data.items || [];
			countries = cRes.data.items || [];
			operators = opRes.data.items || [];

			if (countries.length > 0) {
				destCountryId = countries[0].id;
			}
		} catch (err) {
			errorRefMessage = err instanceof ApiClientError ? err.message : 'Failed to load reference data.';
		} finally {
			isLoadingRefs = false;
		}
	}

	onMount(() => {
		loadReferences();
	});

	const filteredDestOperators = $derived(
		destCountryId ? operators.filter((o) => o.country_id === destCountryId) : operators
	);

	async function handlePhoneLookup() {
		if (!phoneInput.trim()) {
			phoneError = 'Please enter an E.164 phone number';
			return;
		}
		isPhoneLoading = true;
		phoneError = null;
		phoneResult = null;
		try {
			const res = await apiClient.lookupPhoneNumberRate({
				phone_number: phoneInput.trim(),
				customer_org_id: phoneCustomerOrgId || undefined,
				message_type: phoneMsgType || undefined,
				at: phoneTimestamp ? new Date(phoneTimestamp).toISOString() : undefined
			});
			phoneResult = res.data;
		} catch (err) {
			phoneError = err instanceof ApiClientError ? err.message : 'Failed to lookup phone rate.';
		} finally {
			isPhoneLoading = false;
		}
	}

	async function handleDestLookup() {
		if (!destCountryId) {
			destError = 'Country is required';
			return;
		}
		isDestLoading = true;
		destError = null;
		destResult = null;
		try {
			const res = await apiClient.lookupRate({
				customer_org_id: destCustomerOrgId || undefined,
				country_id: destCountryId,
				operator_id: destOperatorId || undefined,
				message_type: destMsgType || undefined,
				at: destTimestamp ? new Date(destTimestamp).toISOString() : undefined
			});
			destResult = res.data;
		} catch (err) {
			destError = err instanceof ApiClientError ? err.message : 'Failed to lookup destination rate.';
		} finally {
			isDestLoading = false;
		}
	}
</script>

<div class="space-y-6">
	<!-- Top Bar -->
	<div>
		<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Rate Calculator & Lookup</h1>
		<p class="text-sm text-slate-500 dark:text-slate-400">
			Test real-time rating engine resolutions for E.164 phone numbers or country/operator pairs.
		</p>
	</div>

	{#if errorRefMessage}
		<ErrorState message={errorRefMessage} retry={loadReferences} />
	{:else if isLoadingRefs}
		<Loading message="Initializing rating calculator..." />
	{:else}
		<!-- Tabs -->
		<div class="border-b border-slate-200 dark:border-slate-800">
			<nav class="-mb-px flex space-x-6 text-xs font-semibold">
				<button
					type="button"
					onclick={() => { activeTab = 'phone'; }}
					class="pb-3 border-b-2 transition {activeTab === 'phone'
						? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400'
						: 'border-transparent text-slate-500 hover:border-slate-300 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-300'}"
				>
					Phone Number (E.164) Lookup
				</button>
				<button
					type="button"
					onclick={() => { activeTab = 'destination'; }}
					class="pb-3 border-b-2 transition {activeTab === 'destination'
						? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400'
						: 'border-transparent text-slate-500 hover:border-slate-300 hover:text-slate-700 dark:text-slate-400 dark:hover:text-slate-300'}"
				>
					Country & Operator Lookup
				</button>
			</nav>
		</div>

		<!-- Tab 1: Phone Number Lookup -->
		{#if activeTab === 'phone'}
			<div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
				<!-- Input Form -->
				<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
					<h2 class="text-sm font-bold text-slate-900 dark:text-white">Lookup Parameters</h2>
					<p class="mt-1 text-xs text-slate-500">
						Enter any international phone number to resolve prefix mapping and compute effective cost per SMS.
					</p>

					{#if phoneError}
						<div class="mt-4 rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300">
							{phoneError}
						</div>
					{/if}

					<div class="mt-4 space-y-4">
						<div>
							<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="p-num">
								Phone Number (E.164 format) *
							</label>
							<input
								id="p-num"
								type="text"
								bind:value={phoneInput}
								placeholder="+14155552671"
								class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs font-mono text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
							/>
							<div class="mt-1 text-[11px] text-slate-400">
								Examples: +14155552671 (US), +447911123456 (UK), +8801712345678 (BD)
							</div>
						</div>

						<div>
							<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="p-org">
								Customer Organization (Optional)
							</label>
							<select
								id="p-org"
								bind:value={phoneCustomerOrgId}
								class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
							>
								<option value="">Current Organization (Self)</option>
								{#each organizations as org}
									<option value={org.id}>
										{org.name} ({org.type})
									</option>
								{/each}
							</select>
						</div>

						<div class="grid grid-cols-2 gap-4">
							<div>
								<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="p-msgtype">
									Message Type
								</label>
								<select
									id="p-msgtype"
									bind:value={phoneMsgType}
									class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
								>
									<option value="default">Default</option>
									<option value="promotional">Promotional</option>
									<option value="transactional">Transactional</option>
									<option value="otp">OTP</option>
								</select>
							</div>
							<div>
								<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="p-time">
									Simulate Timestamp (Optional)
								</label>
								<input
									id="p-time"
									type="datetime-local"
									bind:value={phoneTimestamp}
									class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
								/>
							</div>
						</div>

						<button
							type="button"
							onclick={handlePhoneLookup}
							disabled={isPhoneLoading}
							class="w-full rounded-lg bg-indigo-600 px-4 py-2.5 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50"
						>
							{isPhoneLoading ? 'Resolving Rate...' : 'Calculate SMS Rate'}
						</button>
					</div>
				</div>

				<!-- Result Card -->
				<div>
					{#if isPhoneLoading}
						<div class="flex h-64 items-center justify-center rounded-2xl border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
							<Loading message="Evaluating number prefix and matching rate table..." />
						</div>
					{:else if phoneResult}
						<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900 space-y-5">
							<div class="flex items-center justify-between border-b border-slate-100 pb-4 dark:border-slate-800">
								<div>
									<span class="text-xs font-semibold text-slate-500">Calculated Rate</span>
									{#if phoneResult.found}
										<div class="mt-1 flex items-baseline gap-2">
											<span class="font-mono text-3xl font-extrabold text-indigo-600 dark:text-indigo-400">
												{phoneResult.price}
											</span>
											<span class="font-mono text-sm font-bold text-slate-700 dark:text-slate-300">
												{phoneResult.currency} / SMS
											</span>
										</div>
									{:else}
										<div class="mt-1 text-xl font-bold text-rose-600 dark:text-rose-400">
											Unrated (No Matching Rate)
										</div>
									{/if}
								</div>

								{#if phoneResult.found}
									{#if phoneResult.specificity === 'operator'}
										<span class="rounded-full bg-emerald-50 px-2.5 py-1 text-xs font-bold text-emerald-700 dark:bg-emerald-950/60 dark:text-emerald-300">
											Exact Operator Match
										</span>
									{:else}
										<span class="rounded-full bg-amber-50 px-2.5 py-1 text-xs font-bold text-amber-700 dark:bg-amber-950/60 dark:text-amber-300">
											Country Default Fallback
										</span>
									{/if}
								{/if}
							</div>

							<!-- Details -->
							<div class="grid grid-cols-2 gap-4 text-xs">
								<div class="rounded-xl bg-slate-50 p-3 dark:bg-slate-800/40">
									<div class="text-slate-400 font-medium">Normalized E.164</div>
									<div class="mt-0.5 font-mono font-bold text-slate-900 dark:text-white">
										{phoneResult.resolved_e164 || phoneInput}
									</div>
								</div>

								<div class="rounded-xl bg-slate-50 p-3 dark:bg-slate-800/40">
									<div class="text-slate-400 font-medium">Destination Country</div>
									<div class="mt-0.5 font-bold text-slate-900 dark:text-white">
										{phoneResult.resolved_country_name || phoneResult.country_name || '—'}
										{#if phoneResult.resolved_country_iso2 || phoneResult.country_iso2}
											<span class="font-mono font-normal text-slate-400">
												({phoneResult.resolved_country_iso2 || phoneResult.country_iso2})
											</span>
										{/if}
									</div>
								</div>

								<div class="rounded-xl bg-slate-50 p-3 dark:bg-slate-800/40">
									<div class="text-slate-400 font-medium">Destination Operator</div>
									<div class="mt-0.5 font-bold text-slate-900 dark:text-white">
										{phoneResult.resolved_operator_name || phoneResult.operator_name || 'Country Default'}
									</div>
								</div>

								<div class="rounded-xl bg-slate-50 p-3 dark:bg-slate-800/40">
									<div class="text-slate-400 font-medium">Effective Period</div>
									<div class="mt-0.5 text-slate-700 dark:text-slate-300">
										{#if phoneResult.effective_from}
											From: {new Date(phoneResult.effective_from).toLocaleDateString()}
											{#if phoneResult.effective_to}
												&bull; To: {new Date(phoneResult.effective_to).toLocaleDateString()}
											{:else}
												&bull; Open
											{/if}
										{:else}
											—
										{/if}
									</div>
								</div>
							</div>

							{#if !phoneResult.found}
								<div class="rounded-xl border border-rose-200 bg-rose-50 p-3 text-xs text-rose-700 dark:border-rose-900/60 dark:bg-rose-950/40 dark:text-rose-300">
									<strong>Rating Engine Result:</strong> No rate covers this number under the selected rate card. SMS traffic dispatched to this destination would fail rating validation or require fallback routing.
								</div>
							{/if}
						</div>
					{:else}
						<div class="flex h-64 flex-col items-center justify-center rounded-2xl border border-dashed border-slate-200 bg-slate-50/50 p-6 text-center text-xs text-slate-400 dark:border-slate-800 dark:bg-slate-900/40">
							<svg class="h-8 w-8 text-slate-300 dark:text-slate-600 mb-2" fill="none" viewBox="0 0 24 24" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 7h6m0 10v-3m-3 3h.01M9 17h.01M9 14h.01M12 14h.01M15 11h.01M12 11h.01M9 11h.01M7 21h10a2 2 0 002-2V5a2 2 0 00-2-2H7a2 2 0 00-2 2v14a2 2 0 002 2z" />
							</svg>
							<span>Run a phone number lookup to view real-time rate resolution and pricing.</span>
						</div>
					{/if}
				</div>
			</div>
		{:else if activeTab === 'destination'}
			<!-- Tab 2: Country & Operator Lookup -->
			<div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
				<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
					<h2 class="text-sm font-bold text-slate-900 dark:text-white">Destination Parameters</h2>
					<p class="mt-1 text-xs text-slate-500">
						Select a destination country and optional mobile operator to test direct rate resolution.
					</p>

					{#if destError}
						<div class="mt-4 rounded-lg bg-rose-50 p-3 text-xs text-rose-700 dark:bg-rose-950/50 dark:text-rose-300">
							{destError}
						</div>
					{/if}

					<div class="mt-4 space-y-4">
						<div>
							<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="d-org">
								Customer Organization (Optional)
							</label>
							<select
								id="d-org"
								bind:value={destCustomerOrgId}
								class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
							>
								<option value="">Current Organization (Self)</option>
								{#each organizations as org}
									<option value={org.id}>
										{org.name} ({org.type})
									</option>
								{/each}
							</select>
						</div>

						<div>
							<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="d-country">
								Country *
							</label>
							<select
								id="d-country"
								bind:value={destCountryId}
								onchange={() => { destOperatorId = ''; }}
								class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
							>
								{#each countries as c}
									<option value={c.id}>{c.name} ({c.iso2} / +{c.calling_code})</option>
								{/each}
							</select>
						</div>

						<div>
							<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="d-op">
								Operator (Optional)
							</label>
							<select
								id="d-op"
								bind:value={destOperatorId}
								class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
							>
								<option value="">Country Default (All Operators)</option>
								{#each filteredDestOperators as op}
									<option value={op.id}>{op.name} (MCC:{op.mcc || '—'} MNC:{op.mnc || '—'})</option>
								{/each}
							</select>
						</div>

						<div class="grid grid-cols-2 gap-4">
							<div>
								<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="d-msgtype">
									Message Type
								</label>
								<select
									id="d-msgtype"
									bind:value={destMsgType}
									class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
								>
									<option value="default">Default</option>
									<option value="promotional">Promotional</option>
									<option value="transactional">Transactional</option>
									<option value="otp">OTP</option>
								</select>
							</div>
							<div>
								<label class="block text-xs font-semibold text-slate-700 dark:text-slate-300" for="d-time">
									Simulate Timestamp
								</label>
								<input
									id="d-time"
									type="datetime-local"
									bind:value={destTimestamp}
									class="mt-1 w-full rounded-lg border border-slate-300 bg-transparent px-3 py-2 text-xs text-slate-900 focus:border-indigo-500 focus:outline-none dark:border-slate-700 dark:text-white"
								/>
							</div>
						</div>

						<button
							type="button"
							onclick={handleDestLookup}
							disabled={isDestLoading}
							class="w-full rounded-lg bg-indigo-600 px-4 py-2.5 text-xs font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50"
						>
							{isDestLoading ? 'Resolving Rate...' : 'Query Destination Rate'}
						</button>
					</div>
				</div>

				<!-- Result Card -->
				<div>
					{#if isDestLoading}
						<div class="flex h-64 items-center justify-center rounded-2xl border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
							<Loading message="Resolving rate match..." />
						</div>
					{:else if destResult}
						<div class="rounded-2xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900 space-y-5">
							<div class="flex items-center justify-between border-b border-slate-100 pb-4 dark:border-slate-800">
								<div>
									<span class="text-xs font-semibold text-slate-500">Resolved Rate</span>
									{#if destResult.found}
										<div class="mt-1 flex items-baseline gap-2">
											<span class="font-mono text-3xl font-extrabold text-indigo-600 dark:text-indigo-400">
												{destResult.price}
											</span>
											<span class="font-mono text-sm font-bold text-slate-700 dark:text-slate-300">
												{destResult.currency} / SMS
											</span>
										</div>
									{:else}
										<div class="mt-1 text-xl font-bold text-rose-600 dark:text-rose-400">
											Not Found
										</div>
									{/if}
								</div>

								{#if destResult.found}
									{#if destResult.specificity === 'operator'}
										<span class="rounded-full bg-emerald-50 px-2.5 py-1 text-xs font-bold text-emerald-700 dark:bg-emerald-950/60 dark:text-emerald-300">
											Operator Specific Rate
										</span>
									{:else}
										<span class="rounded-full bg-amber-50 px-2.5 py-1 text-xs font-bold text-amber-700 dark:bg-amber-950/60 dark:text-amber-300">
											Country Default Fallback
										</span>
									{/if}
								{/if}
							</div>

							<div class="grid grid-cols-2 gap-4 text-xs">
								<div class="rounded-xl bg-slate-50 p-3 dark:bg-slate-800/40">
									<div class="text-slate-400 font-medium">Destination Country</div>
									<div class="mt-0.5 font-bold text-slate-900 dark:text-white">
										{destResult.country_name || '—'} ({destResult.country_iso2 || '—'})
									</div>
								</div>

								<div class="rounded-xl bg-slate-50 p-3 dark:bg-slate-800/40">
									<div class="text-slate-400 font-medium">Operator</div>
									<div class="mt-0.5 font-bold text-slate-900 dark:text-white">
										{destResult.operator_name || 'Country Default'}
									</div>
								</div>
							</div>
						</div>
					{:else}
						<div class="flex h-64 flex-col items-center justify-center rounded-2xl border border-dashed border-slate-200 bg-slate-50/50 p-6 text-center text-xs text-slate-400 dark:border-slate-800 dark:bg-slate-900/40">
							<svg class="h-8 w-8 text-slate-300 dark:text-slate-600 mb-2" fill="none" viewBox="0 0 24 24" stroke="currentColor">
								<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 7h6m0 10v-3m-3 3h.01M9 17h.01M9 14h.01M12 14h.01M15 11h.01M12 11h.01M9 11h.01M7 21h10a2 2 0 002-2V5a2 2 0 00-2-2H7a2 2 0 00-2 2v14a2 2 0 002 2z" />
							</svg>
							<span>Select destination and submit query to inspect resolution hierarchy.</span>
						</div>
					{/if}
				</div>
			</div>
		{/if}
	{/if}
</div>
