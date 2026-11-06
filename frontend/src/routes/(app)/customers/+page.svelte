<script lang="ts">
	import { onMount } from 'svelte';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type {
		CustomerAPIKey,
		CustomerProfile,
		CustomerProfileHistory,
		CustomerSMPPAccount,
		Organization,
		SenderID
	} from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import Empty from '$lib/components/Empty.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	let profiles = $state<CustomerProfile[]>([]);
	let customerOrgs = $state<Organization[]>([]);
	let selectedOrgId = $state('');
	let selectedProfile = $state<CustomerProfile | null>(null);
	let apiKeys = $state<CustomerAPIKey[]>([]);
	let smppAccounts = $state<CustomerSMPPAccount[]>([]);
	let senderIds = $state<SenderID[]>([]);
	let profileHistory = $state<CustomerProfileHistory[]>([]);

	let activeTab = $state<'profile' | 'apikeys' | 'smpp' | 'senderids' | 'history'>('profile');
	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	// Create Profile Modal
	let isProfileModalOpen = $state(false);
	let profOrgId = $state('');
	let profAccountType = $state<'prepaid' | 'postpaid'>('prepaid');
	let profCurrency = $state('USD');
	let profTimezone = $state('UTC');
	let profCreditLimit = $state(1000);
	let profMaxTps = $state(25);
	let profDestPolicy = $state<'allow_all' | 'allow_listed' | 'block_listed'>('allow_all');
	let profAllowedCountries = $state('US,GB');
	let profStatus = $state<'draft' | 'active' | 'suspended' | 'closed'>('active');
	let profTechContact = $state('');
	let profBillingContact = $state('');
	let profNotes = $state('');
	let profReason = $state('');
	let profError = $state<string | null>(null);

	// Create API Key Modal
	let isKeyModalOpen = $state(false);
	let keyName = $state('');
	let keyScopes = $state('sms.send,dlr.read');
	let keyIpAllowlist = $state('');
	let keyReason = $state('');
	let keyError = $state<string | null>(null);

	// One-time Secret Reveal Modal
	let revealedSecretTitle = $state('');
	let revealedSecretValue = $state('');
	let revealedSecretWarning = $state('');

	// Create SMPP Account Modal
	let isSmppModalOpen = $state(false);
	let smppSystemId = $state('');
	let smppMaxBinds = $state(2);
	let smppMaxTps = $state(10);
	let smppIpAllowlist = $state('');
	let smppReason = $state('');
	let smppError = $state<string | null>(null);

	const canEditProfile = $derived(auth.hasPermission('customer.profile.create') || auth.hasPermission('customer.profile.edit'));
	const canCreateKey = $derived(auth.hasPermission('customer.apikey.create'));
	const canCreateSmpp = $derived(auth.hasPermission('customer.smppaccount.create'));

	async function loadInitial() {
		isLoading = true;
		errorMessage = null;
		try {
			const profRes = await apiClient.listCustomerProfiles({ pageSize: 100 });
			profiles = profRes.data.items || [];
			try {
				const orgRes = await apiClient.listOrganizations({ type: 'customer', pageSize: 100 });
				customerOrgs = orgRes.data.items || [];
			} catch {
				customerOrgs = [];
			}
			if (profiles.length > 0 && !selectedOrgId) {
				await selectCustomerOrg(profiles[0].organization_id);
			} else if (auth.tenant?.actor_org_type === 'customer' && auth.tenant.actor_org_id) {
				await selectCustomerOrg(auth.tenant.actor_org_id);
			}
		} catch (err) {
			errorMessage = err instanceof ApiClientError ? err.message : 'Failed to load customer profiles.';
		} finally {
			isLoading = false;
		}
	}

	async function selectCustomerOrg(orgId: string) {
		selectedOrgId = orgId;
		selectedProfile = profiles.find((p) => p.organization_id === orgId) || null;
		try {
			const [kRes, sRes, sidRes] = await Promise.all([
				apiClient.listCustomerAPIKeys(orgId),
				apiClient.listCustomerSMPPAccounts(orgId),
				apiClient.listSenderIDs({ organization_id: orgId, pageSize: 50 })
			]);
			apiKeys = kRes.data || [];
			smppAccounts = sRes.data || [];
			senderIds = sidRes.data.items || [];
			if (selectedProfile) {
				const hRes = await apiClient.getCustomerProfileHistory(selectedProfile.id);
				profileHistory = hRes.data || [];
			} else {
				profileHistory = [];
			}
		} catch {
			// ignore partial load errors
		}
	}

	onMount(() => {
		loadInitial();
	});

	async function submitCreateProfile() {
		profError = null;
		try {
			const countries = profAllowedCountries
				.split(',')
				.map((s) => s.trim().toUpperCase())
				.filter(Boolean);
			const res = await apiClient.createCustomerProfile({
				organization_id: profOrgId,
				account_type: profAccountType,
				billing_currency: profCurrency.trim().toUpperCase(),
				timezone: profTimezone.trim(),
				credit_limit: Number(profCreditLimit),
				default_max_tps: Number(profMaxTps),
				destination_policy: profDestPolicy,
				allowed_countries: countries,
				status: profStatus,
				technical_contact: profTechContact.trim(),
				billing_contact: profBillingContact.trim(),
				notes: profNotes.trim(),
				reason: profReason.trim() || 'Created customer commercial profile'
			});
			isProfileModalOpen = false;
			if (res.data.pending_approval) {
				toast('Customer profile submitted to Maker-Checker approval queue', 'info');
			} else {
				toast('Customer commercial profile created', 'success');
			}
			await loadInitial();
			if (profOrgId) await selectCustomerOrg(profOrgId);
		} catch (err) {
			profError = err instanceof ApiClientError ? err.message : 'Failed to create customer profile';
		}
	}

	async function submitCreateAPIKey() {
		keyError = null;
		const targetOrg = selectedOrgId || auth.tenant?.actor_org_id || '';
		try {
			const scopes = keyScopes.split(',').map((s) => s.trim()).filter(Boolean);
			const ips = keyIpAllowlist.split(',').map((s) => s.trim()).filter(Boolean);
			const res = await apiClient.createCustomerAPIKey({
				organization_id: targetOrg,
				label: keyName.trim(),
				name: keyName.trim(),
				scopes,
				ip_allowlist: ips,
				reason: keyReason.trim() || 'Created customer API key'
			});
			isKeyModalOpen = false;
			revealedSecretTitle = `API Key Created: ${res.data.api_key.label || res.data.api_key.name}`;
			revealedSecretValue = res.data.plaintext_key;
			revealedSecretWarning = res.data.warning || 'Copy this API key now. It will never be shown again.';
			await selectCustomerOrg(targetOrg);
		} catch (err) {
			keyError = err instanceof ApiClientError ? err.message : 'Failed to create API key';
		}
	}

	async function handleRevokeKey(id: string) {
		const reason = prompt('Reason for revoking API key:', 'Revoked via UI');
		if (!reason) return;
		try {
			await apiClient.revokeCustomerAPIKey(id, reason);
			toast('API key revoked', 'success');
			await selectCustomerOrg(selectedOrgId);
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Revoke failed', 'error');
		}
	}

	async function handleRotateKey(k: CustomerAPIKey) {
		const reason = prompt('Reason for rotating API key:', 'Scheduled key rotation');
		if (!reason) return;
		try {
			const res = await apiClient.rotateCustomerAPIKey(k.id, `${k.label || k.name} (rotated)`, reason);
			revealedSecretTitle = `Rotated API Key: ${res.data.api_key.label || res.data.api_key.name}`;
			revealedSecretValue = res.data.plaintext_key;
			revealedSecretWarning = res.data.warning || 'Copy this API key now. It will never be shown again.';
			await selectCustomerOrg(selectedOrgId);
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Rotation failed', 'error');
		}
	}

	async function submitCreateSMPPAccount() {
		smppError = null;
		const targetOrg = selectedOrgId || auth.tenant?.actor_org_id || '';
		try {
			const ips = smppIpAllowlist.split(',').map((s) => s.trim()).filter(Boolean);
			const res = await apiClient.createCustomerSMPPAccount({
				organization_id: targetOrg,
				system_id: smppSystemId.trim(),
				max_binds: Number(smppMaxBinds),
				max_tps: Number(smppMaxTps),
				ip_allowlist: ips,
				allowed_bind_types: ['transceiver'],
				reason: smppReason.trim() || 'Created customer SMPP account'
			});
			isSmppModalOpen = false;
			revealedSecretTitle = `SMPP Account Created: ${res.data.account.system_id}`;
			revealedSecretValue = res.data.plaintext_password;
			revealedSecretWarning = res.data.warning || 'Copy this SMPP password now. It will never be shown again.';
			await selectCustomerOrg(targetOrg);
		} catch (err) {
			smppError = err instanceof ApiClientError ? err.message : 'Failed to create SMPP account';
		}
	}

	async function handleResetSmppPassword(acc: CustomerSMPPAccount) {
		const reason = prompt(`Reason for resetting password for ${acc.system_id}:`, 'Password reset via UI');
		if (!reason) return;
		try {
			const res = await apiClient.resetCustomerSMPPPassword(acc.id, reason);
			revealedSecretTitle = `SMPP Password Reset: ${acc.system_id}`;
			revealedSecretValue = res.data.plaintext_password;
			revealedSecretWarning = res.data.warning || 'Copy this SMPP password now. It will never be shown again.';
			await selectCustomerOrg(selectedOrgId);
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Password reset failed', 'error');
		}
	}

	function copyRevealedSecret() {
		if (navigator.clipboard && revealedSecretValue) {
			navigator.clipboard.writeText(revealedSecretValue);
			toast('Copied secret to clipboard', 'success');
		}
	}
