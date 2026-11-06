<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { apiClient } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import { formatMoney, isNegativeAmount } from '$lib/formatMoney';
	import type { Wallet, LedgerTransaction, Hold, StatementItem } from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';

	const walletId = $derived($page.params.id);

	let wallet = $state<Wallet | null>(null);
	let loading = $state(true);
	let errorMessage = $state('');
	let activeTab = $state<'overview' | 'ledger' | 'holds' | 'statements'>('overview');

	// Ledger transactions state
	let transactions = $state<LedgerTransaction[]>([]);
	let txTotal = $state(0);
	let txPage = $state(1);
	let txLoading = $state(false);

	// Holds state
	let holds = $state<Hold[]>([]);
	let holdsLoading = $state(false);

	// Statements state
	let statements = $state<StatementItem[]>([]);
	let stmtTotal = $state(0);
	let stmtPage = $state(1);
	let stmtStartDate = $state('');
	let stmtEndDate = $state('');
	let stmtLoading = $state(false);

	// Modals state
	let showTopupModal = $state(false);
	let topupAmount = $state('');
	let topupReason = $state('');
	let topupRef = $state('');
	let topupSubmitting = $state(false);
	let topupMessage = $state('');

	let showAdjustModal = $state(false);
	let adjustAmount = $state('');
	let adjustDirection = $state<'credit' | 'debit'>('credit');
	let adjustReason = $state('');
	let adjustRef = $state('');
	let adjustSubmitting = $state(false);
	let adjustMessage = $state('');

	let showFreezeModal = $state(false);
	let freezeReason = $state('');
	let freezeSubmitting = $state(false);

	let showUnfreezeModal = $state(false);
	let unfreezeReason = $state('');
	let unfreezeSubmitting = $state(false);

	const canTopup = $derived(auth.hasPermission('wallet.topup.create'));
	const canAdjust = $derived(auth.hasPermission('wallet.adjustment.create'));
	const canFreeze = $derived(auth.hasPermission('wallet.freeze'));
	const hasOverride = $derived(auth.hasPermission('wallet.override'));
	const isStaff = $derived(auth.tenant?.actor_org_type === 'platform');

	async function loadWallet() {
		loading = true;
		errorMessage = '';
		try {
			const res = await apiClient.getWallet(walletId);
			wallet = res.data;
		} catch (err: any) {
			errorMessage = err.message || 'Failed to load wallet details';
		} finally {
			loading = false;
		}
	}

	async function loadTransactions() {
		txLoading = true;
		try {
			const res = await apiClient.listLedgerTransactions({
				wallet_id: walletId,
				page: txPage,
				page_size: 20
			});
			transactions = res.data.items || [];
			txTotal = res.data.total;
		} catch (err) {
			console.error('Failed to load transactions:', err);
		} finally {
			txLoading = false;
		}
	}

	async function loadHolds() {
		holdsLoading = true;
		try {
			const res = await apiClient.listWalletHolds(walletId);
			holds = res.data || [];
		} catch (err) {
			console.error('Failed to load holds:', err);
		} finally {
			holdsLoading = false;
		}
	}

	async function loadStatements() {
		stmtLoading = true;
		try {
			const res = await apiClient.getWalletStatement(walletId, {
				start_date: stmtStartDate || undefined,
				end_date: stmtEndDate || undefined,
				page: stmtPage,
				page_size: 25
			});
			statements = res.data.items || [];
			stmtTotal = res.data.total;
		} catch (err) {
			console.error('Failed to load statements:', err);
		} finally {
			stmtLoading = false;
		}
	}

	async function handleExportStatement(format: 'csv' | 'json') {
		try {
			const blob = await apiClient.exportWalletStatement(walletId, format, {
				start_date: stmtStartDate || undefined,
				end_date: stmtEndDate || undefined
			});
			const url = window.URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = `statement-${walletId.slice(0, 8)}.${format}`;
			a.click();
			window.URL.revokeObjectURL(url);
		} catch (err: any) {
			alert('Export failed: ' + (err.message || 'Unknown error'));
		}
	}

	async function handleTopupSubmit(e: Event) {
		e.preventDefault();
		if (!topupAmount || !topupReason.trim()) return;
		topupSubmitting = true;
		topupMessage = '';
		try {
			const res = await apiClient.topupWallet(walletId, {
				amount: topupAmount,
				reason: topupReason.trim(),
				reference: topupRef.trim() || undefined
			});
			if (res.data.pending_approval) {
				topupMessage = 'Top-up request submitted for Dual-Control Maker-Checker approval.';
			} else {
				topupMessage = 'Top-up successfully applied to ledger balance.';
			}
			await loadWallet();
			if (activeTab === 'ledger') loadTransactions();
			setTimeout(() => {
				showTopupModal = false;
				topupAmount = '';
				topupReason = '';
				topupRef = '';
				topupMessage = '';
			}, 1500);
		} catch (err: any) {
			topupMessage = 'Error: ' + (err.message || 'Failed to submit topup');
		} finally {
			topupSubmitting = false;
		}
	}

	async function handleAdjustSubmit(e: Event) {
		e.preventDefault();
		if (!adjustAmount || !adjustReason.trim()) return;
		adjustSubmitting = true;
		adjustMessage = '';
		try {
			const res = await apiClient.adjustWallet(walletId, {
				amount: adjustAmount,
				direction: adjustDirection,
				reason: adjustReason.trim(),
				reference: adjustRef.trim() || undefined
			});
			if (res.data.pending_approval) {
				adjustMessage = 'Adjustment request submitted for Dual-Control Maker-Checker approval.';
			} else {
				adjustMessage = 'Adjustment successfully posted to ledger.';
			}
			await loadWallet();
			if (activeTab === 'ledger') loadTransactions();
			setTimeout(() => {
				showAdjustModal = false;
				adjustAmount = '';
				adjustReason = '';
				adjustRef = '';
				adjustMessage = '';
			}, 1500);
		} catch (err: any) {
			adjustMessage = 'Error: ' + (err.message || 'Failed to submit adjustment');
		} finally {
			adjustSubmitting = false;
		}
	}

	async function handleFreezeSubmit() {
		if (!freezeReason.trim()) return;
		freezeSubmitting = true;
		try {
			await apiClient.freezeWallet(walletId, freezeReason.trim());
			showFreezeModal = false;
			freezeReason = '';
			await loadWallet();
		} catch (err: any) {
			alert('Freeze failed: ' + (err.message || 'Unknown error'));
		} finally {
			freezeSubmitting = false;
		}
	}

	async function handleUnfreezeSubmit() {
		if (!unfreezeReason.trim()) return;
		unfreezeSubmitting = true;
		try {
			const res = await apiClient.unfreezeWallet(walletId, unfreezeReason.trim());
			showUnfreezeModal = false;
			unfreezeReason = '';
			if (res.data.pending_approval) {
				alert('Unfreeze request submitted for Maker-Checker approval.');
			} else {
				alert('Wallet successfully unfrozen.');
			}
			await loadWallet();
		} catch (err: any) {
			alert('Unfreeze failed: ' + (err.message || 'Unknown error'));
		} finally {
			unfreezeSubmitting = false;
		}
	}

	$effect(() => {
		if (activeTab === 'ledger') {
			loadTransactions();
		} else if (activeTab === 'holds') {
			loadHolds();
		} else if (activeTab === 'statements') {
			loadStatements();
		}
	});

	onMount(() => {
		loadWallet();
	});
