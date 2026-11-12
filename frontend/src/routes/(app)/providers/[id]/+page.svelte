<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { apiClient, ApiClientError } from '$lib/api/client';
	import { auth } from '$lib/auth.svelte';
	import type {
		Country,
		HTTPConnectorConfig,
		HTTPTestSendResult,
		Operator,
		Provider,
		ProviderConnection,
		ProviderConnectionHistory,
		ProviderCoverage,
		ProviderHistory
	} from '$lib/types/api';
	import Loading from '$lib/components/Loading.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import { toast } from '$lib/components/Toast.svelte';

	const providerId = $derived($page.params.id || '');
	let activeTab = $state<'overview' | 'connections' | 'coverage' | 'history'>('connections');

	let providerData = $state<Provider | null>(null);
	let connections = $state<ProviderConnection[]>([]);
	let coverage = $state<ProviderCoverage[]>([]);
	let history = $state<ProviderHistory[]>([]);
	let countries = $state<Country[]>([]);
	let operators = $state<Operator[]>([]);

	let isLoading = $state(true);
	let errorMessage = $state<string | null>(null);

	// Edit Provider state
	let editName = $state('');
	let editLegal = $state('');
	let editTech = $state('');
	let editComm = $state('');
	let editNoc = $state('');
	let editTimezone = $state('UTC');
	let editCurrency = $state('USD');
	let editStatus = $state<'draft' | 'active' | 'suspended' | 'inactive'>('draft');
	let editNotes = $state('');
	let editReason = $state('');
	let isSavingProvider = $state(false);

	// Create Connection Modal
	let isConnModalOpen = $state(false);
	let connName = $state('');
	let connProtocol = $state<'smpp' | 'http'>('smpp');
	let connRoleNotes = $state('');
	let connStatus = $state<'draft' | 'active' | 'disabled'>('draft');
	let connMaxTps = $state(50);
	let connSmppHost = $state('');
	let connSmppPort = $state(2775);
	let connSmppSystemId = $state('');
	let connSmppPassword = $state('');
	let connSmppSystemType = $state('');
	let connSmppBindType = $state('transceiver');
	let connSmppTls = $state(true);
	let connSmppWindowSize = $state(10);
	let connSmppEnquireLink = $state(30);
	let connSmppCharset = $state('GSM7');
	let connIpAllowlist = $state('');
	let connHttpBaseUrl = $state('');
	let connHttpAuthType = $state<'none' | 'basic' | 'bearer' | 'header_token' | 'api_key_header' | 'query_token' | 'api_key_query'>('bearer');
	let connHttpCredentials = $state('');
	let connHttpFormatNotes = $state('');
	let connHttpDlrNotes = $state('');
	let connHttpMethod = $state('POST');
	let connHttpUrlTemplate = $state('');
	let connHttpAuthHeaderName = $state('');
	let connHttpAuthQueryParam = $state('');
	let connHttpHeadersJson = $state('{}');
	let connHttpBodyFormat = $state<'json' | 'form' | 'none'>('json');
	let connHttpBodyTemplate = $state('{"to":"{{destination}}","from":"{{sender}}","text":"{{text}}","message_id":"{{message_id}}","encoding":"{{encoding}}"}');
	let connHttpMsgIdPath = $state('message_id');
	let connHttpErrorCodePath = $state('error');
	let connHttpSuccessMin = $state(200);
	let connHttpSuccessMax = $state(299);
	let connHttpDlrMode = $state<'none' | 'callback' | 'polling'>('none');
	let connHttpDlrSecret = $state('');
	let connHttpCallbackSigAlgo = $state<'hmac_sha256' | 'shared_secret' | 'none'>('hmac_sha256');
	let connHttpCallbackMsgIdPath = $state('message_id');
	let connHttpCallbackStatusPath = $state('status');
	let connHttpPollUrlTemplate = $state('');
	let connHttpPollIntervalSec = $state(30);
	let connHttpPollStatusPath = $state('status');
	let connReason = $state('');
	let connError = $state<string | null>(null);
	let isSubmittingConn = $state(false);

	// Edit HTTP Template Modal
	let editHttpConn = $state<ProviderConnection | null>(null);
	let editHttpError = $state<string | null>(null);
	let isSavingHttpCfg = $state(false);

	// Test Send Modal (HTTP Connector)
	let testSendTargetConn = $state<ProviderConnection | null>(null);
	let testDest = $state('+15550001234');
	let testSenderId = $state('ZNERO');
	let testText = $state('Znero HTTP connector test');
	let testSimMode = $state<'' | 'accept' | 'reject' | 'throttle' | 'server_err'>('accept');
	let testResult = $state<HTTPTestSendResult | null>(null);
	let testError = $state<string | null>(null);
	let isTestingSend = $state(false);

	// Update Secret Modal (write-only)
	let secretTargetConn = $state<ProviderConnection | null>(null);
	let secretKind = $state<'auth' | 'dlr'>('auth');
	let newSecretValue = $state('');
	let secretReason = $state('');
	let secretError = $state<string | null>(null);
	let isUpdatingSecret = $state(false);

	// Status Change Modal
	let statusTargetConn = $state<ProviderConnection | null>(null);
	let nextConnStatus = $state<'draft' | 'active' | 'disabled'>('active');
	let statusReason = $state('');
	let isChangingStatus = $state(false);

	// Connection History Modal
	let histTargetConn = $state<ProviderConnection | null>(null);
	let connHistories = $state<ProviderConnectionHistory[]>([]);

	// Create Coverage Modal
	let isCovModalOpen = $state(false);
	let covCountryId = $state('');
	let covOperatorId = $state('');
	let covStatus = $state<'declared' | 'tested' | 'active' | 'suspended'>('declared');
	let covNotes = $state('');
	let covDataSource = $state('DEV SAMPLE');
	let covReason = $state('');
	let covError = $state<string | null>(null);
	let isSubmittingCov = $state(false);

	const isPlatformOrg = $derived(auth.tenant?.actor_org_type === 'platform');

	async function loadAll() {
		if (!isPlatformOrg) {
			errorMessage = '403 Forbidden: Providers are platform-only.';
			isLoading = false;
			return;
		}
		isLoading = true;
		errorMessage = null;
		try {
			const [pRes, connRes, covRes, histRes, cRes, opRes] = await Promise.all([
				apiClient.getProvider(providerId),
				apiClient.listProviderConnections(providerId),
				apiClient.listProviderCoverage({ provider_id: providerId, pageSize: 100 }),
				apiClient.getProviderHistory(providerId),
				apiClient.listCountries({ pageSize: 300 }),
				apiClient.listOperators({ pageSize: 200 })
			]);
			providerData = pRes.data;
			connections = connRes.data || [];
			coverage = covRes.data.items || [];
			history = histRes.data || [];
			countries = cRes.data.items || [];
			operators = opRes.data.items || [];

			editName = providerData.name;
			editLegal = providerData.legal_entity_name;
			editTech = providerData.contact_technical;
			editComm = providerData.contact_commercial;
			editNoc = providerData.contact_noc;
			editTimezone = providerData.timezone;
			editCurrency = providerData.currency_code;
			editStatus = providerData.status;
			editNotes = providerData.notes;
		} catch (err) {
			errorMessage = err instanceof ApiClientError ? err.message : 'Failed to load provider details.';
		} finally {
			isLoading = false;
		}
	}

	onMount(() => {
		loadAll();
	});

	async function saveProviderOverview() {
		if (!providerData) return;
		isSavingProvider = true;
		try {
			const res = await apiClient.updateProvider(providerData.id, {
				name: editName.trim(),
				legal_entity_name: editLegal.trim(),
				contact_technical: editTech.trim(),
				contact_commercial: editComm.trim(),
				contact_noc: editNoc.trim(),
				timezone: editTimezone.trim(),
				currency_code: editCurrency.trim().toUpperCase(),
				status: editStatus,
				notes: editNotes.trim(),
				reason: editReason.trim() || 'Updated provider details'
			});
			if (res.data.pending_approval) {
				toast('Provider status/edit submitted to Maker-Checker queue', 'info');
			} else {
				toast('Provider updated', 'success');
			}
			editReason = '';
			await loadAll();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Update failed', 'error');
		} finally {
			isSavingProvider = false;
		}
	}

	function openCreateConnection() {
		connName = '';
		connProtocol = providerData?.provider_type === 'http' ? 'http' : 'smpp';
		connRoleNotes = '';
		connStatus = 'draft';
		connMaxTps = 50;
		connSmppHost = '';
		connSmppPort = 2775;
		connSmppSystemId = '';
		connSmppPassword = '';
		connSmppSystemType = 'CMT';
		connSmppBindType = 'transceiver';
		connSmppTls = true;
		connSmppWindowSize = 10;
		connSmppEnquireLink = 30;
		connSmppCharset = 'GSM7';
		connIpAllowlist = '';
		connHttpBaseUrl = '';
		connHttpAuthType = 'bearer';
		connHttpCredentials = '';
		connHttpFormatNotes = '';
		connHttpDlrNotes = '';
		connHttpMethod = 'POST';
		connHttpUrlTemplate = '';
		connHttpAuthHeaderName = '';
		connHttpAuthQueryParam = '';
		connHttpHeadersJson = '{}';
		connHttpBodyFormat = 'json';
		connHttpBodyTemplate = '{"to":"{{destination}}","from":"{{sender}}","text":"{{text}}","message_id":"{{message_id}}","encoding":"{{encoding}}"}';
		connHttpMsgIdPath = 'message_id';
		connHttpErrorCodePath = 'error';
		connHttpSuccessMin = 200;
		connHttpSuccessMax = 299;
		connHttpDlrMode = 'none';
		connHttpDlrSecret = '';
		connHttpCallbackSigAlgo = 'hmac_sha256';
		connHttpCallbackMsgIdPath = 'message_id';
		connHttpCallbackStatusPath = 'status';
		connHttpPollUrlTemplate = '';
		connHttpPollIntervalSec = 30;
		connHttpPollStatusPath = 'status';
		connReason = '';
		connError = null;
		isConnModalOpen = true;
	}

	function buildHttpConfigPayload(): HTTPConnectorConfig {
		let headers: Record<string, string> | undefined = undefined;
		const trimmedHdr = connHttpHeadersJson.trim();
		if (trimmedHdr && trimmedHdr !== '{}') {
			const parsed = JSON.parse(trimmedHdr);
			if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
				headers = {};
				for (const [k, v] of Object.entries(parsed)) {
					headers[k] = String(v);
				}
			} else {
				throw new Error('Custom headers must be a JSON object of key/value strings');
			}
		}
		return {
			method: connHttpMethod,
			url_template: connHttpUrlTemplate.trim() || undefined,
			headers,
			auth_header_name: connHttpAuthHeaderName.trim() || undefined,
			auth_query_param: connHttpAuthQueryParam.trim() || undefined,
			body_format: connHttpBodyFormat,
			body_template: connHttpBodyTemplate,
			success_http_min: Number(connHttpSuccessMin) || 200,
			success_http_max: Number(connHttpSuccessMax) || 299,
			provider_msg_id_path: connHttpMsgIdPath.trim() || 'message_id',
			error_code_path: connHttpErrorCodePath.trim() || 'error',
			callback_signature_algo: connHttpCallbackSigAlgo,
			callback_msg_id_path: connHttpCallbackMsgIdPath.trim() || 'message_id',
			callback_status_path: connHttpCallbackStatusPath.trim() || 'status',
			poll_url_template: connHttpPollUrlTemplate.trim() || undefined,
			poll_interval_sec: Number(connHttpPollIntervalSec) || 30,
			poll_status_path: connHttpPollStatusPath.trim() || 'status'
		};
	}

	function openEditHttpConfig(c: ProviderConnection) {
		editHttpConn = c;
		connHttpBaseUrl = c.http_base_url;
		connHttpAuthType = c.http_auth_type;
		const cfg = c.http_config || {};
		connHttpMethod = cfg.method || 'POST';
		connHttpUrlTemplate = cfg.url_template || '';
		connHttpAuthHeaderName = cfg.auth_header_name || '';
		connHttpAuthQueryParam = cfg.auth_query_param || '';
		connHttpHeadersJson = JSON.stringify(cfg.headers || {}, null, 2);
		connHttpBodyFormat = (cfg.body_format as 'json' | 'form' | 'none') || 'json';
		connHttpBodyTemplate = cfg.body_template || '{"to":"{{destination}}","from":"{{sender}}","text":"{{text}}","message_id":"{{message_id}}","encoding":"{{encoding}}"}';
		connHttpMsgIdPath = cfg.provider_msg_id_path || 'message_id';
		connHttpErrorCodePath = cfg.error_code_path || 'error';
		connHttpSuccessMin = cfg.success_http_min || 200;
		connHttpSuccessMax = cfg.success_http_max || 299;
		connHttpDlrMode = c.http_dlr_mode || 'none';
		connHttpCallbackSigAlgo = (cfg.callback_signature_algo as 'hmac_sha256' | 'shared_secret' | 'none') || 'hmac_sha256';
		connHttpCallbackMsgIdPath = cfg.callback_msg_id_path || 'message_id';
		connHttpCallbackStatusPath = cfg.callback_status_path || 'status';
		connHttpPollUrlTemplate = cfg.poll_url_template || '';
		connHttpPollIntervalSec = cfg.poll_interval_sec || 30;
		connHttpPollStatusPath = cfg.poll_status_path || 'status';
		connReason = '';
		editHttpError = null;
	}

	async function submitEditHttpConfig() {
		if (!editHttpConn) return;
		isSavingHttpCfg = true;
		editHttpError = null;
		try {
			const httpCfg = buildHttpConfigPayload();
			const res = await apiClient.updateProviderConnection(providerId, editHttpConn.id, {
				http_base_url: connHttpBaseUrl.trim(),
				http_auth_type: connHttpAuthType,
				http_config: httpCfg,
				http_dlr_mode: connHttpDlrMode,
				reason: connReason.trim() || 'Updated HTTP connector template'
			});
			editHttpConn = null;
			if (res.data.pending_approval) {
				toast('HTTP config update submitted for approval', 'info');
			} else {
				toast('HTTP connector template saved', 'success');
			}
			await loadAll();
		} catch (err) {
			editHttpError = err instanceof ApiClientError ? err.message : (err instanceof Error ? err.message : 'Failed to save HTTP config');
		} finally {
			isSavingHttpCfg = false;
		}
	}

	function openTestSend(c: ProviderConnection) {
		testSendTargetConn = c;
		testDest = '+15550001234';
		testSenderId = 'ZNERO';
		testText = 'Znero HTTP connector test';
		testSimMode = '';
		testResult = null;
		testError = null;
	}

	async function submitTestSend() {
		if (!testSendTargetConn) return;
		isTestingSend = true;
		testError = null;
		testResult = null;
		try {
			const res = await apiClient.testProviderConnectionSend(providerId, testSendTargetConn.id, {
				destination: testDest.trim(),
				sender_id: testSenderId.trim(),
				text: testText,
				simulate_mode: testSimMode
			});
			testResult = res.data;
		} catch (err) {
			testError = err instanceof ApiClientError ? err.message : 'Test send failed';
		} finally {
			isTestingSend = false;
		}
	}

	function copyCallbackUrl(path: string) {
		const full = typeof window !== 'undefined' ? window.location.origin + path : path;
		if (typeof navigator !== 'undefined' && navigator.clipboard) {
			void navigator.clipboard.writeText(full);
			toast('Callback URL copied to clipboard', 'success');
		}
	}

	async function submitConnection() {
		isSubmittingConn = true;
		connError = null;
		try {
			const payload: Record<string, unknown> = {
				name: connName.trim(),
				protocol: connProtocol,
				role_notes: connRoleNotes.trim(),
				status: connStatus,
				max_tps: Number(connMaxTps),
				ip_allowlist_notes: connIpAllowlist.trim(),
				reason: connReason.trim() || 'Created provider connection'
			};
			if (connProtocol === 'smpp') {
				payload.smpp_host = connSmppHost.trim();
				payload.smpp_port = Number(connSmppPort);
				payload.smpp_system_id = connSmppSystemId.trim();
				if (connSmppPassword) payload.smpp_password = connSmppPassword;
				payload.smpp_system_type = connSmppSystemType.trim();
				payload.smpp_bind_type = connSmppBindType;
				payload.smpp_tls = connSmppTls;
				payload.smpp_window_size = Number(connSmppWindowSize);
				payload.smpp_enquire_link_sec = Number(connSmppEnquireLink);
				payload.smpp_charset = connSmppCharset;
			} else {
				payload.http_base_url = connHttpBaseUrl.trim();
				payload.http_auth_type = connHttpAuthType;
				if (connHttpCredentials) payload.http_credentials = connHttpCredentials;
				payload.http_format_notes = connHttpFormatNotes.trim();
				payload.http_dlr_callback_notes = connHttpDlrNotes.trim();
				payload.http_config = buildHttpConfigPayload();
				payload.http_dlr_mode = connHttpDlrMode;
				if (connHttpDlrSecret) payload.http_dlr_secret = connHttpDlrSecret;
			}
			const res = await apiClient.createProviderConnection(providerId, payload);
			isConnModalOpen = false;
			connSmppPassword = '';
			connHttpCredentials = '';
			connHttpDlrSecret = '';
			if (res.data.pending_approval) {
				toast('Connection creation submitted to Maker-Checker approval queue', 'info');
			} else {
				toast('Provider connection created (secret encrypted at rest)', 'success');
			}
			await loadAll();
		} catch (err) {
			connError = err instanceof ApiClientError ? err.message : 'Failed to create connection';
		} finally {
			isSubmittingConn = false;
		}
	}

	function openUpdateSecretModal(c: ProviderConnection) {
		secretTargetConn = c;
		secretKind = 'auth';
		newSecretValue = '';
		secretReason = '';
		secretError = null;
	}

	async function submitSecretUpdate() {
		if (!secretTargetConn || !newSecretValue) return;
		isUpdatingSecret = true;
		secretError = null;
		try {
			const body: { smpp_password?: string; http_credentials?: string; http_dlr_secret?: string; reason: string } = {
				reason: secretReason.trim() || 'Rotated connection credential'
			};
			if (secretTargetConn.protocol === 'smpp') {
				body.smpp_password = newSecretValue;
			} else if (secretKind === 'dlr') {
				body.http_dlr_secret = newSecretValue;
			} else {
				body.http_credentials = newSecretValue;
			}
			const res = await apiClient.updateProviderConnectionSecret(providerId, secretTargetConn.id, body);
			newSecretValue = '';
			secretTargetConn = null;
			if (res.data.pending_approval) {
				toast('Secret rotation submitted to Maker-Checker approval queue', 'info');
			} else {
				toast('Connection secret rotated and encrypted', 'success');
			}
			await loadAll();
		} catch (err) {
			secretError = err instanceof ApiClientError ? err.message : 'Failed to update secret';
		} finally {
			isUpdatingSecret = false;
		}
	}

	function openStatusModal(c: ProviderConnection) {
		statusTargetConn = c;
		nextConnStatus = c.status === 'active' ? 'disabled' : 'active';
		statusReason = '';
	}

	async function submitStatusChange() {
		if (!statusTargetConn) return;
		isChangingStatus = true;
		try {
			const res = await apiClient.changeProviderConnectionStatus(
				providerId,
				statusTargetConn.id,
				nextConnStatus,
				statusReason.trim() || `Changed connection status to ${nextConnStatus}`
			);
			statusTargetConn = null;
			if (res.data.pending_approval) {
				toast('Connection status change submitted to Maker-Checker approval queue', 'info');
			} else {
				toast('Connection status updated', 'success');
			}
			await loadAll();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Failed to change status', 'error');
		} finally {
			isChangingStatus = false;
		}
	}

	async function openConnHistory(c: ProviderConnection) {
		histTargetConn = c;
		try {
			const res = await apiClient.getProviderConnectionHistory(providerId, c.id);
			connHistories = res.data || [];
		} catch {
			toast('Failed to load connection history', 'error');
		}
	}

	async function submitCoverage() {
		isSubmittingCov = true;
		covError = null;
		try {
			const res = await apiClient.createProviderCoverage({
				provider_id: providerId,
				country_id: covCountryId,
				operator_id: covOperatorId || null,
				status: covStatus,
				notes: covNotes.trim(),
				data_source: covDataSource.trim() || 'DEV SAMPLE',
				reason: covReason.trim() || 'Added provider coverage'
			});
			isCovModalOpen = false;
			if (res.data.pending_approval) {
				toast('Coverage change submitted to Maker-Checker queue', 'info');
			} else {
				toast('Provider coverage added', 'success');
			}
			await loadAll();
		} catch (err) {
			covError = err instanceof ApiClientError ? err.message : 'Failed to add coverage';
		} finally {
			isSubmittingCov = false;
		}
	}

	async function handleRollbackProvider(version: number) {
		const reason = prompt(`Enter reason for rolling back provider to version ${version}:`, `Rollback to v${version}`);
		if (!reason) return;
		try {
			await apiClient.rollbackProvider(providerId, version, reason);
			toast(`Rolled back provider to version ${version}`, 'success');
			await loadAll();
		} catch (err) {
			toast(err instanceof ApiClientError ? err.message : 'Rollback failed', 'error');
		}
	}