</script>

<div class="space-y-6">
	<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
		<div>
			<h1 class="text-2xl font-bold tracking-tight text-slate-900 dark:text-white">Customer Commercial Profiles & Credentials</h1>
			<p class="text-sm text-slate-500 dark:text-slate-400">
				Tenant-scoped commercial profiles, one-time HTTP API keys, SMPP bind credentials, and Sender IDs.
			</p>
		</div>

		{#if canEditProfile}
			<button
				type="button"
				onclick={() => { profOrgId = selectedOrgId; profNotes = ''; profReason = ''; profError = null; isProfileModalOpen = true; }}
				class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white hover:bg-indigo-500"
			>
				+ Create Commercial Profile
			</button>
		{/if}
	</div>

	{#if isLoading}
		<div class="flex justify-center py-16">
			<Loading message="Loading customer profiles..." />
		</div>
	{:else if errorMessage}
		<ErrorState message={errorMessage} retry={loadInitial} />
	{:else}
		<div class="grid grid-cols-1 lg:grid-cols-4 gap-6">
			<!-- Left selector: Customer Profiles -->
			<div class="rounded-xl border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-slate-900 space-y-3">
				<h2 class="text-xs font-bold uppercase tracking-wider text-slate-400">Customer Organizations ({profiles.length})</h2>
				{#if profiles.length === 0}
					<p class="text-xs text-slate-500">No commercial profiles created yet.</p>
				{:else}
					<div class="space-y-1.5">
						{#each profiles as p}
							<button
								type="button"
								onclick={() => selectCustomerOrg(p.organization_id)}
								class="w-full rounded-lg p-3 text-left text-xs transition {selectedOrgId === p.organization_id
									? 'bg-indigo-50 border border-indigo-200 text-indigo-900 dark:bg-indigo-950/50 dark:border-indigo-800 dark:text-white'
									: 'hover:bg-slate-50 text-slate-700 dark:text-slate-300 dark:hover:bg-slate-800/50'}"
							>
								<div class="font-bold">{p.organization_name || p.organization_id.slice(0, 8)}</div>
								<div class="mt-1 flex items-center justify-between text-[11px] text-slate-500">
									<span class="uppercase font-mono">{p.account_type} • {p.billing_currency}</span>
									<span class="uppercase font-semibold">{p.status}</span>
								</div>
							</button>
						{/each}
					</div>
				{/if}
			</div>

			<!-- Right detail area -->
			<div class="lg:col-span-3 space-y-4">
				<div class="flex flex-wrap items-center justify-between gap-2 rounded-xl border border-slate-200 bg-white p-3 shadow-sm dark:border-slate-800 dark:bg-slate-900">
					<div class="flex flex-wrap gap-1">
						<button
							type="button"
							onclick={() => activeTab = 'profile'}
							class="rounded-lg px-3 py-1.5 text-xs font-semibold {activeTab === 'profile' ? 'bg-indigo-600 text-white' : 'text-slate-600 dark:text-slate-300'}"
						>
							Commercial Profile
						</button>
						<button
							type="button"
							onclick={() => activeTab = 'apikeys'}
							class="rounded-lg px-3 py-1.5 text-xs font-semibold {activeTab === 'apikeys' ? 'bg-indigo-600 text-white' : 'text-slate-600 dark:text-slate-300'}"
						>
							API Keys ({apiKeys.length})
						</button>
						<button
							type="button"
							onclick={() => activeTab = 'smpp'}
							class="rounded-lg px-3 py-1.5 text-xs font-semibold {activeTab === 'smpp' ? 'bg-indigo-600 text-white' : 'text-slate-600 dark:text-slate-300'}"
						>
							SMPP Accounts ({smppAccounts.length})
						</button>
						<button
							type="button"
							onclick={() => activeTab = 'senderids'}
							class="rounded-lg px-3 py-1.5 text-xs font-semibold {activeTab === 'senderids' ? 'bg-indigo-600 text-white' : 'text-slate-600 dark:text-slate-300'}"
						>
							Sender IDs ({senderIds.length})
						</button>
						<button
							type="button"
							onclick={() => activeTab = 'history'}
							class="rounded-lg px-3 py-1.5 text-xs font-semibold {activeTab === 'history' ? 'bg-indigo-600 text-white' : 'text-slate-600 dark:text-slate-300'}"
						>
							History ({profileHistory.length})
						</button>
					</div>
				</div>

				{#if activeTab === 'profile'}
					{#if !selectedProfile}
						<Empty title="No profile selected" description="Select a customer organization from the left or create a commercial profile." />
					{:else}
						<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900 space-y-4 text-xs">
							<div class="flex items-center justify-between border-b border-slate-100 pb-3 dark:border-slate-800">
								<div>
									<h3 class="text-base font-bold text-slate-900 dark:text-white">{selectedProfile.organization_name}</h3>
									<p class="text-slate-500">Organization ID: <span class="font-mono">{selectedProfile.organization_id}</span> • v{selectedProfile.version}</p>
								</div>
								<span class="rounded-full bg-emerald-100 px-3 py-1 font-mono text-xs font-bold uppercase text-emerald-800">
									{selectedProfile.status}
								</span>
							</div>

							<div class="grid grid-cols-2 sm:grid-cols-3 gap-4">
								<div>
									<div class="text-slate-400">Account Type</div>
									<div class="font-mono font-bold uppercase mt-0.5">{selectedProfile.account_type}</div>
								</div>
								<div>
									<div class="text-slate-400">Billing Currency</div>
									<div class="font-mono font-bold mt-0.5">{selectedProfile.billing_currency}</div>
								</div>
								<div>
									<div class="text-slate-400">Credit Limit</div>
									<div class="font-mono font-bold mt-0.5">{selectedProfile.credit_limit} {selectedProfile.billing_currency}</div>
								</div>
								<div>
									<div class="text-slate-400">Default Max TPS</div>
									<div class="font-mono font-bold mt-0.5">{selectedProfile.default_max_tps} TPS</div>
								</div>
								<div>
									<div class="text-slate-400">Destination Policy</div>
									<div class="font-mono font-bold mt-0.5">{selectedProfile.destination_policy}</div>
								</div>
								<div>
									<div class="text-slate-400">Allowed Countries</div>
									<div class="font-mono mt-0.5">{selectedProfile.allowed_countries?.join(', ') || 'All'}</div>
								</div>
							</div>
						</div>
					{/if}
				{:else if activeTab === 'apikeys'}
					<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900 space-y-4">
						<div class="flex items-center justify-between">
							<div>
								<h3 class="text-sm font-bold text-slate-900 dark:text-white">HTTP API Keys</h3>
								<p class="text-xs text-slate-500">Plaintext API keys are shown once at creation/rotation and stored SHA-256 hashed.</p>
							</div>
							{#if canCreateKey && selectedOrgId}
								<button
									type="button"
									onclick={() => { keyName = ''; keyReason = ''; keyError = null; isKeyModalOpen = true; }}
									class="rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-semibold text-white hover:bg-indigo-500"
								>
									+ Create API Key
								</button>
							{/if}
						</div>

						<table class="min-w-full divide-y divide-slate-200 text-left text-xs dark:divide-slate-800">
							<thead class="bg-slate-50 text-slate-500 uppercase font-semibold dark:bg-slate-800/50">
								<tr>
									<th class="px-3 py-2">Name</th>
									<th class="px-3 py-2">Key Prefix</th>
									<th class="px-3 py-2">Scopes</th>
									<th class="px-3 py-2">Status</th>
									<th class="px-3 py-2 text-right">Actions</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
								{#each apiKeys as k}
									<tr>
										<td class="px-3 py-2 font-semibold">{k.label || k.name}</td>
										<td data-testid="apikey-prefix-badge" class="px-3 py-2 font-mono">{k.key_prefix}...</td>
										<td class="px-3 py-2 font-mono">{k.scopes?.join(', ')}</td>
										<td class="px-3 py-2 uppercase font-mono font-semibold">{k.status}</td>
										<td class="px-3 py-2 text-right space-x-2">
											{#if k.status === 'active'}
												<button type="button" onclick={() => handleRotateKey(k)} class="text-indigo-600 font-semibold hover:underline">Rotate</button>
												<button type="button" onclick={() => handleRevokeKey(k.id)} class="text-rose-600 font-semibold hover:underline">Revoke</button>
											{/if}
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{:else if activeTab === 'smpp'}
					<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900 space-y-4">
						<div class="flex items-center justify-between">
							<div>
								<h3 class="text-sm font-bold text-slate-900 dark:text-white">Customer SMPP Accounts</h3>
								<p class="text-xs text-slate-500">Passwords are generated once and stored Argon2id hashed.</p>
							</div>
							{#if canCreateSmpp && selectedOrgId}
								<button
									type="button"
									onclick={() => { smppSystemId = ''; smppReason = ''; smppError = null; isSmppModalOpen = true; }}
									class="rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-semibold text-white hover:bg-indigo-500"
								>
									+ Create SMPP Account
								</button>
							{/if}
						</div>

						<table class="min-w-full divide-y divide-slate-200 text-left text-xs dark:divide-slate-800">
							<thead class="bg-slate-50 text-slate-500 uppercase font-semibold dark:bg-slate-800/50">
								<tr>
									<th class="px-3 py-2">System ID</th>
									<th class="px-3 py-2">Password</th>
									<th class="px-3 py-2">Max Binds / Max TPS</th>
									<th class="px-3 py-2">Status</th>
									<th class="px-3 py-2 text-right">Actions</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
								{#each smppAccounts as acc}
									<tr>
										<td class="px-3 py-2 font-mono font-bold">{acc.system_id}</td>
										<td class="px-3 py-2 font-mono">{acc.password_meta?.masked_display || '••• set'}</td>
										<td class="px-3 py-2 font-mono">{acc.max_binds} binds / {acc.max_tps} TPS</td>
										<td class="px-3 py-2 uppercase font-mono font-semibold">{acc.status}</td>
										<td class="px-3 py-2 text-right">
											<button type="button" onclick={() => handleResetSmppPassword(acc)} class="text-indigo-600 font-semibold hover:underline">Reset Password</button>
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{:else if activeTab === 'senderids'}
					<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900 space-y-3">
						<div class="flex items-center justify-between">
							<h3 class="text-sm font-bold text-slate-900 dark:text-white">Registered Sender IDs</h3>
							<a href="/sender-ids" class="text-xs font-semibold text-indigo-600 hover:underline">Open Full Sender ID Registry →</a>
						</div>
						<table class="min-w-full divide-y divide-slate-200 text-left text-xs dark:divide-slate-800">
							<thead>
								<tr>
									<th class="px-3 py-2">Sender ID</th>
									<th class="px-3 py-2">Type</th>
									<th class="px-3 py-2">Country</th>
									<th class="px-3 py-2">Status</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
								{#each senderIds as sid}
									<tr>
										<td class="px-3 py-2 font-mono font-bold">{sid.sender_id}</td>
										<td class="px-3 py-2 uppercase font-mono">{sid.sender_type}</td>
										<td class="px-3 py-2">{sid.country_iso2 || 'Global'}</td>
										<td class="px-3 py-2 uppercase font-mono font-semibold">{sid.status}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{:else if activeTab === 'history'}
					<div class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
						<table class="min-w-full divide-y divide-slate-200 text-left text-xs dark:divide-slate-800">
							<thead>
								<tr>
									<th class="px-3 py-2">Version</th>
									<th class="px-3 py-2">Changed At</th>
									<th class="px-3 py-2">Reason</th>
								</tr>
							</thead>
							<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
								{#each profileHistory as h}
									<tr>
										<td class="px-3 py-2 font-mono font-bold">v{h.version}</td>
										<td class="px-3 py-2">{new Date(h.changed_at).toLocaleString()}</td>
										<td class="px-3 py-2">{h.reason}</td>
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/if}
			</div>
		</div>
	{/if}
</div>

<!-- One-Time Plaintext Secret Reveal Modal -->
{#if revealedSecretValue}
	<div
		data-testid="one-time-secret-modal"
		class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/60 p-4 backdrop-blur-sm"
	>
		<div class="w-full max-w-lg rounded-2xl bg-white p-6 shadow-2xl dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">{revealedSecretTitle}</h2>
			<div class="mt-3 rounded-lg border border-amber-300 bg-amber-50 p-3 text-xs font-semibold text-amber-900 dark:border-amber-700 dark:bg-amber-950/60 dark:text-amber-200">
				⚠️ {revealedSecretWarning}
			</div>
			<div class="mt-4 flex items-center justify-between gap-2 rounded-lg border border-slate-300 bg-slate-950 p-3 font-mono text-xs text-emerald-400">
				<span data-testid="one-time-secret-value" class="break-all select-all">{revealedSecretValue}</span>
				<button
					type="button"
					onclick={copyRevealedSecret}
					class="shrink-0 rounded bg-indigo-600 px-3 py-1 text-xs font-semibold text-white hover:bg-indigo-500"
				>
					Copy
				</button>
			</div>
			<div class="mt-5 flex justify-end">
				<button
					type="button"
					data-testid="close-one-time-secret-btn"
					onclick={() => { revealedSecretValue = ''; }}
					class="rounded-lg bg-slate-900 px-4 py-2 text-xs font-semibold text-white hover:bg-slate-700"
				>
					I have saved this credential
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Create API Key Modal -->
{#if isKeyModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-md rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Create Customer API Key</h2>
			{#if keyError}
				<div class="mt-2 rounded bg-rose-50 p-2 text-xs text-rose-700">{keyError}</div>
			{/if}
			<div class="mt-4 space-y-3 text-xs">
				<div>
					<label for="ak-name" class="block font-medium text-slate-700 mb-1">Key Name *</label>
					<input id="ak-name" type="text" bind:value={keyName} placeholder="e.g. Production Messaging Key" class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="ak-scopes" class="block font-medium text-slate-700 mb-1">Scopes (comma-separated)</label>
					<input id="ak-scopes" type="text" bind:value={keyScopes} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="ak-reason" class="block font-medium text-slate-700 mb-1">Audit Reason *</label>
					<input id="ak-reason" type="text" bind:value={keyReason} placeholder="Reason for creating key" class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
			</div>
			<div class="mt-5 flex justify-end gap-2">
				<button type="button" onclick={() => isKeyModalOpen = false} class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700">Cancel</button>
				<button
					type="button"
					disabled={!keyName.trim()}
					onclick={submitCreateAPIKey}
					class="rounded-lg bg-indigo-600 px-4 py-1.5 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					Generate API Key
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Create SMPP Account Modal -->
{#if isSmppModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-md rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Create Customer SMPP Account</h2>
			{#if smppError}
				<div class="mt-2 rounded bg-rose-50 p-2 text-xs text-rose-700">{smppError}</div>
			{/if}
			<div class="mt-4 space-y-3 text-xs">
				<div>
					<label for="smpp-sysid" class="block font-medium text-slate-700 mb-1">System ID (3..15 chars) *</label>
					<input id="smpp-sysid" type="text" bind:value={smppSystemId} placeholder="cust_bind_01" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="smpp-tps" class="block font-medium text-slate-700 mb-1">Max TPS</label>
					<input id="smpp-tps" type="number" min="1" max="500" bind:value={smppMaxTps} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="smpp-reason" class="block font-medium text-slate-700 mb-1">Audit Reason *</label>
					<input id="smpp-reason" type="text" bind:value={smppReason} placeholder="Reason for creating SMPP bind" class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
			</div>
			<div class="mt-5 flex justify-end gap-2">
				<button type="button" onclick={() => isSmppModalOpen = false} class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700">Cancel</button>
				<button
					type="button"
					disabled={!smppSystemId.trim()}
					onclick={submitCreateSMPPAccount}
					class="rounded-lg bg-indigo-600 px-4 py-1.5 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					Generate SMPP Account
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Create Commercial Profile Modal -->
{#if isProfileModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm overflow-y-auto">
		<div class="w-full max-w-lg rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900 my-8">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Create Customer Commercial Profile</h2>
			{#if profError}
				<div class="mt-2 rounded bg-rose-50 p-2 text-xs text-rose-700">{profError}</div>
			{/if}
			<div class="mt-4 space-y-3 text-xs">
				<div>
					<label for="cprof-org" class="block font-medium text-slate-700 mb-1">Customer Organization *</label>
					<select id="cprof-org" bind:value={profOrgId} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="">Select Customer Organization</option>
						{#each customerOrgs as o}
							<option value={o.id}>{o.name}</option>
						{/each}
					</select>
				</div>
				<div class="grid grid-cols-2 gap-3">
					<div>
						<label for="cprof-type" class="block font-medium text-slate-700 mb-1">Account Type</label>
						<select id="cprof-type" bind:value={profAccountType} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
							<option value="prepaid">Prepaid</option>
							<option value="postpaid">Postpaid</option>
						</select>
					</div>
					<div>
						<label for="cprof-curr" class="block font-medium text-slate-700 mb-1">Billing Currency</label>
						<input id="cprof-curr" type="text" maxlength="3" bind:value={profCurrency} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono uppercase dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="cprof-credit" class="block font-medium text-slate-700 mb-1">Credit Limit</label>
						<input id="cprof-credit" type="number" bind:value={profCreditLimit} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="cprof-tps" class="block font-medium text-slate-700 mb-1">Default Max TPS</label>
						<input id="cprof-tps" type="number" bind:value={profMaxTps} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
				</div>
				<div>
					<label for="cprof-reason" class="block font-medium text-slate-700 mb-1">Reason *</label>
					<input id="cprof-reason" type="text" bind:value={profReason} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
			</div>
			<div class="mt-5 flex justify-end gap-2">
				<button type="button" onclick={() => isProfileModalOpen = false} class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700">Cancel</button>
				<button
					type="button"
					disabled={!profOrgId}
					onclick={submitCreateProfile}
					class="rounded-lg bg-indigo-600 px-4 py-1.5 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					Create Profile
				</button>
			</div>
		</div>
	</div>
{/if}