</script>

<div class="space-y-6">
	<!-- Breadcrumbs & Back -->
	<div class="flex items-center gap-2 text-xs text-slate-500">
		<a href="/wallets" class="hover:text-indigo-600">Wallets</a>
		<span>/</span>
		<span class="font-mono text-slate-900 dark:text-white">{walletId.slice(0, 8)}...</span>
	</div>

	{#if loading}
		<Loading message="Loading wallet..." />
	{:else if errorMessage || !wallet}
		<ErrorState message={errorMessage || 'Wallet not found'} retry={loadWallet} />
	{:else}
		<!-- Header Card with Balances & Actions -->
		<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
			<div class="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
				<div>
					<div class="flex items-center gap-3">
						<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
							{wallet.organization_name || 'Organization ' + wallet.organization_id.slice(0, 8)}
						</h1>
						<span class="rounded px-2.5 py-0.5 text-xs font-semibold uppercase {wallet.status === 'active' ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300' : wallet.status === 'frozen' ? 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300' : 'bg-rose-100 text-rose-800 dark:bg-rose-950 dark:text-rose-300'}">
							{wallet.status}
						</span>
						<span class="rounded bg-indigo-50 px-2 py-0.5 text-xs font-semibold text-indigo-700 dark:bg-indigo-950 dark:text-indigo-300 capitalize">
							{wallet.account_type}
						</span>
					</div>
					<div class="mt-1 text-xs text-slate-500 font-mono">
						Wallet ID: {wallet.id} &bull; Org ID: {wallet.organization_id}
					</div>
				</div>

				<!-- Actions for Staff / Maker -->
				{#if isStaff}
					<div class="flex flex-wrap items-center gap-2">
						{#if canTopup && wallet.status === 'active'}
							<button
								onclick={() => (showTopupModal = true)}
								class="rounded-lg bg-emerald-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-emerald-700"
							>
								+ Top-up
							</button>
						{/if}

						{#if canAdjust && wallet.status !== 'closed'}
							<button
								onclick={() => (showAdjustModal = true)}
								class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white shadow-sm hover:bg-indigo-700"
							>
								&plusmn; Adjustment
							</button>
						{/if}

						{#if canFreeze}
							{#if wallet.status === 'active'}
								<button
									onclick={() => (showFreezeModal = true)}
									class="rounded-lg border border-amber-300 bg-amber-50 px-4 py-2 text-xs font-semibold text-amber-800 hover:bg-amber-100 dark:border-amber-700 dark:bg-amber-950/50 dark:text-amber-300"
								>
									Freeze Wallet
								</button>
							{:else if wallet.status === 'frozen'}
								<button
									onclick={() => (showUnfreezeModal = true)}
									class="rounded-lg border border-emerald-300 bg-emerald-50 px-4 py-2 text-xs font-semibold text-emerald-800 hover:bg-emerald-100 dark:border-emerald-700 dark:bg-emerald-950/50 dark:text-emerald-300"
								>
									Unfreeze Wallet
								</button>
							{/if}
						{/if}
					</div>
				{/if}
			</div>

			<!-- Three Key Financial Metrics Cards -->
			<div class="mt-6 grid grid-cols-1 gap-4 sm:grid-cols-3">
				<!-- 1. Ledger Balance -->
				<div class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-slate-800 dark:bg-slate-800/40">
					<div class="text-[11px] font-semibold uppercase tracking-wider text-slate-500">Ledger Balance</div>
					<div class="mt-2 text-2xl font-bold font-mono {isNegativeAmount(wallet.cached_balance) ? 'text-rose-600' : 'text-slate-900 dark:text-white'}">
						{formatMoney(wallet.cached_balance, wallet.currency)}
					</div>
					<div class="mt-1 text-[11px] text-slate-400">Sum of all posted entries in ledger</div>
				</div>

				<!-- 2. Active Holds -->
				<div class="rounded-lg border border-slate-200 bg-slate-50 p-4 dark:border-slate-800 dark:bg-slate-800/40">
					<div class="text-[11px] font-semibold uppercase tracking-wider text-slate-500">Active Holds</div>
					<div class="mt-2 text-2xl font-bold font-mono text-amber-600 dark:text-amber-400">
						{formatMoney(wallet.active_holds, wallet.currency)}
					</div>
					<div class="mt-1 text-[11px] text-slate-400">Reserved for in-flight transactions</div>
				</div>

				<!-- 3. Available Funds -->
				<div class="rounded-lg border border-indigo-100 bg-indigo-50/60 p-4 dark:border-indigo-900 dark:bg-indigo-950/30">
					<div class="text-[11px] font-semibold uppercase tracking-wider text-indigo-600 dark:text-indigo-400">
						Available Funds
					</div>
					<div class="mt-2 text-2xl font-bold font-mono {isNegativeAmount(wallet.available_balance) ? 'text-rose-600 dark:text-rose-400' : 'text-emerald-600 dark:text-emerald-400'}">
						{formatMoney(wallet.available_balance, wallet.currency)}
					</div>
					<div class="mt-1 text-[11px] text-slate-500">
						{#if wallet.account_type === 'postpaid'}
							Ledger - Holds + Credit Limit ({formatMoney(wallet.credit_limit, wallet.currency)})
						{:else}
							Ledger Balance - Active Holds
						{/if}
					</div>
				</div>
			</div>
		</div>

		<!-- Tabs Header -->
		<div class="border-b border-slate-200 dark:border-slate-800">
			<nav class="flex space-x-6">
				<button
					onclick={() => (activeTab = 'overview')}
					class="border-b-2 py-3 text-xs font-semibold transition {activeTab === 'overview' ? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400' : 'border-transparent text-slate-500 hover:text-slate-700'}"
				>
					Overview
				</button>
				<button
					onclick={() => (activeTab = 'ledger')}
					class="border-b-2 py-3 text-xs font-semibold transition {activeTab === 'ledger' ? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400' : 'border-transparent text-slate-500 hover:text-slate-700'}"
				>
					Ledger Transactions
				</button>
				{#if isStaff}
					<button
						onclick={() => (activeTab = 'holds')}
						class="border-b-2 py-3 text-xs font-semibold transition {activeTab === 'holds' ? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400' : 'border-transparent text-slate-500 hover:text-slate-700'}"
					>
						Holds
					</button>
				{/if}
				<button
					onclick={() => (activeTab = 'statements')}
					class="border-b-2 py-3 text-xs font-semibold transition {activeTab === 'statements' ? 'border-indigo-600 text-indigo-600 dark:border-indigo-400 dark:text-indigo-400' : 'border-transparent text-slate-500 hover:text-slate-700'}"
				>
					Statements
				</button>
			</nav>
		</div>

		<!-- Tab Content: Overview -->
		{#if activeTab === 'overview'}
			<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<h3 class="text-sm font-bold text-slate-900 dark:text-white">Commercial Configuration</h3>
				<dl class="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2 text-xs">
					<div>
						<dt class="font-medium text-slate-500">Account Type</dt>
						<dd class="mt-1 font-semibold text-slate-900 dark:text-white capitalize">{wallet.account_type}</dd>
					</div>
					<div>
						<dt class="font-medium text-slate-500">Currency</dt>
						<dd class="mt-1 font-semibold text-slate-900 dark:text-white">{wallet.currency}</dd>
					</div>
					<div>
						<dt class="font-medium text-slate-500">Credit Limit</dt>
						<dd class="mt-1 font-mono font-semibold text-slate-900 dark:text-white">
							{formatMoney(wallet.credit_limit, wallet.currency)}
						</dd>
					</div>
					<div>
						<dt class="font-medium text-slate-500">Low Balance Alert Threshold</dt>
						<dd class="mt-1 font-mono font-semibold text-slate-900 dark:text-white">
							{formatMoney(wallet.low_balance_threshold, wallet.currency)}
						</dd>
					</div>
					<div>
						<dt class="font-medium text-slate-500">Record Version</dt>
						<dd class="mt-1 font-mono text-slate-600 dark:text-slate-300">v{wallet.version}</dd>
					</div>
					<div>
						<dt class="font-medium text-slate-500">Created At</dt>
						<dd class="mt-1 text-slate-600 dark:text-slate-300">{new Date(wallet.created_at).toLocaleString()}</dd>
					</div>
				</dl>
			</div>
		{/if}

		<!-- Tab Content: Ledger Transactions -->
		{#if activeTab === 'ledger'}
			<div class="space-y-4">
				{#if txLoading}
					<Loading message="Loading ledger transactions..." />
				{:else if transactions.length === 0}
					<Empty description="No transactions recorded in the ledger yet." />
				{:else}
					<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
						<div class="overflow-x-auto">
							<table class="w-full text-left text-xs">
								<thead class="border-b border-slate-200 bg-slate-50 font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-400">
									<tr>
										<th class="px-4 py-3">Timestamp</th>
										<th class="px-4 py-3">Type</th>
										<th class="px-4 py-3">Description</th>
										<th class="px-4 py-3">Reason / Ref</th>
										<th class="px-4 py-3 text-right">Debit</th>
										<th class="px-4 py-3 text-right">Credit</th>
									</tr>
								</thead>
								<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
									{#each transactions as tx (tx.id)}
										<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40">
											<td class="px-4 py-3 text-slate-500 whitespace-nowrap">
												{new Date(tx.created_at).toLocaleString()}
											</td>
											<td class="px-4 py-3">
												<span class="inline-block rounded px-2 py-0.5 text-[10px] font-semibold uppercase {tx.type === 'topup' ? 'bg-emerald-50 text-emerald-700' : tx.type === 'charge' ? 'bg-purple-50 text-purple-700' : tx.type === 'reversal' ? 'bg-rose-50 text-rose-700' : 'bg-slate-100 text-slate-700'}">
													{tx.type}
												</span>
											</td>
											<td class="px-4 py-3 font-medium text-slate-900 dark:text-white">
												{tx.description}
											</td>
											<td class="px-4 py-3 text-slate-500">
												{#if tx.reason}<div>{tx.reason}</div>{/if}
												{#if tx.external_ref}<div class="text-[10px] font-mono text-slate-400">Ref: {tx.external_ref}</div>{/if}
											</td>
											<td class="px-4 py-3 text-right font-mono text-rose-600">
												{#each tx.entries || [] as e}
													{#if e.direction === 'debit' && e.account_code.startsWith('WALLET_')}
														{formatMoney(e.amount, e.currency)}
													{/if}
												{/each}
											</td>
											<td class="px-4 py-3 text-right font-mono text-emerald-600 font-semibold">
												{#each tx.entries || [] as e}
													{#if e.direction === 'credit' && e.account_code.startsWith('WALLET_')}
														{formatMoney(e.amount, e.currency)}
													{/if}
												{/each}
											</td>
										</tr>
									{/each}
								</tbody>
							</table>
						</div>
					</div>
				{/if}
			</div>
		{/if}

		<!-- Tab Content: Holds (Staff only) -->
		{#if activeTab === 'holds'}
			<div class="space-y-4">
				{#if holdsLoading}
					<Loading message="Loading active holds..." />
				{:else if holds.length === 0}
					<Empty description="No active or pending holds on this wallet." />
				{:else}
					<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
						<table class="w-full text-left text-xs">
							<thead class="border-b border-slate-200 bg-slate-50 font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-400">
								<tr>
									<th class="px-4 py-3">Hold ID</th>
									<th class="px-4 py-3 text-right">Hold Amount</th>
									<th class="px-4 py-3 text-right">Captured</th>
									<th class="px-4 py-3">Status</th>
									<th class="px-4 py-3">Expires At</th>
									<th class="px-4 py-3">Created At</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
								{#each holds as h (h.id)}
									<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40">
										<td class="px-4 py-3 font-mono text-slate-600 dark:text-slate-300">
											{h.id.slice(0, 8)}...
										</td>
										<td class="px-4 py-3 text-right font-mono font-bold text-amber-600">
											{formatMoney(h.amount, h.currency)}
										</td>
										<td class="px-4 py-3 text-right font-mono text-slate-600 dark:text-slate-300">
											{formatMoney(h.captured_amount, h.currency)}
										</td>
										<td class="px-4 py-3">
											<span class="inline-block rounded px-2 py-0.5 text-[10px] font-semibold uppercase {h.status === 'active' ? 'bg-amber-100 text-amber-800' : h.status === 'captured' ? 'bg-emerald-100 text-emerald-800' : 'bg-slate-100 text-slate-700'}">
												{h.status}
											</span>
										</td>
										<td class="px-4 py-3 text-slate-500">
											{new Date(h.expires_at).toLocaleString()}
										</td>
										<td class="px-4 py-3 text-slate-500">
											{new Date(h.created_at).toLocaleString()}
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/if}
			</div>
		{/if}

		<!-- Tab Content: Statements -->
		{#if activeTab === 'statements'}
			<div class="space-y-4">
				<div class="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900">
					<div class="flex flex-wrap items-center gap-3">
						<div>
							<label for="stmt-start" class="block text-[10px] font-medium text-slate-400">Start Date</label>
							<input
								id="stmt-start"
								type="date"
								bind:value={stmtStartDate}
								class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							/>
						</div>
						<div>
							<label for="stmt-end" class="block text-[10px] font-medium text-slate-400">End Date</label>
							<input
								id="stmt-end"
								type="date"
								bind:value={stmtEndDate}
								class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
							/>
						</div>
						<button
							onclick={() => { stmtPage = 1; loadStatements(); }}
							class="mt-4 rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-semibold text-white shadow-sm hover:bg-indigo-700"
						>
							Filter
						</button>
					</div>

					<div class="flex items-center gap-2">
						<button
							onclick={() => handleExportStatement('csv')}
							class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
						>
							Export CSV
						</button>
						<button
							onclick={() => handleExportStatement('json')}
							class="rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-200"
						>
							Export JSON
						</button>
					</div>
				</div>

				{#if stmtLoading}
					<Loading message="Loading account statement..." />
				{:else if statements.length === 0}
					<Empty description="No statement records found for the selected date range." />
				{:else}
					<div class="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
						<table class="w-full text-left text-xs">
							<thead class="border-b border-slate-200 bg-slate-50 font-semibold uppercase tracking-wider text-slate-500 dark:border-slate-800 dark:bg-slate-800/50 dark:text-slate-400">
								<tr>
									<th class="px-4 py-3">Date</th>
									<th class="px-4 py-3">Type</th>
									<th class="px-4 py-3">Description / Reason</th>
									<th class="px-4 py-3 text-right">Debit</th>
									<th class="px-4 py-3 text-right">Credit</th>
									<th class="px-4 py-3 text-right">Running Balance</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
								{#each statements as s}
									<tr class="hover:bg-slate-50 dark:hover:bg-slate-800/40">
										<td class="px-4 py-3 text-slate-500 whitespace-nowrap">
											{new Date(s.created_at).toLocaleString()}
										</td>
										<td class="px-4 py-3 capitalize font-semibold text-slate-700 dark:text-slate-300">
											{s.type}
										</td>
										<td class="px-4 py-3">
											<div class="font-medium text-slate-900 dark:text-white">{s.description}</div>
											{#if s.reason}<div class="text-[11px] text-slate-500">{s.reason}</div>{/if}
										</td>
										<td class="px-4 py-3 text-right font-mono text-rose-600">
											{s.direction === 'debit' ? formatMoney(s.amount, wallet.currency) : '-'}
										</td>
										<td class="px-4 py-3 text-right font-mono text-emerald-600 font-semibold">
											{s.direction === 'credit' ? formatMoney(s.amount, wallet.currency) : '-'}
										</td>
										<td class="px-4 py-3 text-right font-mono font-bold {isNegativeAmount(s.running_balance) ? 'text-rose-600' : 'text-slate-900 dark:text-white'}">
											{formatMoney(s.running_balance, wallet.currency)}
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/if}
			</div>
		{/if}
	{/if}
</div>

<!-- Modal: Top-up -->
{#if showTopupModal}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
		<div class="w-full max-w-md rounded-xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Manual Wallet Top-up</h2>
			<p class="mt-1 text-xs text-slate-500">
				{#if hasOverride}
					Holding override permission: top-up will immediately apply to balance.
				{:else}
					Non-override: this action generates a Maker-Checker pending change request.
				{/if}
			</p>

			<form onsubmit={handleTopupSubmit} class="mt-4 space-y-4 text-xs">
				<div>
					<label for="topup-amount" class="block font-medium text-slate-700 dark:text-slate-300">Amount ({wallet?.currency}) *</label>
					<input
						id="topup-amount"
						type="number"
						step="0.0001"
						min="0.0001"
						required
						bind:value={topupAmount}
						placeholder="e.g. 500.00"
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>
				<div>
					<label for="topup-reason" class="block font-medium text-slate-700 dark:text-slate-300">Mandatory Reason *</label>
					<textarea
						id="topup-reason"
						required
						rows="2"
						bind:value={topupReason}
						placeholder="e.g. Client bank wire deposit received and cleared"
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					></textarea>
				</div>
				<div>
					<label for="topup-ref" class="block font-medium text-slate-700 dark:text-slate-300">External Bank Reference (Optional)</label>
					<input
						id="topup-ref"
						type="text"
						bind:value={topupRef}
						placeholder="e.g. WIRE-88192-HSBC"
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				{#if topupMessage}
					<div class="rounded p-2 text-xs font-semibold {topupMessage.startsWith('Error') ? 'bg-rose-50 text-rose-700' : 'bg-emerald-50 text-emerald-700'}">
						{topupMessage}
					</div>
				{/if}

				<div class="flex justify-end gap-2 pt-2">
					<button
						type="button"
						onclick={() => (showTopupModal = false)}
						class="rounded-lg border border-slate-300 px-4 py-2 font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={topupSubmitting}
						class="rounded-lg bg-emerald-600 px-4 py-2 font-semibold text-white hover:bg-emerald-700 disabled:opacity-50"
					>
						{topupSubmitting ? 'Posting...' : 'Confirm Top-up'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}

<!-- Modal: Adjustment -->
{#if showAdjustModal}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
		<div class="w-full max-w-md rounded-xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Manual Balance Adjustment</h2>
			<form onsubmit={handleAdjustSubmit} class="mt-4 space-y-4 text-xs">
				<div>
					<span class="block font-medium text-slate-700 dark:text-slate-300">Adjustment Direction *</span>
					<div class="mt-1 flex gap-4">
						<label class="flex items-center gap-1.5 cursor-pointer">
							<input type="radio" bind:group={adjustDirection} value="credit" />
							<span>Credit (Add Funds)</span>
						</label>
						<label class="flex items-center gap-1.5 cursor-pointer">
							<input type="radio" bind:group={adjustDirection} value="debit" />
							<span>Debit (Deduct Funds)</span>
						</label>
					</div>
				</div>
				<div>
					<label for="adjust-amount" class="block font-medium text-slate-700 dark:text-slate-300">Amount ({wallet?.currency}) *</label>
					<input
						id="adjust-amount"
						type="number"
						step="0.0001"
						min="0.0001"
						required
						bind:value={adjustAmount}
						placeholder="e.g. 50.00"
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>
				<div>
					<label for="adjust-reason" class="block font-medium text-slate-700 dark:text-slate-300">Mandatory Reason *</label>
					<textarea
						id="adjust-reason"
						required
						rows="2"
						bind:value={adjustReason}
						placeholder="e.g. Compensation for platform delivery degradation"
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					></textarea>
				</div>
				<div>
					<label for="adjust-ref" class="block font-medium text-slate-700 dark:text-slate-300">Reference / Ticket (Optional)</label>
					<input
						id="adjust-ref"
						type="text"
						bind:value={adjustRef}
						placeholder="e.g. TICKET-9921"
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					/>
				</div>

				{#if adjustMessage}
					<div class="rounded p-2 text-xs font-semibold {adjustMessage.startsWith('Error') ? 'bg-rose-50 text-rose-700' : 'bg-emerald-50 text-emerald-700'}">
						{adjustMessage}
					</div>
				{/if}

				<div class="flex justify-end gap-2 pt-2">
					<button
						type="button"
						onclick={() => (showAdjustModal = false)}
						class="rounded-lg border border-slate-300 px-4 py-2 font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
					>
						Cancel
					</button>
					<button
						type="submit"
						disabled={adjustSubmitting}
						class="rounded-lg bg-indigo-600 px-4 py-2 font-semibold text-white hover:bg-indigo-700 disabled:opacity-50"
					>
						{adjustSubmitting ? 'Posting...' : 'Confirm Adjustment'}
					</button>
				</div>
			</form>
		</div>
	</div>
{/if}

<!-- Modal: Freeze Wallet -->
{#if showFreezeModal}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
		<div class="w-full max-w-md rounded-xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Freeze Wallet</h2>
			<p class="mt-1 text-xs text-slate-500">
				Freezing blocks all debit operations and usage charges immediately.
			</p>
			<div class="mt-4 space-y-4 text-xs">
				<div>
					<label for="freeze-reason" class="block font-medium text-slate-700 dark:text-slate-300">Mandatory Reason *</label>
					<textarea
						id="freeze-reason"
						required
						rows="2"
						bind:value={freezeReason}
						placeholder="e.g. Compliance investigation or unpaid overdue invoice"
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					></textarea>
				</div>
				<div class="flex justify-end gap-2 pt-2">
					<button
						type="button"
						onclick={() => (showFreezeModal = false)}
						class="rounded-lg border border-slate-300 px-4 py-2 font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
					>
						Cancel
					</button>
					<button
						type="button"
						disabled={freezeSubmitting || !freezeReason.trim()}
						onclick={handleFreezeSubmit}
						class="rounded-lg bg-amber-600 px-4 py-2 font-semibold text-white hover:bg-amber-700 disabled:opacity-50"
					>
						{freezeSubmitting ? 'Freezing...' : 'Confirm Freeze'}
					</button>
				</div>
			</div>
		</div>
	</div>
{/if}

<!-- Modal: Unfreeze Wallet -->
{#if showUnfreezeModal}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
		<div class="w-full max-w-md rounded-xl border border-slate-200 bg-white p-6 shadow-xl dark:border-slate-800 dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Unfreeze Wallet</h2>
			<p class="mt-1 text-xs text-slate-500">
				Unfreezing restores live debit capabilities. Requires Maker-Checker approval unless holding override.
			</p>
			<div class="mt-4 space-y-4 text-xs">
				<div>
					<label for="unfreeze-reason" class="block font-medium text-slate-700 dark:text-slate-300">Mandatory Reason *</label>
					<textarea
						id="unfreeze-reason"
						required
						rows="2"
						bind:value={unfreezeReason}
						placeholder="e.g. Investigation completed and debt settled"
						class="mt-1 w-full rounded-lg border border-slate-300 p-2 text-slate-900 dark:border-slate-700 dark:bg-slate-800 dark:text-white"
					></textarea>
				</div>
				<div class="flex justify-end gap-2 pt-2">
					<button
						type="button"
						onclick={() => (showUnfreezeModal = false)}
						class="rounded-lg border border-slate-300 px-4 py-2 font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
					>
						Cancel
					</button>
					<button
						type="button"
						disabled={unfreezeSubmitting || !unfreezeReason.trim()}
						onclick={handleUnfreezeSubmit}
						class="rounded-lg bg-emerald-600 px-4 py-2 font-semibold text-white hover:bg-emerald-700 disabled:opacity-50"
					>
						{unfreezeSubmitting ? 'Submitting...' : 'Confirm Unfreeze'}
					</button>
				</div>
			</div>
		</div>
	</div>
{/if}