</script>

<div class="space-y-6">
	{#if isLoading}
		<div class="flex justify-center py-16">
			<Loading message="Loading provider configuration..." />
		</div>
	{:else if errorMessage || !providerData}
		<ErrorState message={errorMessage || 'Provider not found'} retry={loadAll} />
	{:else}
		<div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
			<div>
				<div class="flex items-center gap-2 text-xs text-slate-500">
					<a href="/providers" class="hover:underline">Providers</a>
					<span>/</span>
					<span class="font-mono font-bold text-indigo-600">{providerData.code}</span>
				</div>
				<h1 class="mt-1 text-2xl font-bold tracking-tight text-slate-900 dark:text-white">
					{providerData.name}
				</h1>
				<p class="text-xs text-slate-500">
					Protocol: <span class="font-mono uppercase font-semibold">{providerData.provider_type}</span> •
					Status: <span class="font-mono uppercase font-semibold">{providerData.status}</span> •
					Version: <span class="font-mono">v{providerData.version}</span>
				</p>
			</div>

			<div class="flex rounded-lg border border-slate-200 bg-white p-0.5 dark:border-slate-700 dark:bg-slate-800">
				<button
					type="button"
					onclick={() => activeTab = 'connections'}
					class="rounded-md px-3 py-1.5 text-xs font-semibold transition {activeTab === 'connections' ? 'bg-indigo-600 text-white' : 'text-slate-600 dark:text-slate-300'}"
				>
					Connections ({connections.length})
				</button>
				<button
					type="button"
					onclick={() => activeTab = 'coverage'}
					class="rounded-md px-3 py-1.5 text-xs font-semibold transition {activeTab === 'coverage' ? 'bg-indigo-600 text-white' : 'text-slate-600 dark:text-slate-300'}"
				>
					Coverage ({coverage.length})
				</button>
				<button
					type="button"
					onclick={() => activeTab = 'overview'}
					class="rounded-md px-3 py-1.5 text-xs font-semibold transition {activeTab === 'overview' ? 'bg-indigo-600 text-white' : 'text-slate-600 dark:text-slate-300'}"
				>
					Overview
				</button>
				<button
					type="button"
					onclick={() => activeTab = 'history'}
					class="rounded-md px-3 py-1.5 text-xs font-semibold transition {activeTab === 'history' ? 'bg-indigo-600 text-white' : 'text-slate-600 dark:text-slate-300'}"
				>
					History ({history.length})
				</button>
			</div>
		</div>

		{#if activeTab === 'connections'}
			<div class="space-y-4">
				<div class="flex items-center justify-between">
					<div>
						<h2 class="text-base font-bold text-slate-900 dark:text-white">Connection Configurations</h2>
						<p class="text-xs text-slate-500">
							Phase 4 stores validated configuration and AES-256-GCM encrypted credentials only. Passwords and tokens are write-only and never returned by the API.
						</p>
					</div>
					<button
						type="button"
						onclick={openCreateConnection}
						class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white hover:bg-indigo-500"
					>
						+ New Connection
					</button>
				</div>

				{#if connections.length === 0}
					<div class="rounded-xl border border-slate-200 bg-white p-8 text-center text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-900">
						No connections configured for this provider yet.
					</div>
				{:else}
					<div class="grid grid-cols-1 gap-4">
						{#each connections as c}
							{@const secretMeta = c.protocol === 'smpp' ? (c.smpp_password || c.smpp_password_meta) : (c.http_credentials || c.http_credentials_meta)}
							<div data-testid="connection-card" class="rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-slate-900">
								<div class="flex flex-wrap items-start justify-between gap-4">
									<div>
										<div class="flex items-center gap-2">
											<h3 class="text-sm font-bold text-slate-900 dark:text-white">{c.name}</h3>
											<span class="rounded bg-slate-100 px-2 py-0.5 text-[10px] font-mono uppercase dark:bg-slate-800">{c.protocol}</span>
											<span class="rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase {c.status === 'active'
												? 'bg-emerald-100 text-emerald-800'
												: c.status === 'draft'
													? 'bg-amber-100 text-amber-800'
													: 'bg-rose-100 text-rose-800'}">
												{c.status}
											</span>
											<span class="text-[11px] font-mono text-slate-400">v{c.version}</span>
										</div>
										<p class="mt-1 text-xs text-slate-500">{c.role_notes || 'No role notes'}</p>
									</div>

									<div class="flex flex-wrap items-center gap-2 text-xs">
										{#if c.protocol === 'http'}
											<button
												type="button"
												onclick={() => openTestSend(c)}
												class="rounded-lg border border-emerald-300 bg-emerald-50 px-3 py-1.5 font-semibold text-emerald-700 hover:bg-emerald-100 dark:border-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-300"
											>
												Test Send
											</button>
											<button
												type="button"
												onclick={() => openEditHttpConfig(c)}
												class="rounded-lg border border-slate-300 px-3 py-1.5 font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
											>
												Configure HTTP
											</button>
										{/if}
										<button
											type="button"
											onclick={() => openUpdateSecretModal(c)}
											class="rounded-lg border border-indigo-300 bg-indigo-50 px-3 py-1.5 font-semibold text-indigo-700 hover:bg-indigo-100 dark:border-indigo-800 dark:bg-indigo-950/50 dark:text-indigo-300"
										>
											Update Secret
										</button>
										<button
											type="button"
											onclick={() => openStatusModal(c)}
											class="rounded-lg border border-slate-300 px-3 py-1.5 font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
										>
											Change Status
										</button>
										<button
											type="button"
											onclick={() => openConnHistory(c)}
											class="rounded-lg border border-slate-300 px-3 py-1.5 font-semibold text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300"
										>
											History
										</button>
									</div>
								</div>

								<div class="mt-4 grid grid-cols-1 sm:grid-cols-3 gap-3 border-t border-slate-100 pt-3 text-xs dark:border-slate-800">
									{#if c.protocol === 'smpp'}
										<div>
											<span class="text-slate-400">Endpoint:</span>
											<span class="ml-1 font-mono font-semibold">{c.smpp_host}:{c.smpp_port}</span>
										</div>
										<div>
											<span class="text-slate-400">System ID / Bind:</span>
											<span class="ml-1 font-mono">{c.smpp_system_id} ({c.smpp_bind_type})</span>
										</div>
									{:else}
										<div class="sm:col-span-2">
											<span class="text-slate-400">Base URL:</span>
											<span class="ml-1 font-mono font-semibold">{c.http_base_url}</span>
											<span class="ml-2 text-slate-400">({c.http_config?.method || 'POST'} / {c.http_config?.body_format || 'json'} / auth:{c.http_auth_type})</span>
										</div>
										<div>
											<span class="text-slate-400">DLR Mode:</span>
											<span class="ml-1 font-mono font-semibold uppercase">{c.http_dlr_mode || 'none'}</span>
										</div>
										{#if c.http_dlr_callback_path && c.http_dlr_mode === 'callback'}
											<div class="sm:col-span-3 flex flex-wrap items-center gap-2 rounded-lg bg-slate-50 px-3 py-2 dark:bg-slate-800/60">
												<span class="text-slate-500">Generated Callback URL:</span>
												<code data-testid="http-callback-url" class="font-mono text-[11px] font-semibold text-indigo-700 dark:text-indigo-300">{c.http_dlr_callback_path}</code>
												<button
													type="button"
													onclick={() => copyCallbackUrl(c.http_dlr_callback_path || '')}
													class="rounded border border-slate-300 px-2 py-0.5 text-[10px] font-semibold text-slate-600 hover:bg-white dark:border-slate-700 dark:text-slate-300"
												>
													Copy Full URL
												</button>
												<span class="text-[11px] text-slate-400">
													DLR Secret: {c.http_dlr_secret?.masked_display || 'Not set'}
												</span>
											</div>
										{/if}
									{/if}
									<div>
										<span class="text-slate-400">Max TPS:</span>
										<span class="ml-1 font-mono font-semibold">{c.max_tps}</span>
									</div>
									<div class="sm:col-span-3 flex items-center gap-2">
										<span class="text-slate-400">Credential Status:</span>
										<span
											data-testid="secret-masked-badge"
											class="inline-flex items-center rounded bg-slate-100 px-2 py-0.5 font-mono text-[11px] font-semibold text-slate-700 dark:bg-slate-800 dark:text-slate-200"
										>
											{secretMeta?.masked_display || (secretMeta?.set || secretMeta?.configured ? '••• set' : 'Not set')}
										</span>
										{#if secretMeta?.last_changed || secretMeta?.changed_at}
											<span class="text-[11px] text-slate-400">
												(changed {new Date(secretMeta.last_changed || secretMeta.changed_at || '').toLocaleString()})
											</span>
										{/if}
									</div>
								</div>
							</div>
						{/each}
					</div>
				{/if}
			</div>
		{:else if activeTab === 'coverage'}
			<div class="space-y-4">
				<div class="flex items-center justify-between">
					<div>
						<h2 class="text-base font-bold text-slate-900 dark:text-white">Provider Coverage</h2>
						<p class="text-xs text-slate-500">Declared, tested, and active country/operator routes supported by {providerData.code}.</p>
					</div>
					<button
						type="button"
						onclick={() => { covCountryId = ''; covOperatorId = ''; covNotes = ''; covReason = ''; covError = null; isCovModalOpen = true; }}
						class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white hover:bg-indigo-500"
					>
						+ Add Coverage
					</button>
				</div>

				<div class="overflow-x-auto rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
					<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-left text-xs">
						<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 uppercase font-semibold">
							<tr>
								<th class="px-4 py-3">Country</th>
								<th class="px-4 py-3">Operator (MCC/MNC)</th>
								<th class="px-4 py-3">Status</th>
								<th class="px-4 py-3">Source</th>
								<th class="px-4 py-3">Notes</th>
							</tr>
						</thead>
						<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
							{#each coverage as cov}
								<tr>
									<td class="px-4 py-3 font-semibold">{cov.country_iso2} — {cov.country_name}</td>
									<td class="px-4 py-3 font-mono">
										{cov.operator_name ? `${cov.operator_name} (${cov.operator_mcc}/${cov.operator_mnc})` : 'All Operators (Country-wide)'}
									</td>
									<td class="px-4 py-3 uppercase font-mono font-semibold">{cov.status}</td>
									<td class="px-4 py-3">{cov.data_source}</td>
									<td class="px-4 py-3 text-slate-500">{cov.notes || '—'}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			</div>
		{:else if activeTab === 'overview'}
			<div class="rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-800 dark:bg-slate-900 space-y-4">
				<h2 class="text-base font-bold text-slate-900 dark:text-white">Edit Provider Profile</h2>
				<div class="grid grid-cols-1 sm:grid-cols-2 gap-4 text-xs">
					<div>
						<label for="edit-prov-name" class="block font-medium text-slate-700 mb-1">Provider Name</label>
						<input id="edit-prov-name" type="text" bind:value={editName} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="edit-prov-status" class="block font-medium text-slate-700 mb-1">Status</label>
						<select id="edit-prov-status" bind:value={editStatus} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
							<option value="draft">Draft</option>
							<option value="active">Active</option>
							<option value="suspended">Suspended</option>
							<option value="inactive">Inactive</option>
						</select>
					</div>
					<div>
						<label for="edit-prov-legal" class="block font-medium text-slate-700 mb-1">Legal Entity Name</label>
						<input id="edit-prov-legal" type="text" bind:value={editLegal} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="edit-prov-curr" class="block font-medium text-slate-700 mb-1">Currency Code</label>
						<input id="edit-prov-curr" type="text" maxlength="3" bind:value={editCurrency} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono uppercase dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div class="sm:col-span-2">
						<label for="edit-prov-notes" class="block font-medium text-slate-700 mb-1">Notes</label>
						<textarea id="edit-prov-notes" rows="2" bind:value={editNotes} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white"></textarea>
					</div>
					<div class="sm:col-span-2">
						<label for="edit-prov-reason" class="block font-medium text-slate-700 mb-1">Reason for Change</label>
						<input id="edit-prov-reason" type="text" bind:value={editReason} placeholder="Audit reason..." class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
				</div>
				<div class="flex justify-end">
					<button
						type="button"
						disabled={isSavingProvider}
						onclick={saveProviderOverview}
						class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white hover:bg-indigo-500"
					>
						{isSavingProvider ? 'Saving...' : 'Save Changes'}
					</button>
				</div>
			</div>
		{:else if activeTab === 'history'}
			<div class="overflow-x-auto rounded-xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-slate-900">
				<table class="min-w-full divide-y divide-slate-200 dark:divide-slate-800 text-left text-xs">
					<thead class="bg-slate-50 dark:bg-slate-800/50 text-slate-500 uppercase font-semibold">
						<tr>
							<th class="px-4 py-3">Version</th>
							<th class="px-4 py-3">Changed At</th>
							<th class="px-4 py-3">Reason</th>
							<th class="px-4 py-3">Snapshot</th>
							<th class="px-4 py-3 text-right">Actions</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200 dark:divide-slate-800">
						{#each history as h}
							<tr>
								<td class="px-4 py-3 font-mono font-bold">v{h.version}</td>
								<td class="px-4 py-3">{new Date(h.changed_at).toLocaleString()}</td>
								<td class="px-4 py-3">{h.reason}</td>
								<td class="px-4 py-3 font-mono text-[11px] max-w-md truncate">{JSON.stringify(h.snapshot)}</td>
								<td class="px-4 py-3 text-right">
									<button
										type="button"
										onclick={() => handleRollbackProvider(h.version)}
										class="rounded bg-slate-100 px-2.5 py-1 text-xs font-semibold text-slate-700 hover:bg-slate-200"
									>
										Rollback to v{h.version}
									</button>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	{/if}
</div>

<!-- Create Connection Modal -->
{#if isConnModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm overflow-y-auto">
		<div class="w-full max-w-2xl rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900 my-8">
			<div class="flex items-center justify-between border-b border-slate-200 pb-3 dark:border-slate-800">
				<h2 class="text-base font-bold text-slate-900 dark:text-white">Create Provider Connection</h2>
				<button type="button" aria-label="Close modal" onclick={() => isConnModalOpen = false} class="text-slate-400">✕</button>
			</div>

			{#if connError}
				<div class="mt-3 rounded-lg bg-rose-50 p-3 text-xs text-rose-700 border border-rose-200">{connError}</div>
			{/if}

			<div class="mt-4 grid grid-cols-1 sm:grid-cols-2 gap-4 text-xs">
				<div>
					<label for="conn-name" class="block font-medium text-slate-700 mb-1">Connection Name *</label>
					<input id="conn-name" type="text" bind:value={connName} placeholder="e.g. Primary SMPP Bind" class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="conn-proto" class="block font-medium text-slate-700 mb-1">Protocol *</label>
					<select id="conn-proto" bind:value={connProtocol} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="smpp">SMPP</option>
						<option value="http">HTTP</option>
					</select>
				</div>
				<div>
					<label for="conn-status" class="block font-medium text-slate-700 mb-1">Initial Status</label>
					<select id="conn-status" bind:value={connStatus} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="draft">Draft</option>
						<option value="active">Active</option>
						<option value="disabled">Disabled</option>
					</select>
				</div>
				<div>
					<label for="conn-tps" class="block font-medium text-slate-700 mb-1">Max TPS *</label>
					<input id="conn-tps" type="number" min="1" max="100000" bind:value={connMaxTps} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>

				{#if connProtocol === 'smpp'}
					<div>
						<label for="conn-host" class="block font-medium text-slate-700 mb-1">SMPP Host (Public DNS/IP) *</label>
						<input id="conn-host" type="text" bind:value={connSmppHost} placeholder="smpp.carrier.example.com" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="conn-port" class="block font-medium text-slate-700 mb-1">SMPP Port *</label>
						<input id="conn-port" type="number" min="1" max="65535" bind:value={connSmppPort} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="conn-sysid" class="block font-medium text-slate-700 mb-1">System ID *</label>
						<input id="conn-sysid" type="text" bind:value={connSmppSystemId} placeholder="znero_bind" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="conn-pass" class="block font-medium text-slate-700 mb-1">SMPP Password (Write-Only)</label>
						<input id="conn-pass" type="password" autocomplete="new-password" bind:value={connSmppPassword} placeholder="Encrypted at rest" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="conn-bind" class="block font-medium text-slate-700 mb-1">Bind Type</label>
						<select id="conn-bind" bind:value={connSmppBindType} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
							<option value="transceiver">Transceiver (TRX)</option>
							<option value="transmitter">Transmitter (TX)</option>
							<option value="receiver">Receiver (RX)</option>
						</select>
					</div>
					<div>
						<label for="conn-charset" class="block font-medium text-slate-700 mb-1">Default Charset</label>
						<select id="conn-charset" bind:value={connSmppCharset} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
							<option value="GSM7">GSM7</option>
							<option value="UCS2">UCS2</option>
							<option value="UTF8">UTF8</option>
							<option value="LATIN1">LATIN1</option>
						</select>
					</div>
				{:else}
					<div class="sm:col-span-2">
						<label for="conn-url" class="block font-medium text-slate-700 mb-1">HTTPS Base URL *</label>
						<input id="conn-url" type="url" bind:value={connHttpBaseUrl} placeholder="https://api.carrier.example.com/v1/sms" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="conn-auth" class="block font-medium text-slate-700 mb-1">HTTP Auth Type</label>
						<select id="conn-auth" bind:value={connHttpAuthType} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
							<option value="bearer">Bearer Token</option>
							<option value="api_key_header">API Key in Header</option>
							<option value="api_key_query">API Key in Query</option>
							<option value="basic">HTTP Basic (user:pass)</option>
							<option value="header_token">Legacy Header Token</option>
							<option value="query_token">Legacy Query Token</option>
							<option value="none">None</option>
						</select>
					</div>
					<div>
						<label for="conn-http-sec" class="block font-medium text-slate-700 mb-1">HTTP Credentials (Write-Only)</label>
						<input id="conn-http-sec" type="password" autocomplete="new-password" bind:value={connHttpCredentials} placeholder="Encrypted at rest" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="conn-http-method" class="block font-medium text-slate-700 mb-1">HTTP Method</label>
						<select id="conn-http-method" bind:value={connHttpMethod} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
							<option value="POST">POST</option>
							<option value="PUT">PUT</option>
							<option value="GET">GET</option>
						</select>
					</div>
					<div>
						<label for="conn-http-fmt" class="block font-medium text-slate-700 mb-1">Body Format</label>
						<select id="conn-http-fmt" bind:value={connHttpBodyFormat} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
							<option value="json">JSON (escaped)</option>
							<option value="form">Form URL-Encoded (escaped)</option>
							<option value="none">None (Query URL only)</option>
						</select>
					</div>
					<div class="sm:col-span-2">
						<label for="conn-http-urltpl" class="block font-medium text-slate-700 mb-1">Optional URL Path/Query Template</label>
						<input id="conn-http-urltpl" type="text" bind:value={connHttpUrlTemplate} placeholder="e.g. /send?to=&#123;&#123;destination&#125;&#125; (optional)" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					{#if connHttpAuthType === 'api_key_header' || connHttpAuthType === 'header_token' || connHttpAuthType === 'bearer'}
						<div>
							<label for="conn-auth-hdr" class="block font-medium text-slate-700 mb-1">Auth Header Name</label>
							<input id="conn-auth-hdr" type="text" bind:value={connHttpAuthHeaderName} placeholder={connHttpAuthType === 'api_key_header' ? 'X-API-Key' : 'Authorization'} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
						</div>
					{:else if connHttpAuthType === 'api_key_query' || connHttpAuthType === 'query_token'}
						<div>
							<label for="conn-auth-qp" class="block font-medium text-slate-700 mb-1">Auth Query Param</label>
							<input id="conn-auth-qp" type="text" bind:value={connHttpAuthQueryParam} placeholder="api_key" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
						</div>
					{/if}
					<div>
						<label for="conn-http-dlrmode" class="block font-medium text-slate-700 mb-1">DLR Mode</label>
						<select id="conn-http-dlrmode" bind:value={connHttpDlrMode} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
							<option value="none">None</option>
							<option value="callback">Webhook Callback</option>
							<option value="polling">Active Polling</option>
						</select>
					</div>
					{#if connHttpBodyFormat !== 'none'}
						<div class="sm:col-span-2">
							<label for="conn-http-bodytpl" class="block font-medium text-slate-700 mb-1">
								Body Template (placeholders: &#123;&#123;destination&#125;&#125;, &#123;&#123;sender&#125;&#125;, &#123;&#123;text&#125;&#125;, &#123;&#123;message_id&#125;&#125;, &#123;&#123;encoding&#125;&#125;)
							</label>
							<textarea id="conn-http-bodytpl" rows="3" bind:value={connHttpBodyTemplate} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono text-[11px] dark:border-slate-700 dark:bg-slate-800 dark:text-white"></textarea>
						</div>
					{/if}
					<div>
						<label for="conn-http-msgidpath" class="block font-medium text-slate-700 mb-1">Provider Msg ID JSON Path</label>
						<input id="conn-http-msgidpath" type="text" bind:value={connHttpMsgIdPath} placeholder="message_id" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="conn-http-errpath" class="block font-medium text-slate-700 mb-1">Error Code JSON Path</label>
						<input id="conn-http-errpath" type="text" bind:value={connHttpErrorCodePath} placeholder="error" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					{#if connHttpDlrMode === 'callback'}
						<div>
							<label for="conn-dlr-algo" class="block font-medium text-slate-700 mb-1">Callback Verification</label>
							<select id="conn-dlr-algo" bind:value={connHttpCallbackSigAlgo} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
								<option value="hmac_sha256">HMAC-SHA256</option>
								<option value="shared_secret">Shared Secret</option>
								<option value="none">Unsigned</option>
							</select>
						</div>
						<div>
							<label for="conn-dlr-sec" class="block font-medium text-slate-700 mb-1">Callback Secret (Optional Write-Only)</label>
							<input id="conn-dlr-sec" type="password" autocomplete="new-password" bind:value={connHttpDlrSecret} placeholder="Optional HMAC secret" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
						</div>
					{:else if connHttpDlrMode === 'polling'}
						<div class="sm:col-span-2">
							<label for="conn-poll-url" class="block font-medium text-slate-700 mb-1">Polling URL Template *</label>
							<input id="conn-poll-url" type="url" bind:value={connHttpPollUrlTemplate} placeholder="https://api.carrier.example.com/v1/dlr?id=&#123;&#123;provider_message_id&#125;&#125;" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
						</div>
					{/if}
				{/if}

				<div class="sm:col-span-2">
					<label for="conn-role" class="block font-medium text-slate-700 mb-1">Role / Routing Notes</label>
					<input id="conn-role" type="text" bind:value={connRoleNotes} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div class="sm:col-span-2">
					<label for="conn-reason" class="block font-medium text-slate-700 mb-1">Audit Reason *</label>
					<input id="conn-reason" type="text" bind:value={connReason} placeholder="Reason for creating connection" class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
			</div>

			<div class="mt-6 flex items-center justify-end gap-2">
				<button type="button" onclick={() => isConnModalOpen = false} class="rounded-lg border border-slate-300 px-4 py-2 text-xs font-semibold text-slate-700">
					Cancel
				</button>
				<button
					type="button"
					disabled={isSubmittingConn || !connName.trim()}
					onclick={submitConnection}
					class="rounded-lg bg-indigo-600 px-4 py-2 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					{isSubmittingConn ? 'Saving...' : 'Create Connection'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Update Secret Modal (Write-Only) -->
{#if secretTargetConn}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-md rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Update Connection Secret: {secretTargetConn.name}</h2>
			<p class="mt-1 text-xs text-slate-500">
				Secrets are write-only and encrypted at rest with AES-256-GCM. The existing secret cannot be viewed.
			</p>
			{#if secretError}
				<div class="mt-3 rounded-lg bg-rose-50 p-2.5 text-xs text-rose-700 border border-rose-200">{secretError}</div>
			{/if}
			<div class="mt-4 space-y-3 text-xs">
				{#if secretTargetConn.protocol === 'http'}
					<div>
						<label for="sec-kind" class="block font-medium text-slate-700 mb-1">Credential Target</label>
						<select id="sec-kind" bind:value={secretKind} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
							<option value="auth">HTTP API Credential / Key</option>
							<option value="dlr">HTTP DLR Callback Secret (HMAC)</option>
						</select>
					</div>
				{/if}
				<div>
					<label for="sec-val" class="block font-medium text-slate-700 mb-1">New Secret / Password *</label>
					<input id="sec-val" type="password" autocomplete="new-password" bind:value={newSecretValue} placeholder="Enter new credential" class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="sec-reason" class="block font-medium text-slate-700 mb-1">Audit Reason *</label>
					<input id="sec-reason" type="text" bind:value={secretReason} placeholder="e.g. Scheduled key rotation" class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
			</div>
			<div class="mt-5 flex justify-end gap-2">
				<button type="button" onclick={() => secretTargetConn = null} class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700">Cancel</button>
				<button
					type="button"
					disabled={isUpdatingSecret || !newSecretValue}
					onclick={submitSecretUpdate}
					class="rounded-lg bg-indigo-600 px-4 py-1.5 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					{isUpdatingSecret ? 'Encrypting...' : 'Rotate Secret'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Change Connection Status Modal -->
{#if statusTargetConn}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-md rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Change Connection Status: {statusTargetConn.name}</h2>
			<div class="mt-4 space-y-3 text-xs">
				<div>
					<label for="next-conn-status" class="block font-medium text-slate-700 mb-1">Target Status</label>
					<select id="next-conn-status" bind:value={nextConnStatus} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="draft">Draft</option>
						<option value="active">Active</option>
						<option value="disabled">Disabled</option>
					</select>
				</div>
				<div>
					<label for="conn-status-reason" class="block font-medium text-slate-700 mb-1">Reason *</label>
					<input id="conn-status-reason" type="text" bind:value={statusReason} placeholder="Reason for status change" class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
			</div>
			<div class="mt-5 flex justify-end gap-2">
				<button type="button" onclick={() => statusTargetConn = null} class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700">Cancel</button>
				<button
					type="button"
					disabled={isChangingStatus || !statusReason.trim()}
					onclick={submitStatusChange}
					class="rounded-lg bg-indigo-600 px-4 py-1.5 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					{isChangingStatus ? 'Submitting...' : 'Submit Status Change'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Edit HTTP Connector Template Modal -->
{#if editHttpConn}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm overflow-y-auto">
		<div class="w-full max-w-2xl rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900 my-8">
			<div class="flex items-center justify-between border-b border-slate-200 pb-3 dark:border-slate-800">
				<h2 class="text-base font-bold text-slate-900 dark:text-white">Configure HTTP Connector: {editHttpConn.name}</h2>
				<button type="button" aria-label="Close modal" onclick={() => editHttpConn = null} class="text-slate-400">✕</button>
			</div>
			{#if editHttpError}
				<div class="mt-3 rounded-lg bg-rose-50 p-3 text-xs text-rose-700 border border-rose-200">{editHttpError}</div>
			{/if}
			<div class="mt-4 grid grid-cols-1 sm:grid-cols-2 gap-3 text-xs">
				<div class="sm:col-span-2">
					<label for="edit-http-url" class="block font-medium text-slate-700 mb-1">Base URL *</label>
					<input id="edit-http-url" type="url" bind:value={connHttpBaseUrl} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="edit-http-auth" class="block font-medium text-slate-700 mb-1">Auth Type</label>
					<select id="edit-http-auth" bind:value={connHttpAuthType} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="bearer">Bearer Token</option>
						<option value="api_key_header">API Key in Header</option>
						<option value="api_key_query">API Key in Query</option>
						<option value="basic">HTTP Basic (user:pass)</option>
						<option value="header_token">Legacy Header Token</option>
						<option value="query_token">Legacy Query Token</option>
						<option value="none">None</option>
					</select>
				</div>
				<div>
					<label for="edit-http-method" class="block font-medium text-slate-700 mb-1">Method</label>
					<select id="edit-http-method" bind:value={connHttpMethod} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="POST">POST</option>
						<option value="PUT">PUT</option>
						<option value="GET">GET</option>
					</select>
				</div>
				<div>
					<label for="edit-http-fmt" class="block font-medium text-slate-700 mb-1">Body Format</label>
					<select id="edit-http-fmt" bind:value={connHttpBodyFormat} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="json">JSON</option>
						<option value="form">Form URL-Encoded</option>
						<option value="none">None</option>
					</select>
				</div>
				<div>
					<label for="edit-http-dlrmode" class="block font-medium text-slate-700 mb-1">DLR Mode</label>
					<select id="edit-http-dlrmode" bind:value={connHttpDlrMode} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="none">None</option>
						<option value="callback">Webhook Callback</option>
						<option value="polling">Active Polling</option>
					</select>
				</div>
				<div class="sm:col-span-2">
					<label for="edit-http-hdrs" class="block font-medium text-slate-700 mb-1">Custom Headers (JSON)</label>
					<textarea id="edit-http-hdrs" rows="2" bind:value={connHttpHeadersJson} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono text-[11px] dark:border-slate-700 dark:bg-slate-800 dark:text-white"></textarea>
				</div>
				{#if connHttpBodyFormat !== 'none'}
					<div class="sm:col-span-2">
						<label for="edit-http-tpl" class="block font-medium text-slate-700 mb-1">Body Template</label>
						<textarea id="edit-http-tpl" rows="3" bind:value={connHttpBodyTemplate} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono text-[11px] dark:border-slate-700 dark:bg-slate-800 dark:text-white"></textarea>
					</div>
				{/if}
				<div>
					<label for="edit-http-msgidpath" class="block font-medium text-slate-700 mb-1">Provider Msg ID Path</label>
					<input id="edit-http-msgidpath" type="text" bind:value={connHttpMsgIdPath} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="edit-http-errpath" class="block font-medium text-slate-700 mb-1">Error Code Path</label>
					<input id="edit-http-errpath" type="text" bind:value={connHttpErrorCodePath} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				{#if connHttpDlrMode === 'callback'}
					<div>
						<label for="edit-cb-algo" class="block font-medium text-slate-700 mb-1">Callback Auth Algo</label>
						<select id="edit-cb-algo" bind:value={connHttpCallbackSigAlgo} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
							<option value="hmac_sha256">HMAC-SHA256</option>
							<option value="shared_secret">Shared Secret</option>
							<option value="none">Unsigned</option>
						</select>
					</div>
					<div>
						<label for="edit-cb-id" class="block font-medium text-slate-700 mb-1">Callback Msg ID Path</label>
						<input id="edit-cb-id" type="text" bind:value={connHttpCallbackMsgIdPath} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
				{:else if connHttpDlrMode === 'polling'}
					<div class="sm:col-span-2">
						<label for="edit-poll-url" class="block font-medium text-slate-700 mb-1">Poll URL Template *</label>
						<input id="edit-poll-url" type="url" bind:value={connHttpPollUrlTemplate} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="edit-poll-int" class="block font-medium text-slate-700 mb-1">Poll Interval (sec)</label>
						<input id="edit-poll-int" type="number" min="5" max="3600" bind:value={connHttpPollIntervalSec} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
					<div>
						<label for="edit-poll-stat" class="block font-medium text-slate-700 mb-1">Poll Status Path</label>
						<input id="edit-poll-stat" type="text" bind:value={connHttpPollStatusPath} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
					</div>
				{/if}
				<div class="sm:col-span-2">
					<label for="edit-http-reason" class="block font-medium text-slate-700 mb-1">Audit Reason *</label>
					<input id="edit-http-reason" type="text" bind:value={connReason} placeholder="Reason for updating HTTP connector configuration" class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
			</div>
			<div class="mt-5 flex justify-end gap-2">
				<button type="button" onclick={() => editHttpConn = null} class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700">Cancel</button>
				<button type="button" disabled={isSavingHttpCfg} onclick={submitEditHttpConfig} class="rounded-lg bg-indigo-600 px-4 py-1.5 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50">
					{isSavingHttpCfg ? 'Saving...' : 'Save HTTP Config'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- HTTP Test Send Modal -->
{#if testSendTargetConn}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm overflow-y-auto">
		<div class="w-full max-w-lg rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
			<div class="flex items-center justify-between border-b border-slate-200 pb-3 dark:border-slate-800">
				<h2 class="text-base font-bold text-slate-900 dark:text-white">Test HTTP Send: {testSendTargetConn.name}</h2>
				<button type="button" aria-label="Close modal" onclick={() => testSendTargetConn = null} class="text-slate-400">✕</button>
			</div>
			{#if testError}
				<div class="mt-3 rounded-lg bg-rose-50 p-3 text-xs text-rose-700 border border-rose-200">{testError}</div>
			{/if}
			<div class="mt-4 grid grid-cols-1 sm:grid-cols-2 gap-3 text-xs">
				<div>
					<label for="test-dest" class="block font-medium text-slate-700 mb-1">Destination (E.164)</label>
					<input id="test-dest" type="text" bind:value={testDest} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="test-sender" class="block font-medium text-slate-700 mb-1">Sender ID</label>
					<input id="test-sender" type="text" bind:value={testSenderId} class="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div class="sm:col-span-2">
					<label for="test-text" class="block font-medium text-slate-700 mb-1">Message Text</label>
					<input id="test-text" type="text" bind:value={testText} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div class="sm:col-span-2">
					<label for="test-sim" class="block font-medium text-slate-700 mb-1">Target Mode</label>
					<select id="test-sim" bind:value={testSimMode} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="">Configured Base URL ({testSendTargetConn.http_base_url})</option>
						<option value="accept">Simulate FakeHTTPProvider: 200 Accept</option>
						<option value="reject">Simulate FakeHTTPProvider: 400 Permanent Reject</option>
						<option value="throttle">Simulate FakeHTTPProvider: 429 Throttle</option>
						<option value="server_err">Simulate FakeHTTPProvider: 500 Ambiguous</option>
					</select>
				</div>
			</div>
			{#if testResult}
				<div data-testid="http-test-send-result" class="mt-4 rounded-xl border p-3 text-xs {testResult.success ? 'border-emerald-200 bg-emerald-50 dark:bg-emerald-950/30' : 'border-amber-200 bg-amber-50 dark:bg-amber-950/30'}">
					<div class="flex items-center justify-between font-bold">
						<span>Outcome: {testResult.status} (HTTP {testResult.http_status_code || 'N/A'})</span>
						<span class="font-mono">{testResult.latency_ms}ms {testResult.simulated ? '[Simulated]' : ''}</span>
					</div>
					<div class="mt-2 space-y-1 font-mono text-[11px]">
						{#if testResult.provider_message_id}
							<div>Provider Msg ID: {testResult.provider_message_id}</div>
						{/if}
						{#if testResult.error_code}
							<div>Error Code: {testResult.error_code} — {testResult.error_message}</div>
						{/if}
						<div class="truncate">Request: {testResult.method} {testResult.redacted_url}</div>
						{#if testResult.redacted_body}
							<div class="break-all text-slate-600 dark:text-slate-300">Rendered Body: {testResult.redacted_body}</div>
						{/if}
						{#if testResult.response_snippet}
							<div class="break-all text-slate-600 dark:text-slate-300">Response: {testResult.response_snippet}</div>
						{/if}
					</div>
				</div>
			{/if}
			<div class="mt-5 flex justify-end gap-2">
				<button type="button" onclick={() => testSendTargetConn = null} class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700">Close</button>
				<button type="button" disabled={isTestingSend} onclick={submitTestSend} class="rounded-lg bg-emerald-600 px-4 py-1.5 text-xs font-semibold text-white hover:bg-emerald-500 disabled:opacity-50">
					{isTestingSend ? 'Sending...' : 'Execute Test Send'}
				</button>
			</div>
		</div>
	</div>
{/if}

<!-- Connection History Modal -->
{#if histTargetConn}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-2xl rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
			<div class="flex items-center justify-between border-b border-slate-200 pb-3">
				<h2 class="text-base font-bold text-slate-900 dark:text-white">Connection History: {histTargetConn.name}</h2>
				<button type="button" aria-label="Close modal" onclick={() => histTargetConn = null} class="text-slate-400">✕</button>
			</div>
			<div class="mt-4 max-h-80 overflow-y-auto">
				<table class="min-w-full divide-y divide-slate-200 text-xs">
					<thead>
						<tr>
							<th class="px-3 py-2 text-left">Ver</th>
							<th class="px-3 py-2 text-left">When</th>
							<th class="px-3 py-2 text-left">Reason</th>
							<th class="px-3 py-2 text-left">Snapshot (Secret-Free)</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-slate-200">
						{#each connHistories as ch}
							<tr>
								<td class="px-3 py-2 font-mono font-bold">v{ch.version}</td>
								<td class="px-3 py-2">{new Date(ch.changed_at).toLocaleString()}</td>
								<td class="px-3 py-2">{ch.reason}</td>
								<td class="px-3 py-2 font-mono text-[10px] max-w-xs truncate">{JSON.stringify(ch.snapshot)}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>
	</div>
{/if}

<!-- Add Coverage Modal -->
{#if isCovModalOpen}
	<div class="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-sm">
		<div class="w-full max-w-lg rounded-2xl bg-white p-6 shadow-xl dark:bg-slate-900">
			<h2 class="text-base font-bold text-slate-900 dark:text-white">Add Provider Coverage</h2>
			{#if covError}
				<div class="mt-2 rounded bg-rose-50 p-2 text-xs text-rose-700">{covError}</div>
			{/if}
			<div class="mt-4 space-y-3 text-xs">
				<div>
					<label for="cov-country" class="block font-medium text-slate-700 mb-1">Country *</label>
					<select id="cov-country" bind:value={covCountryId} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="">Select Country</option>
						{#each countries as c}
							<option value={c.id}>{c.iso2} — {c.name}</option>
						{/each}
					</select>
				</div>
				<div>
					<label for="cov-op" class="block font-medium text-slate-700 mb-1">Operator (Optional — leave blank for country-wide)</label>
					<select id="cov-op" bind:value={covOperatorId} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="">All Operators in Country</option>
						{#each operators.filter((o) => !covCountryId || o.country_id === covCountryId) as op}
							<option value={op.id}>{op.name} ({op.mcc}/{op.mnc})</option>
						{/each}
					</select>
				</div>
				<div>
					<label for="cov-status" class="block font-medium text-slate-700 mb-1">Coverage Status</label>
					<select id="cov-status" bind:value={covStatus} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white">
						<option value="declared">Declared</option>
						<option value="tested">Tested</option>
						<option value="active">Active</option>
						<option value="suspended">Suspended</option>
					</select>
				</div>
				<div>
					<label for="cov-notes" class="block font-medium text-slate-700 mb-1">Notes</label>
					<input id="cov-notes" type="text" bind:value={covNotes} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
				<div>
					<label for="cov-reason" class="block font-medium text-slate-700 mb-1">Audit Reason *</label>
					<input id="cov-reason" type="text" bind:value={covReason} class="w-full rounded-lg border border-slate-300 px-3 py-2 dark:border-slate-700 dark:bg-slate-800 dark:text-white" />
				</div>
			</div>
			<div class="mt-5 flex justify-end gap-2">
				<button type="button" onclick={() => isCovModalOpen = false} class="rounded-lg border border-slate-300 px-3 py-1.5 text-xs font-semibold text-slate-700">Cancel</button>
				<button
					type="button"
					disabled={isSubmittingCov || !covCountryId}
					onclick={submitCoverage}
					class="rounded-lg bg-indigo-600 px-4 py-1.5 text-xs font-semibold text-white hover:bg-indigo-500 disabled:opacity-50"
				>
					Save Coverage
				</button>
			</div>
		</div>
	</div>
{/if}
