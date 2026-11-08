import type {
	ApiErrorBody,
	ApiSuccess,
	AuditListResult,
	BulkLookupResult,
	ChangeRequest,
	ChangeRequestListResult,
	Country,
	CountryHistory,
	CountryListResult,
	CreateCountryRequest,
	CreateOperatorRequest,
	CreatePrefixRequest,
	CreateTransformRuleRequest,
	ImportJob,
	ImportJobListResult,
	ImportJobRowsResult,
	KafkaPublishResult,
	LiveStatus,
	LoginHistoryItem,
	LoginResponse,
	MFASetupResponse,
	NumberLookupResult,
	NumberPrefix,
	Operator,
	OperatorHistory,
	OperatorListResult,
	Organization,
	OrgListResult,
	PaginatedResult,
	Permission,
	PrefixHistory,
	PrefixListResult,
	ReadyReport,
	Role,
	SessionInfo,
	SystemInfo,
	TenantContext,
	TransformChainResult,
	TransformPreviewResult,
	TransformRule,
	TransformRuleListResult,
	UpdateCountryRequest,
	UpdateOperatorRequest,
	UpdatePrefixRequest,
	Provider,
	ProviderHistory,
	ProviderMutationResponse,
	ProviderConnection,
	ProviderConnectionHistory,
	ConnectionMutationResponse,
	ProviderCoverage,
	ProviderCoverageHistory,
	CoverageMutationResponse,
	CustomerProfile,
	CustomerProfileHistory,
	ProfileMutationResponse,
	CustomerAPIKey,
	CreateAPIKeyResponse,
	CustomerSMPPAccount,
	Wallet,
	LedgerTransaction,
	Hold,
	StatementItem,
	ReconciliationReport,
	MessageView,
	MessageDetailView,
	SessionStats,
	QueueBacklogStats,
	UnmatchedDLR,
	SettlementException,
	ReconciliationRun,
	DLRMapping,
	TestSendRequest,
	Invoice,
	Payment,
	CreateSMPPAccountResponse,
	SenderID,
	SenderIDHistory,
	UpdateTransformRuleRequest,
	User,
	RateCard,
	RateCardHistory,
	RateCardMutationResponse,
	Rate,
	RateHistory,
	RateMutationResponse,
	CustomerRateAssignment,
	ResellerCostAssignment,
	AssignmentMutationResponse,
	RateMatch,
	MarginReportItem,
	MarginResult,
	RoutePlan,
	RoutePlanHistory,
	RoutePlanEntry,
	RoutePlanEntryHistory,
	CustomerRouteAssignment,
	CustomerRouteAssignmentHistory,
	RoutingRule,
	RoutingRuleHistory,
	ConnectionOverride,
	Candidate,
	ExclusionReason,
	ResolveResult,
	SimulateResult,
	SnapshotStatus,
	CoverageReportItem
} from '../types/api';

export class ApiClientError extends Error {
	constructor(
		public readonly status: number,
		public readonly code: string,
		message: string,
		public readonly details?: unknown,
		public readonly requestId?: string
	) {
		super(message);
		this.name = 'ApiClientError';
	}
}

// In-memory access token storage (NEVER written to localStorage or sessionStorage)
let accessTokenMemory: string | null = null;
let isRefreshing = false;
let refreshSubscribers: ((token: string | null) => void)[] = [];

function onRefreshed(token: string | null) {
	refreshSubscribers.forEach((cb) => cb(token));
	refreshSubscribers = [];
}

export class ApiClient {
	constructor(private readonly baseUrl: string = '') {}

	setAccessToken(token: string | null) {
		accessTokenMemory = token;
	}

	getAccessToken(): string | null {
		return accessTokenMemory;
	}

	async request<T>(path: string, options: RequestInit = {}, isRetry = false): Promise<{ data: T; requestId: string }> {
		const url = `${this.baseUrl}${path}`;
		const headers = new Headers(options.headers || {});
		headers.set('Accept', 'application/json');
		if (options.body && !headers.has('Content-Type')) {
			headers.set('Content-Type', 'application/json');
		}

		if (accessTokenMemory && !headers.has('Authorization')) {
			headers.set('Authorization', `Bearer ${accessTokenMemory}`);
		}

		let response: Response;
		try {
			response = await fetch(url, {
				...options,
				headers,
				credentials: 'include' // required for SameSite httpOnly cookies
			});
		} catch (err) {
			const cause = err instanceof Error ? err.message : String(err);
			throw new ApiClientError(
				0,
				'network_error',
				`Unable to reach backend server: ${cause}`
			);
		}

		// Handle 401 Unauthorized with automatic silent refresh (only for non-auth endpoints and not during a retry)
		if (response.status === 401 && !isRetry && !path.startsWith('/api/v1/auth/login') && !path.startsWith('/api/v1/auth/refresh')) {
			const waitForToken = new Promise<string | null>((resolve) => {
				refreshSubscribers.push(resolve);
			});

			if (!isRefreshing) {
				isRefreshing = true;
				try {
					const refreshRes = await this.refreshToken();
					if (refreshRes.data.access_token) {
						this.setAccessToken(refreshRes.data.access_token);
						onRefreshed(refreshRes.data.access_token);
					} else {
						this.setAccessToken(null);
						onRefreshed(null);
					}
				} catch {
					this.setAccessToken(null);
					onRefreshed(null);
				} finally {
					isRefreshing = false;
				}
			}

			// Wait for refresh to complete
			const newToken = await waitForToken;

			if (newToken) {
				return this.request<T>(path, options, true);
			}
		}

		const requestIdHeader = response.headers.get('X-Request-ID') || '';
		let responseJson: unknown = null;
		const text = await response.text();

		if (text) {
			try {
				responseJson = JSON.parse(text);
			} catch {
				// Non-JSON response
			}
		}

		if (!response.ok) {
			if (responseJson && typeof responseJson === 'object' && 'error' in responseJson) {
				const errorBody = responseJson as ApiErrorBody;
				const metaReqId = errorBody.meta?.request_id || requestIdHeader;
				throw new ApiClientError(
					response.status,
					errorBody.error?.code || 'request_failed',
					errorBody.error?.message || response.statusText,
					errorBody.error?.details,
					metaReqId
				);
			}

			throw new ApiClientError(
				response.status,
				'http_error',
				response.statusText || `HTTP error ${response.status}`,
				undefined,
				requestIdHeader
			);
		}

		if (responseJson && typeof responseJson === 'object' && 'data' in responseJson) {
			const successBody = responseJson as ApiSuccess<T>;
			return {
				data: successBody.data,
				requestId: successBody.meta?.request_id || requestIdHeader
			};
		}

		return {
			data: responseJson as T,
			requestId: requestIdHeader
		};
	}

	// -------------------------------------------------------------
	// System & Health
	// -------------------------------------------------------------

	async getHealth(): Promise<{ data: LiveStatus; requestId: string }> {
		return this.request<LiveStatus>('/healthz');
	}

	async getReadiness(): Promise<{ report: ReadyReport; requestId: string; isDegraded: boolean }> {
		try {
			const res = await this.request<ReadyReport>('/readyz');
			return {
				report: res.data,
				requestId: res.requestId,
				isDegraded: false
			};
		} catch (err) {
			if (err instanceof ApiClientError && err.status === 503 && err.details && typeof err.details === 'object') {
				const report = err.details as ReadyReport;
				if (report && report.checks) {
					return {
						report,
						requestId: err.requestId || '',
						isDegraded: true
					};
				}
			}
			throw err;
		}
	}

	async getSystemInfo(): Promise<{ data: SystemInfo; requestId: string }> {
		return this.request<SystemInfo>('/api/v1/system/info');
	}

	async publishTestKafkaMessage(message: string): Promise<{ data: KafkaPublishResult; requestId: string }> {
		return this.request<KafkaPublishResult>('/api/v1/dev/kafka/test-message', {
			method: 'POST',
			body: JSON.stringify({ message })
		});
	}

	// -------------------------------------------------------------
	// Phase 2: Authentication
	// -------------------------------------------------------------

	async login(email: string, password: string): Promise<{ data: LoginResponse; requestId: string }> {
		const res = await this.request<LoginResponse>('/api/v1/auth/login', {
			method: 'POST',
			body: JSON.stringify({ email, password })
		});
		if (res.data.access_token) {
			this.setAccessToken(res.data.access_token);
		}
		return res;
	}

	async verifyMFA(challengeToken: string, code: string): Promise<{ data: LoginResponse; requestId: string }> {
		const res = await this.request<LoginResponse>('/api/v1/auth/mfa/verify', {
			method: 'POST',
			body: JSON.stringify({ challenge_token: challengeToken, code })
		});
		if (res.data.access_token) {
			this.setAccessToken(res.data.access_token);
		}
		return res;
	}

	async refreshToken(): Promise<{ data: LoginResponse; requestId: string }> {
		const res = await this.request<LoginResponse>('/api/v1/auth/refresh', {
			method: 'POST'
		});
		if (res.data.access_token) {
			this.setAccessToken(res.data.access_token);
		}
		return res;
	}

	async logout(): Promise<void> {
		try {
			await this.request('/api/v1/auth/logout', { method: 'POST' });
		} finally {
			this.setAccessToken(null);
		}
	}

	async logoutAll(): Promise<void> {
		try {
			await this.request('/api/v1/auth/logout-all', { method: 'POST' });
		} finally {
			this.setAccessToken(null);
		}
	}

	async getMe(): Promise<{ data: { user: User; tenant_context: TenantContext }; requestId: string }> {
		return this.request<{ user: User; tenant_context: TenantContext }>('/api/v1/auth/me');
	}

	async setupMFA(): Promise<{ data: MFASetupResponse; requestId: string }> {
		return this.request<MFASetupResponse>('/api/v1/auth/mfa/setup', { method: 'POST' });
	}

	async confirmMFA(secret: string, code: string, recoveryCodes: string[]): Promise<void> {
		await this.request('/api/v1/auth/mfa/confirm', {
			method: 'POST',
			body: JSON.stringify({ secret, code, recovery_codes: recoveryCodes })
		});
	}

	async changePassword(currentPassword: string, newPassword: string): Promise<void> {
		await this.request('/api/v1/auth/password/change', {
			method: 'POST',
			body: JSON.stringify({ current_password: currentPassword, new_password: newPassword })
		});
	}

	async requestPasswordReset(email: string): Promise<void> {
		await this.request('/api/v1/auth/password/reset-request', {
			method: 'POST',
			body: JSON.stringify({ email })
		});
	}

	async confirmPasswordReset(token: string, newPassword: string): Promise<void> {
		await this.request('/api/v1/auth/password/reset-confirm', {
			method: 'POST',
			body: JSON.stringify({ token, new_password: newPassword })
		});
	}

	async acceptInvitation(token: string, password: string): Promise<void> {
		await this.request('/api/v1/auth/invitation/accept', {
			method: 'POST',
			body: JSON.stringify({ token, password })
		});
	}

	async getSessions(): Promise<{ data: SessionInfo[]; requestId: string }> {
		return this.request<SessionInfo[]>('/api/v1/auth/sessions');
	}

	async revokeSession(sessionId: string): Promise<void> {
		await this.request(`/api/v1/auth/sessions/${sessionId}`, { method: 'DELETE' });
	}

	async getLoginHistory(page = 1, pageSize = 50, userId?: string): Promise<{ data: PaginatedResult<LoginHistoryItem>; requestId: string }> {
		let path = `/api/v1/auth/login-history?page=${page}&page_size=${pageSize}`;
		if (userId) path += `&user_id=${encodeURIComponent(userId)}`;
		return this.request<PaginatedResult<LoginHistoryItem>>(path);
	}

	// -------------------------------------------------------------
	// Phase 2: Organizations (Tenancy)
	// -------------------------------------------------------------

	async listOrganizations(params?: { search?: string; type?: string; status?: string; page?: number; pageSize?: number }): Promise<{ data: OrgListResult; requestId: string }> {
		const searchParams = new URLSearchParams();
		if (params?.search) searchParams.set('search', params.search);
		if (params?.type) searchParams.set('type', params.type);
		if (params?.status) searchParams.set('status', params.status);
		if (params?.page) searchParams.set('page', String(params.page));
		if (params?.pageSize) searchParams.set('page_size', String(params.pageSize));

		const query = searchParams.toString();
		return this.request<OrgListResult>(`/api/v1/organizations${query ? `?${query}` : ''}`);
	}

	async getOrganization(id: string): Promise<{ data: Organization; requestId: string }> {
		return this.request<Organization>(`/api/v1/organizations/${id}`);
	}

	async createOrganization(body: { name: string; type: 'reseller' | 'customer'; parent_id?: string }): Promise<{ data: Organization; requestId: string }> {
		return this.request<Organization>('/api/v1/organizations', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async updateOrganization(id: string, name: string): Promise<{ data: Organization; requestId: string }> {
		return this.request<Organization>(`/api/v1/organizations/${id}`, {
			method: 'PUT',
			body: JSON.stringify({ name })
		});
	}

	async suspendOrganization(id: string): Promise<void> {
		await this.request(`/api/v1/organizations/${id}/suspend`, { method: 'POST' });
	}

	async activateOrganization(id: string): Promise<void> {
		await this.request(`/api/v1/organizations/${id}/activate`, { method: 'POST' });
	}

	// -------------------------------------------------------------
	// Phase 2: Users
	// -------------------------------------------------------------

	async listUsers(params?: { search?: string; status?: string; orgId?: string; page?: number; pageSize?: number }): Promise<{ data: PaginatedResult<User>; requestId: string }> {
		const searchParams = new URLSearchParams();
		if (params?.search) searchParams.set('search', params.search);
		if (params?.status) searchParams.set('status', params.status);
		if (params?.orgId) searchParams.set('org_id', params.orgId);
		if (params?.page) searchParams.set('page', String(params.page));
		if (params?.pageSize) searchParams.set('page_size', String(params.pageSize));

		const query = searchParams.toString();
		return this.request<PaginatedResult<User>>(`/api/v1/users${query ? `?${query}` : ''}`);
	}

	async getUser(id: string): Promise<{ data: User; requestId: string }> {
		return this.request<User>(`/api/v1/users/${id}`);
	}

	async inviteUser(body: { email: string; organization_id?: string; role_ids: string[] }): Promise<{ data: User; requestId: string }> {
		return this.request<User>('/api/v1/users/invite', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async suspendUser(id: string, reason?: string): Promise<void> {
		await this.request(`/api/v1/users/${id}/suspend`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async activateUser(id: string): Promise<void> {
		await this.request(`/api/v1/users/${id}/activate`, { method: 'POST' });
	}

	async updateUserRoles(id: string, roleIds: string[]): Promise<void> {
		await this.request(`/api/v1/users/${id}/roles`, {
			method: 'PUT',
			body: JSON.stringify({ role_ids: roleIds })
		});
	}

	// -------------------------------------------------------------
	// Phase 2: Roles & Permissions (RBAC)
	// -------------------------------------------------------------

	async listPermissions(): Promise<{ data: Permission[]; requestId: string }> {
		return this.request<Permission[]>('/api/v1/permissions');
	}

	async listRoles(organizationId?: string): Promise<{ data: Role[]; requestId: string }> {
		let path = '/api/v1/roles';
		if (organizationId) {
			path += `?organization_id=${encodeURIComponent(organizationId)}`;
		}
		return this.request<Role[]>(path);
	}

	async getRole(id: string): Promise<{ data: Role; requestId: string }> {
		return this.request<Role>(`/api/v1/roles/${id}`);
	}

	async createRole(body: { name: string; description?: string; organization_id?: string; permissions: string[] }): Promise<{ data: Role; requestId: string }> {
		return this.request<Role>('/api/v1/roles', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async updateRole(id: string, body: { name: string; description?: string; permissions: string[] }): Promise<{ data: Role; requestId: string }> {
		return this.request<Role>(`/api/v1/roles/${id}`, {
			method: 'PUT',
			body: JSON.stringify(body)
		});
	}

	async cloneRole(id: string, name: string): Promise<{ data: Role; requestId: string }> {
		return this.request<Role>(`/api/v1/roles/${id}/clone`, {
			method: 'POST',
			body: JSON.stringify({ name })
		});
	}

	async deleteRole(id: string): Promise<void> {
		await this.request(`/api/v1/roles/${id}`, { method: 'DELETE' });
	}

	// -------------------------------------------------------------
	// Phase 2: Audit Logs
	// -------------------------------------------------------------

	async listAuditLogs(params?: {
		module?: string;
		action?: string;
		entityType?: string;
		search?: string;
		startDate?: string;
		endDate?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: AuditListResult; requestId: string }> {
		const searchParams = new URLSearchParams();
		if (params?.module) searchParams.set('module', params.module);
		if (params?.action) searchParams.set('action', params.action);
		if (params?.entityType) searchParams.set('entity_type', params.entityType);
		if (params?.search) searchParams.set('search', params.search);
		if (params?.startDate) searchParams.set('start_date', params.startDate);
		if (params?.endDate) searchParams.set('end_date', params.endDate);
		if (params?.page) searchParams.set('page', String(params.page));
		if (params?.pageSize) searchParams.set('page_size', String(params.pageSize));

		const query = searchParams.toString();
		return this.request<AuditListResult>(`/api/v1/audit/logs${query ? `?${query}` : ''}`);
	}

	// -------------------------------------------------------------
	// Phase 3: Countries
	// -------------------------------------------------------------

	async listCountries(params?: {
		search?: string;
		status?: string;
		verification_status?: string;
		continent?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: CountryListResult; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.search) sp.set('search', params.search);
		if (params?.status) sp.set('status', params.status);
		if (params?.verification_status) sp.set('verification_status', params.verification_status);
		if (params?.continent) sp.set('continent', params.continent);
		if (params?.page) sp.set('page', String(params.page));
		if (params?.pageSize) sp.set('page_size', String(params.pageSize));
		const q = sp.toString();
		return this.request<CountryListResult>(`/api/v1/master-data/countries${q ? `?${q}` : ''}`);
	}

	async getCountry(id: string): Promise<{ data: Country; requestId: string }> {
		return this.request<Country>(`/api/v1/master-data/countries/${id}`);
	}

	async createCountry(body: CreateCountryRequest): Promise<{ data: Country | ChangeRequest; requestId: string }> {
		return this.request<Country | ChangeRequest>('/api/v1/master-data/countries', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async updateCountry(id: string, body: UpdateCountryRequest): Promise<{ data: Country | ChangeRequest; requestId: string }> {
		return this.request<Country | ChangeRequest>(`/api/v1/master-data/countries/${id}`, {
			method: 'PUT',
			body: JSON.stringify(body)
		});
	}

	async deactivateCountry(id: string, reason: string): Promise<void> {
		await this.request(`/api/v1/master-data/countries/${id}/deactivate`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async activateCountry(id: string, reason: string): Promise<void> {
		await this.request(`/api/v1/master-data/countries/${id}/activate`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async archiveCountry(id: string, reason: string): Promise<void> {
		await this.request(`/api/v1/master-data/countries/${id}/archive`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async patchCountryStatus(id: string, status: string, reason: string): Promise<void> {
		if (status === 'active') return this.activateCountry(id, reason);
		if (status === 'inactive') return this.deactivateCountry(id, reason);
		if (status === 'archived') return this.archiveCountry(id, reason);
	}

	async getCountryHistory(id: string): Promise<{ data: CountryHistory[]; requestId: string }> {
		return this.request<CountryHistory[]>(`/api/v1/master-data/countries/${id}/history`);
	}

	async rollbackCountry(id: string, version: number, reason: string): Promise<{ data: Country; requestId: string }> {
		return this.request<Country>(`/api/v1/master-data/countries/${id}/rollback`, {
			method: 'POST',
			body: JSON.stringify({ version, reason })
		});
	}

	async exportCountries(formatOrParams: 'csv' | 'json' | object, paramsOrFormat?: 'csv' | 'json' | object): Promise<Blob> {
		let format: 'csv' | 'json' = 'csv';
		let params: object = {};
		if (typeof formatOrParams === 'string') {
			format = formatOrParams as 'csv' | 'json';
			params = (paramsOrFormat as object) || {};
		} else {
			params = formatOrParams || {};
			format = (paramsOrFormat as 'csv' | 'json') || 'csv';
		}
		const sp = new URLSearchParams(params as Record<string, string>);
		sp.set('format', format);
		const url = `/api/v1/master-data/countries/export?${sp.toString()}`;
		const headers = new Headers({ Accept: format === 'csv' ? 'text/csv' : 'application/json' });
		if (accessTokenMemory) headers.set('Authorization', `Bearer ${accessTokenMemory}`);
		const res = await fetch(this.baseUrl + url, { headers, credentials: 'include' });
		if (!res.ok) throw new ApiClientError(res.status, 'export_failed', 'Export failed');
		return res.blob();
	}

	// -------------------------------------------------------------
	// Phase 3: Operators
	// -------------------------------------------------------------

	async listOperators(params?: {
		search?: string;
		status?: string;
		country_id?: string;
		mcc?: string;
		mnc?: string;
		technology?: string;
		verification_status?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: OperatorListResult; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.search) sp.set('search', params.search);
		if (params?.status) sp.set('status', params.status);
		if (params?.country_id) sp.set('country_id', params.country_id);
		if (params?.mcc) sp.set('mcc', params.mcc);
		if (params?.mnc) sp.set('mnc', params.mnc);
		if (params?.technology) sp.set('technology', params.technology);
		if (params?.verification_status) sp.set('verification_status', params.verification_status);
		if (params?.page) sp.set('page', String(params.page));
		if (params?.pageSize) sp.set('page_size', String(params.pageSize));
		const q = sp.toString();
		return this.request<OperatorListResult>(`/api/v1/master-data/operators${q ? `?${q}` : ''}`);
	}

	async getOperator(id: string): Promise<{ data: Operator; requestId: string }> {
		return this.request<Operator>(`/api/v1/master-data/operators/${id}`);
	}

	async createOperator(body: CreateOperatorRequest): Promise<{ data: Operator | ChangeRequest; requestId: string }> {
		return this.request<Operator | ChangeRequest>('/api/v1/master-data/operators', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async updateOperator(id: string, body: UpdateOperatorRequest): Promise<{ data: Operator | ChangeRequest; requestId: string }> {
		return this.request<Operator | ChangeRequest>(`/api/v1/master-data/operators/${id}`, {
			method: 'PUT',
			body: JSON.stringify(body)
		});
	}

	async deactivateOperator(id: string, reason: string): Promise<void> {
		await this.request(`/api/v1/master-data/operators/${id}/deactivate`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async activateOperator(id: string, reason: string): Promise<void> {
		await this.request(`/api/v1/master-data/operators/${id}/activate`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async archiveOperator(id: string, reason: string): Promise<void> {
		await this.request(`/api/v1/master-data/operators/${id}/archive`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async patchOperatorStatus(id: string, status: string, reason: string): Promise<void> {
		if (status === 'active') return this.activateOperator(id, reason);
		if (status === 'inactive') return this.deactivateOperator(id, reason);
		if (status === 'archived') return this.archiveOperator(id, reason);
	}

	async getOperatorHistory(id: string): Promise<{ data: OperatorHistory[]; requestId: string }> {
		return this.request<OperatorHistory[]>(`/api/v1/master-data/operators/${id}/history`);
	}

	async rollbackOperator(id: string, version: number, reason: string): Promise<{ data: Operator; requestId: string }> {
		return this.request<Operator>(`/api/v1/master-data/operators/${id}/rollback`, {
			method: 'POST',
			body: JSON.stringify({ version, reason })
		});
	}

	async exportOperators(formatOrParams: 'csv' | 'json' | object, paramsOrFormat?: 'csv' | 'json' | object): Promise<Blob> {
		let format: 'csv' | 'json' = 'csv';
		let params: object = {};
		if (typeof formatOrParams === 'string') {
			format = formatOrParams as 'csv' | 'json';
			params = (paramsOrFormat as object) || {};
		} else {
			params = formatOrParams || {};
			format = (paramsOrFormat as 'csv' | 'json') || 'csv';
		}
		const sp = new URLSearchParams(params as Record<string, string>);
		sp.set('format', format);
		const url = `/api/v1/master-data/operators/export?${sp.toString()}`;
		const headers = new Headers({ Accept: format === 'csv' ? 'text/csv' : 'application/json' });
		if (accessTokenMemory) headers.set('Authorization', `Bearer ${accessTokenMemory}`);
		const res = await fetch(this.baseUrl + url, { headers, credentials: 'include' });
		if (!res.ok) throw new ApiClientError(res.status, 'export_failed', 'Export failed');
		return res.blob();
	}

	// -------------------------------------------------------------
	// Phase 3: Number Prefixes
	// -------------------------------------------------------------

	async listPrefixes(params?: {
		search?: string;
		prefix?: string;
		status?: string;
		country_id?: string;
		operator_id?: string;
		number_type?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: PrefixListResult; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.search) sp.set('prefix', params.search);
		if (params?.prefix) sp.set('prefix', params.prefix);
		if (params?.status) sp.set('status', params.status);
		if (params?.country_id) sp.set('country_id', params.country_id);
		if (params?.operator_id) sp.set('operator_id', params.operator_id);
		if (params?.number_type) sp.set('number_type', params.number_type);
		if (params?.page) sp.set('page', String(params.page));
		if (params?.pageSize) sp.set('page_size', String(params.pageSize));
		const q = sp.toString();
		return this.request<PrefixListResult>(`/api/v1/master-data/prefixes${q ? `?${q}` : ''}`);
	}

	async getPrefix(id: string): Promise<{ data: NumberPrefix; requestId: string }> {
		return this.request<NumberPrefix>(`/api/v1/master-data/prefixes/${id}`);
	}

	async createPrefix(body: CreatePrefixRequest): Promise<{ data: NumberPrefix | ChangeRequest; requestId: string }> {
		return this.request<NumberPrefix | ChangeRequest>('/api/v1/master-data/prefixes', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async updatePrefix(id: string, body: UpdatePrefixRequest): Promise<{ data: NumberPrefix | ChangeRequest; requestId: string }> {
		return this.request<NumberPrefix | ChangeRequest>(`/api/v1/master-data/prefixes/${id}`, {
			method: 'PUT',
			body: JSON.stringify(body)
		});
	}

	async getPrefixHistory(id: string): Promise<{ data: PrefixHistory[]; requestId: string }> {
		return this.request<PrefixHistory[]>(`/api/v1/master-data/prefixes/${id}/history`);
	}

	async rollbackPrefix(id: string, version: number, reason: string): Promise<{ data: NumberPrefix; requestId: string }> {
		return this.request<NumberPrefix>(`/api/v1/master-data/prefixes/${id}/rollback`, {
			method: 'POST',
			body: JSON.stringify({ version, reason })
		});
	}

	async deactivatePrefix(id: string, reason: string): Promise<void> {
		await this.request(`/api/v1/master-data/prefixes/${id}/deactivate`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async activatePrefix(id: string, reason: string): Promise<void> {
		await this.request(`/api/v1/master-data/prefixes/${id}/activate`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async archivePrefix(id: string, reason: string): Promise<void> {
		await this.request(`/api/v1/master-data/prefixes/${id}/archive`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async patchPrefixStatus(id: string, status: string, reason: string): Promise<void> {
		if (status === 'active') return this.activatePrefix(id, reason);
		if (status === 'inactive') return this.deactivatePrefix(id, reason);
		if (status === 'archived') return this.archivePrefix(id, reason);
	}

	async exportPrefixes(formatOrParams: 'csv' | 'json' | object, paramsOrFormat?: 'csv' | 'json' | object): Promise<Blob> {
		let format: 'csv' | 'json' = 'csv';
		let params: object = {};
		if (typeof formatOrParams === 'string') {
			format = formatOrParams as 'csv' | 'json';
			params = (paramsOrFormat as object) || {};
		} else {
			params = formatOrParams || {};
			format = (paramsOrFormat as 'csv' | 'json') || 'csv';
		}
		const sp = new URLSearchParams(params as Record<string, string>);
		sp.set('format', format);
		const url = `/api/v1/master-data/prefixes/export?${sp.toString()}`;
		const headers = new Headers({ Accept: format === 'csv' ? 'text/csv' : 'application/json' });
		if (accessTokenMemory) headers.set('Authorization', `Bearer ${accessTokenMemory}`);
		const res = await fetch(this.baseUrl + url, { headers, credentials: 'include' });
		if (!res.ok) throw new ApiClientError(res.status, 'export_failed', 'Export failed');
		return res.blob();
	}

	// -------------------------------------------------------------
	// Phase 3: Change Requests (Approval Queue)
	// -------------------------------------------------------------

	async listChangeRequests(params?: {
		entity_type?: string;
		status?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: ChangeRequestListResult; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.entity_type) sp.set('entity_type', params.entity_type);
		if (params?.status) sp.set('status', params.status);
		if (params?.page) sp.set('page', String(params.page));
		if (params?.pageSize) sp.set('page_size', String(params.pageSize));
		const q = sp.toString();
		return this.request<ChangeRequestListResult>(`/api/v1/master-data/change-requests${q ? `?${q}` : ''}`);
	}

	async getChangeRequest(id: string): Promise<{ data: ChangeRequest; requestId: string }> {
		return this.request<ChangeRequest>(`/api/v1/master-data/change-requests/${id}`);
	}

	async approveChangeRequest(id: string, reason?: string): Promise<void> {
		await this.request(`/api/v1/master-data/change-requests/${id}/approve`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async rejectChangeRequest(id: string, reason: string): Promise<void> {
		await this.request(`/api/v1/master-data/change-requests/${id}/reject`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async cancelChangeRequest(id: string): Promise<void> {
		await this.request(`/api/v1/master-data/change-requests/${id}/cancel`, { method: 'POST' });
	}

	// -------------------------------------------------------------
	// Phase 3: Import Jobs
	// -------------------------------------------------------------

	async uploadImportFile(entityType: string, file: File): Promise<{ data: ImportJob; requestId: string }> {
		const formData = new FormData();
		formData.append('file', file);
		formData.append('entity_type', entityType);
		const headers = new Headers();
		if (accessTokenMemory) headers.set('Authorization', `Bearer ${accessTokenMemory}`);
		const res = await fetch(`${this.baseUrl}/api/v1/master-data/import/upload`, {
			method: 'POST',
			headers,
			body: formData,
			credentials: 'include'
		});
		const text = await res.text();
		let json: unknown = null;
		try { json = JSON.parse(text); } catch { /* noop */ }
		if (!res.ok) {
			const e = json as { error?: { code?: string; message?: string }; meta?: { request_id?: string } } | null;
			throw new ApiClientError(res.status, e?.error?.code || 'upload_failed', e?.error?.message || 'Upload failed', undefined, e?.meta?.request_id);
		}
		const s = json as { data: ImportJob; meta?: { request_id?: string } };
		return { data: s.data, requestId: s.meta?.request_id || res.headers.get('X-Request-ID') || '' };
	}

	async getImportJob(id: string): Promise<{ data: ImportJob; requestId: string }> {
		return this.request<ImportJob>(`/api/v1/master-data/import/jobs/${id}`);
	}

	async getImportJobRows(id: string, params?: {
		status?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: ImportJobRowsResult; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.status) sp.set('status', params.status);
		if (params?.page) sp.set('page', String(params.page));
		if (params?.pageSize) sp.set('page_size', String(params.pageSize));
		const q = sp.toString();
		return this.request<ImportJobRowsResult>(`/api/v1/master-data/import/jobs/${id}/rows${q ? `?${q}` : ''}`);
	}

	async confirmImport(id: string, allowPartial: boolean): Promise<{ data: ImportJob; requestId: string }> {
		return this.request<ImportJob>(`/api/v1/master-data/import/jobs/${id}/confirm`, {
			method: 'POST',
			body: JSON.stringify({ allow_partial: allowPartial })
		});
	}

	async rollbackImport(id: string): Promise<void> {
		await this.request(`/api/v1/master-data/import/jobs/${id}/rollback`, { method: 'POST' });
	}

	async listImportJobs(params?: {
		entity_type?: string;
		status?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: ImportJobListResult; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.entity_type) sp.set('entity_type', params.entity_type);
		if (params?.status) sp.set('status', params.status);
		if (params?.page) sp.set('page', String(params.page));
		if (params?.pageSize) sp.set('page_size', String(params.pageSize));
		const q = sp.toString();
		return this.request<ImportJobListResult>(`/api/v1/master-data/import/jobs${q ? `?${q}` : ''}`);
	}

	async downloadImportErrors(id: string): Promise<Blob> {
		const headers = new Headers({ Accept: 'text/csv' });
		if (accessTokenMemory) headers.set('Authorization', `Bearer ${accessTokenMemory}`);
		const res = await fetch(`${this.baseUrl}/api/v1/master-data/import/jobs/${id}/errors`, { headers, credentials: 'include' });
		if (!res.ok) throw new ApiClientError(res.status, 'download_failed', 'Download failed');
		return res.blob();
	}

	// -------------------------------------------------------------
	// Phase 3: Number Lookup
	// -------------------------------------------------------------

	async lookupNumber(number: string, direction?: 'inbound' | 'outbound'): Promise<{ data: NumberLookupResult; requestId: string }> {
		const sp = new URLSearchParams({ number });
		if (direction) sp.set('direction', direction);
		return this.request<NumberLookupResult>(`/api/v1/master-data/lookup?${sp.toString()}`);
	}

	async bulkLookupNumbers(numbers: string[], direction?: string): Promise<{ data: BulkLookupResult; requestId: string }> {
		return this.request<BulkLookupResult>('/api/v1/master-data/lookup/bulk', {
			method: 'POST',
			body: JSON.stringify({ numbers, direction })
		});
	}

	// -------------------------------------------------------------
	// Phase 3: Transform Rules
	// -------------------------------------------------------------

	async listTransformRules(params?: {
		scope?: string;
		status?: string;
		country_id?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: TransformRuleListResult; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.scope) sp.set('scope', params.scope);
		if (params?.status) sp.set('status', params.status);
		if (params?.country_id) sp.set('country_id', params.country_id);
		if (params?.page) sp.set('page', String(params.page));
		if (params?.pageSize) sp.set('page_size', String(params.pageSize));
		const q = sp.toString();
		return this.request<TransformRuleListResult>(`/api/v1/master-data/transform-rules${q ? `?${q}` : ''}`);
	}

	async createTransformRule(body: CreateTransformRuleRequest): Promise<{ data: TransformRule; requestId: string }> {
		return this.request<TransformRule>('/api/v1/master-data/transform-rules', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async updateTransformRule(id: string, body: UpdateTransformRuleRequest): Promise<{ data: TransformRule; requestId: string }> {
		return this.request<TransformRule>(`/api/v1/master-data/transform-rules/${id}`, {
			method: 'PUT',
			body: JSON.stringify(body)
		});
	}

	async deleteTransformRule(id: string): Promise<void> {
		await this.request(`/api/v1/master-data/transform-rules/${id}`, { method: 'DELETE' });
	}

	async previewTransformRule(ruleId: string, number: string): Promise<{ data: TransformPreviewResult; requestId: string }> {
		return this.request<TransformPreviewResult>(`/api/v1/master-data/transform-rules/${ruleId}/preview`, {
			method: 'POST',
			body: JSON.stringify({ number })
		});
	}

	async testTransformRuleChain(number: string, direction: string): Promise<{ data: TransformChainResult; requestId: string }> {
		return this.request<TransformChainResult>('/api/v1/master-data/transform-rules/test-chain', {
			method: 'POST',
			body: JSON.stringify({ number, direction })
		});
	}

	// -------------------------------------------------------------
	// Phase 4: Providers, Connections, Coverage
	// -------------------------------------------------------------

	async listProviders(params?: {
		search?: string;
		provider_type?: string;
		status?: string;
		country_id?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: { items: Provider[]; total: number; page: number; page_size: number }; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.search) sp.set('search', params.search);
		if (params?.provider_type) sp.set('provider_type', params.provider_type);
		if (params?.status) sp.set('status', params.status);
		if (params?.country_id) sp.set('country_id', params.country_id);
		if (params?.page) sp.set('page', String(params.page));
		if (params?.pageSize) sp.set('page_size', String(params.pageSize));
		const q = sp.toString();
		return this.request<{ items: Provider[]; total: number; page: number; page_size: number }>(`/api/v1/providers${q ? `?${q}` : ''}`);
	}

	async getProvider(id: string): Promise<{ data: Provider; requestId: string }> {
		return this.request<Provider>(`/api/v1/providers/${id}`);
	}

	async createProvider(body: Record<string, unknown>): Promise<{ data: ProviderMutationResponse; requestId: string }> {
		return this.request<ProviderMutationResponse>('/api/v1/providers', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async updateProvider(id: string, body: Record<string, unknown>): Promise<{ data: ProviderMutationResponse; requestId: string }> {
		return this.request<ProviderMutationResponse>(`/api/v1/providers/${id}`, {
			method: 'PUT',
			body: JSON.stringify(body)
		});
	}

	async getProviderHistory(id: string): Promise<{ data: ProviderHistory[]; requestId: string }> {
		return this.request<ProviderHistory[]>(`/api/v1/providers/${id}/history`);
	}

	async rollbackProvider(id: string, version: number, reason: string): Promise<{ data: ProviderMutationResponse; requestId: string }> {
		return this.request<ProviderMutationResponse>(`/api/v1/providers/${id}/rollback`, {
			method: 'POST',
			body: JSON.stringify({ version, reason })
		});
	}

	async exportProviders(format: 'csv' | 'json' = 'csv'): Promise<Blob> {
		const headers = new Headers({ Accept: format === 'csv' ? 'text/csv' : 'application/json' });
		if (accessTokenMemory) headers.set('Authorization', `Bearer ${accessTokenMemory}`);
		const res = await fetch(`${this.baseUrl}/api/v1/providers/export?format=${format}`, { headers, credentials: 'include' });
		if (!res.ok) throw new ApiClientError(res.status, 'export_failed', 'Export failed');
		return res.blob();
	}

	async listProviderConnections(providerId: string): Promise<{ data: ProviderConnection[]; requestId: string }> {
		return this.request<ProviderConnection[]>(`/api/v1/providers/${providerId}/connections`);
	}

	async createProviderConnection(providerId: string, body: Record<string, unknown>): Promise<{ data: ConnectionMutationResponse; requestId: string }> {
		return this.request<ConnectionMutationResponse>(`/api/v1/providers/${providerId}/connections`, {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async updateProviderConnection(providerId: string, connId: string, body: Record<string, unknown>): Promise<{ data: ConnectionMutationResponse; requestId: string }> {
		return this.request<ConnectionMutationResponse>(`/api/v1/providers/${providerId}/connections/${connId}`, {
			method: 'PUT',
			body: JSON.stringify(body)
		});
	}

	async updateProviderConnectionSecret(providerId: string, connId: string, body: { smpp_password?: string; http_credentials?: string; reason: string }): Promise<{ data: ConnectionMutationResponse; requestId: string }> {
		return this.request<ConnectionMutationResponse>(`/api/v1/providers/${providerId}/connections/${connId}/secret`, {
			method: 'PUT',
			body: JSON.stringify(body)
		});
	}

	async changeProviderConnectionStatus(providerId: string, connId: string, status: string, reason: string): Promise<{ data: ConnectionMutationResponse; requestId: string }> {
		return this.request<ConnectionMutationResponse>(`/api/v1/providers/${providerId}/connections/${connId}/status`, {
			method: 'POST',
			body: JSON.stringify({ status, reason })
		});
	}

	async getProviderConnectionHistory(providerId: string, connId: string): Promise<{ data: ProviderConnectionHistory[]; requestId: string }> {
		return this.request<ProviderConnectionHistory[]>(`/api/v1/providers/${providerId}/connections/${connId}/history`);
	}

	async rollbackProviderConnection(providerId: string, connId: string, version: number, reason: string): Promise<{ data: ConnectionMutationResponse; requestId: string }> {
		return this.request<ConnectionMutationResponse>(`/api/v1/providers/${providerId}/connections/${connId}/rollback`, {
			method: 'POST',
			body: JSON.stringify({ version, reason })
		});
	}

	async listProviderCoverage(params?: {
		provider_id?: string;
		country_id?: string;
		operator_id?: string;
		status?: string;
		search?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: { items: ProviderCoverage[]; total: number; page: number; page_size: number }; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.provider_id) sp.set('provider_id', params.provider_id);
		if (params?.country_id) sp.set('country_id', params.country_id);
		if (params?.operator_id) sp.set('operator_id', params.operator_id);
		if (params?.status) sp.set('status', params.status);
		if (params?.search) sp.set('search', params.search);
		if (params?.page) sp.set('page', String(params.page));
		if (params?.pageSize) sp.set('page_size', String(params.pageSize));
		const q = sp.toString();
		return this.request<{ items: ProviderCoverage[]; total: number; page: number; page_size: number }>(`/api/v1/provider-coverage${q ? `?${q}` : ''}`);
	}

	async createProviderCoverage(body: Record<string, unknown>): Promise<{ data: CoverageMutationResponse; requestId: string }> {
		return this.request<CoverageMutationResponse>('/api/v1/provider-coverage', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async updateProviderCoverage(covId: string, body: Record<string, unknown>): Promise<{ data: CoverageMutationResponse; requestId: string }> {
		return this.request<CoverageMutationResponse>(`/api/v1/provider-coverage/${covId}`, {
			method: 'PUT',
			body: JSON.stringify(body)
		});
	}

	async getProviderCoverageHistory(covId: string): Promise<{ data: ProviderCoverageHistory[]; requestId: string }> {
		return this.request<ProviderCoverageHistory[]>(`/api/v1/provider-coverage/${covId}/history`);
	}

	async rollbackProviderCoverage(covId: string, version: number, reason: string): Promise<{ data: CoverageMutationResponse; requestId: string }> {
		return this.request<CoverageMutationResponse>(`/api/v1/provider-coverage/${covId}/rollback`, {
			method: 'POST',
			body: JSON.stringify({ version, reason })
		});
	}

	async exportProviderCoverage(format: 'csv' | 'json' = 'csv'): Promise<Blob> {
		const headers = new Headers({ Accept: format === 'csv' ? 'text/csv' : 'application/json' });
		if (accessTokenMemory) headers.set('Authorization', `Bearer ${accessTokenMemory}`);
		const res = await fetch(`${this.baseUrl}/api/v1/provider-coverage/export?format=${format}`, { headers, credentials: 'include' });
		if (!res.ok) throw new ApiClientError(res.status, 'export_failed', 'Export failed');
		return res.blob();
	}

	// -------------------------------------------------------------
	// Phase 4: Customer Commercial Profiles, API Keys, SMPP Accounts, Sender IDs
	// -------------------------------------------------------------

	async listCustomerProfiles(params?: {
		search?: string;
		status?: string;
		account_type?: string;
		organization_id?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: { items: CustomerProfile[]; total: number; page: number; page_size: number }; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.search) sp.set('search', params.search);
		if (params?.status) sp.set('status', params.status);
		if (params?.account_type) sp.set('account_type', params.account_type);
		if (params?.organization_id) sp.set('organization_id', params.organization_id);
		if (params?.page) sp.set('page', String(params.page));
		if (params?.pageSize) sp.set('page_size', String(params.pageSize));
		const q = sp.toString();
		return this.request<{ items: CustomerProfile[]; total: number; page: number; page_size: number }>(`/api/v1/customer-profiles${q ? `?${q}` : ''}`);
	}

	async createCustomerProfile(body: Record<string, unknown>): Promise<{ data: ProfileMutationResponse; requestId: string }> {
		return this.request<ProfileMutationResponse>('/api/v1/customer-profiles', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async updateCustomerProfile(id: string, body: Record<string, unknown>): Promise<{ data: ProfileMutationResponse; requestId: string }> {
		return this.request<ProfileMutationResponse>(`/api/v1/customer-profiles/${id}`, {
			method: 'PUT',
			body: JSON.stringify(body)
		});
	}

	async getCustomerProfileHistory(id: string): Promise<{ data: CustomerProfileHistory[]; requestId: string }> {
		return this.request<CustomerProfileHistory[]>(`/api/v1/customer-profiles/${id}/history`);
	}

	async rollbackCustomerProfile(id: string, version: number, reason: string): Promise<{ data: ProfileMutationResponse; requestId: string }> {
		return this.request<ProfileMutationResponse>(`/api/v1/customer-profiles/${id}/rollback`, {
			method: 'POST',
			body: JSON.stringify({ version, reason })
		});
	}

	async listCustomerAPIKeys(organizationId?: string): Promise<{ data: CustomerAPIKey[]; requestId: string }> {
		const q = organizationId ? `?organization_id=${encodeURIComponent(organizationId)}` : '';
		return this.request<CustomerAPIKey[]>(`/api/v1/customer-api-keys${q}`);
	}

	async createCustomerAPIKey(body: Record<string, unknown>): Promise<{ data: CreateAPIKeyResponse; requestId: string }> {
		return this.request<CreateAPIKeyResponse>('/api/v1/customer-api-keys', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async revokeCustomerAPIKey(id: string, reason: string): Promise<{ data: CustomerAPIKey; requestId: string }> {
		return this.request<CustomerAPIKey>(`/api/v1/customer-api-keys/${id}/revoke`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async rotateCustomerAPIKey(id: string, label?: string, reason?: string): Promise<{ data: CreateAPIKeyResponse; requestId: string }> {
		return this.request<CreateAPIKeyResponse>(`/api/v1/customer-api-keys/${id}/rotate`, {
			method: 'POST',
			body: JSON.stringify({ label, name: label, reason })
		});
	}

	async listCustomerSMPPAccounts(organizationId?: string): Promise<{ data: CustomerSMPPAccount[]; requestId: string }> {
		const q = organizationId ? `?organization_id=${encodeURIComponent(organizationId)}` : '';
		return this.request<CustomerSMPPAccount[]>(`/api/v1/customer-smpp-accounts${q}`);
	}

	async createCustomerSMPPAccount(body: Record<string, unknown>): Promise<{ data: CreateSMPPAccountResponse; requestId: string }> {
		return this.request<CreateSMPPAccountResponse>('/api/v1/customer-smpp-accounts', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async updateCustomerSMPPAccount(id: string, body: Record<string, unknown>): Promise<{ data: CustomerSMPPAccount; requestId: string }> {
		return this.request<CustomerSMPPAccount>(`/api/v1/customer-smpp-accounts/${id}`, {
			method: 'PUT',
			body: JSON.stringify(body)
		});
	}

	async resetCustomerSMPPPassword(id: string, reason: string): Promise<{ data: CreateSMPPAccountResponse; requestId: string }> {
		return this.request<CreateSMPPAccountResponse>(`/api/v1/customer-smpp-accounts/${id}/reset-password`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async revokeCustomerSMPPAccount(id: string, reason: string): Promise<{ data: CustomerSMPPAccount; requestId: string }> {
		return this.request<CustomerSMPPAccount>(`/api/v1/customer-smpp-accounts/${id}/revoke`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async listSenderIDs(params?: {
		organization_id?: string;
		country_id?: string;
		sender_type?: string;
		status?: string;
		search?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: { items: SenderID[]; total: number; page: number; page_size: number }; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.organization_id) sp.set('organization_id', params.organization_id);
		if (params?.country_id) sp.set('country_id', params.country_id);
		if (params?.sender_type) sp.set('sender_type', params.sender_type);
		if (params?.status) sp.set('status', params.status);
		if (params?.search) sp.set('search', params.search);
		if (params?.page) sp.set('page', String(params.page));
		if (params?.pageSize) sp.set('page_size', String(params.pageSize));
		const q = sp.toString();
		return this.request<{ items: SenderID[]; total: number; page: number; page_size: number }>(`/api/v1/sender-ids${q ? `?${q}` : ''}`);
	}

	async createSenderID(body: Record<string, unknown>): Promise<{ data: SenderID; requestId: string }> {
		return this.request<SenderID>('/api/v1/sender-ids', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async approveSenderID(id: string, notes: string, reason: string): Promise<{ data: SenderID; requestId: string }> {
		return this.request<SenderID>(`/api/v1/sender-ids/${id}/approve`, {
			method: 'POST',
			body: JSON.stringify({ notes, reason })
		});
	}

	async rejectSenderID(id: string, reason: string): Promise<{ data: SenderID; requestId: string }> {
		return this.request<SenderID>(`/api/v1/sender-ids/${id}/reject`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async getSenderIDHistory(id: string): Promise<{ data: SenderIDHistory[]; requestId: string }> {
		return this.request<SenderIDHistory[]>(`/api/v1/sender-ids/${id}/history`);
	}

	async rollbackSenderID(id: string, version: number, reason: string): Promise<{ data: SenderID; requestId: string }> {
		return this.request<SenderID>(`/api/v1/sender-ids/${id}/rollback`, {
			method: 'POST',
			body: JSON.stringify({ version, reason })
		});
	}

	async exportSenderIDs(format: 'csv' | 'json' = 'csv'): Promise<Blob> {
		const headers = new Headers({ Accept: format === 'csv' ? 'text/csv' : 'application/json' });
		if (accessTokenMemory) headers.set('Authorization', `Bearer ${accessTokenMemory}`);
		const res = await fetch(`${this.baseUrl}/api/v1/sender-ids/export?format=${format}`, { headers, credentials: 'include' });
		if (!res.ok) throw new ApiClientError(res.status, 'export_failed', 'Export failed');
		return res.blob();
	}

	// -------------------------------------------------------------
	// Phase 5: Rating Engine & Rate Cards
	// -------------------------------------------------------------

	async listRateCards(params?: {
		kind?: string;
		status?: string;
		owner_org_id?: string;
		provider_id?: string;
		currency_code?: string;
		search?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: { items: RateCard[]; total: number; page: number; page_size: number }; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.kind) sp.set('kind', params.kind);
		if (params?.status) sp.set('status', params.status);
		if (params?.owner_org_id) sp.set('owner_org_id', params.owner_org_id);
		if (params?.provider_id) sp.set('provider_id', params.provider_id);
		if (params?.currency_code) sp.set('currency_code', params.currency_code);
		if (params?.search) sp.set('search', params.search);
		if (params?.page) sp.set('page', String(params.page));
		if (params?.pageSize) sp.set('page_size', String(params.pageSize));
		const q = sp.toString();
		return this.request<{ items: RateCard[]; total: number; page: number; page_size: number }>(`/api/v1/rates/cards${q ? `?${q}` : ''}`);
	}

	async getRateCard(id: string): Promise<{ data: RateCard; requestId: string }> {
		return this.request<RateCard>(`/api/v1/rates/cards/${id}`);
	}

	async createRateCard(body: Record<string, unknown>): Promise<{ data: RateCardMutationResponse; requestId: string }> {
		return this.request<RateCardMutationResponse>('/api/v1/rates/cards', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async updateRateCard(id: string, body: Record<string, unknown>): Promise<{ data: RateCardMutationResponse; requestId: string }> {
		return this.request<RateCardMutationResponse>(`/api/v1/rates/cards/${id}`, {
			method: 'PUT',
			body: JSON.stringify(body)
		});
	}

	async archiveRateCard(id: string, reason: string): Promise<{ data: RateCard; requestId: string }> {
		return this.request<RateCard>(`/api/v1/rates/cards/${id}/archive`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async getRateCardHistory(id: string): Promise<{ data: RateCardHistory[]; requestId: string }> {
		return this.request<RateCardHistory[]>(`/api/v1/rates/cards/${id}/history`);
	}

	async rollbackRateCard(id: string, version: number, reason: string): Promise<{ data: RateCardMutationResponse; requestId: string }> {
		return this.request<RateCardMutationResponse>(`/api/v1/rates/cards/${id}/rollback`, {
			method: 'POST',
			body: JSON.stringify({ version, reason })
		});
	}

	async listRates(params: {
		card_id: string;
		country_id?: string;
		operator_id?: string;
		message_type?: string;
		status?: string;
		search?: string;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: { items: Rate[]; total: number; page: number; page_size: number }; requestId: string }> {
		const sp = new URLSearchParams();
		sp.set('card_id', params.card_id);
		if (params.country_id) sp.set('country_id', params.country_id);
		if (params.operator_id) sp.set('operator_id', params.operator_id);
		if (params.message_type) sp.set('message_type', params.message_type);
		if (params.status) sp.set('status', params.status);
		if (params.search) sp.set('search', params.search);
		if (params.page) sp.set('page', String(params.page));
		if (params.pageSize) sp.set('page_size', String(params.pageSize));
		return this.request<{ items: Rate[]; total: number; page: number; page_size: number }>(`/api/v1/rates?${sp.toString()}`);
	}

	async getRate(id: string): Promise<{ data: Rate; requestId: string }> {
		return this.request<Rate>(`/api/v1/rates/${id}`);
	}

	async createRate(body: Record<string, unknown>): Promise<{ data: RateMutationResponse; requestId: string }> {
		return this.request<RateMutationResponse>('/api/v1/rates', {
			method: 'POST',
			body: JSON.stringify(body)
		});
	}

	async updateRate(id: string, body: Record<string, unknown>): Promise<{ data: RateMutationResponse; requestId: string }> {
		return this.request<RateMutationResponse>(`/api/v1/rates/${id}`, {
			method: 'PUT',
			body: JSON.stringify(body)
		});
	}

	async closeRate(id: string, reason: string): Promise<{ data: Rate; requestId: string }> {
		return this.request<Rate>(`/api/v1/rates/${id}/close`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async getRateHistory(id: string): Promise<{ data: RateHistory[]; requestId: string }> {
		return this.request<RateHistory[]>(`/api/v1/rates/${id}/history`);
	}

	async rollbackRate(id: string, version: number, reason: string): Promise<{ data: RateMutationResponse; requestId: string }> {
		return this.request<RateMutationResponse>(`/api/v1/rates/${id}/rollback`, {
			method: 'POST',
			body: JSON.stringify({ version, reason })
		});
	}

	async listUpcomingRates(days: number = 30): Promise<{ data: Rate[]; requestId: string }> {
		return this.request<Rate[]>(`/api/v1/rates/upcoming?days=${days}`);
	}

	async listCustomerAssignments(customerOrgId?: string): Promise<{ data: CustomerRateAssignment[]; requestId: string }> {
		const q = customerOrgId ? `?target_org_id=${customerOrgId}` : '';
		return this.request<CustomerRateAssignment[]>(`/api/v1/rates/assignments${q}`);
	}

	async createCustomerAssignment(body: Record<string, unknown>): Promise<{ data: AssignmentMutationResponse; requestId: string }> {
		return this.request<AssignmentMutationResponse>('/api/v1/rates/assignments', {
			method: 'POST',
			body: JSON.stringify({ ...body, is_reseller: false })
		});
	}

	async listResellerCostAssignments(resellerOrgId?: string): Promise<{ data: ResellerCostAssignment[]; requestId: string }> {
		const q = resellerOrgId ? `&target_org_id=${resellerOrgId}` : '';
		return this.request<ResellerCostAssignment[]>(`/api/v1/rates/assignments?is_reseller=true${q}`);
	}

	async createResellerCostAssignment(body: Record<string, unknown>): Promise<{ data: AssignmentMutationResponse; requestId: string }> {
		return this.request<AssignmentMutationResponse>('/api/v1/rates/assignments', {
			method: 'POST',
			body: JSON.stringify({ ...body, is_reseller: true })
		});
	}

	async deactivateAssignment(id: string, isCustomer: boolean, reason: string): Promise<{ data: { status: string; pending_change?: ChangeRequest }; requestId: string }> {
		return this.request<{ status: string; pending_change?: ChangeRequest }>(`/api/v1/rates/assignments/${id}/deactivate`, {
			method: 'POST',
			body: JSON.stringify({ is_customer: isCustomer, reason })
		});
	}

	async getAssignmentHistory(id: string): Promise<{ data: Record<string, unknown>[]; requestId: string }> {
		return this.request<Record<string, unknown>[]>(`/api/v1/rates/assignments/${id}/history`);
	}

	async checkMargin(params: {
		card_id: string;
		country_id: string;
		operator_id?: string;
		message_type?: string;
		sell_price: string;
		currency: string;
	}): Promise<{ data: MarginResult; requestId: string }> {
		const sp = new URLSearchParams();
		sp.set('card_id', params.card_id);
		sp.set('country_id', params.country_id);
		if (params.operator_id) sp.set('operator_id', params.operator_id);
		if (params.message_type) sp.set('message_type', params.message_type);
		sp.set('sell_price', params.sell_price);
		sp.set('currency', params.currency);
		return this.request<MarginResult>(`/api/v1/rates/margin-check?${sp.toString()}`);
	}

	async getMarginReport(params?: {
		card_id?: string;
		country_id?: string;
		deficit?: boolean;
		page?: number;
		pageSize?: number;
	}): Promise<{ data: { items: MarginReportItem[]; total_count: number; page: number; page_size: number }; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.card_id) sp.set('card_id', params.card_id);
		if (params?.country_id) sp.set('country_id', params.country_id);
		if (params?.deficit) sp.set('deficit', 'true');
		if (params?.page) sp.set('page', String(params.page));
		if (params?.pageSize) sp.set('page_size', String(params.pageSize));
		const q = sp.toString();
		return this.request<{ items: MarginReportItem[]; total_count: number; page: number; page_size: number }>(`/api/v1/rates/margin-report${q ? `?${q}` : ''}`);
	}

	async lookupRate(params: {
		customer_org_id?: string;
		country_id: string;
		operator_id?: string;
		message_type?: string;
		at?: string;
	}): Promise<{ data: RateMatch; requestId: string }> {
		const sp = new URLSearchParams();
		if (params.customer_org_id) sp.set('customer', params.customer_org_id);
		sp.set('country', params.country_id);
		if (params.operator_id) sp.set('operator', params.operator_id);
		if (params.message_type) sp.set('msg_type', params.message_type);
		if (params.at) sp.set('at', params.at);
		return this.request<RateMatch>(`/api/v1/rates/lookup?${sp.toString()}`);
	}

	async lookupPhoneNumberRate(params: {
		phone_number: string;
		customer_org_id?: string;
		message_type?: string;
		at?: string;
	}): Promise<{ data: RateMatch; requestId: string }> {
		const sp = new URLSearchParams();
		sp.set('number', params.phone_number);
		if (params.customer_org_id) sp.set('customer', params.customer_org_id);
		if (params.message_type) sp.set('msg_type', params.message_type);
		if (params.at) sp.set('at', params.at);
		return this.request<RateMatch>(`/api/v1/rates/lookup-by-number?${sp.toString()}`);
	}

	async getMyRates(): Promise<{ data: { card: RateCard; rates: Rate[] }; requestId: string }> {
		return this.request<{ card: RateCard; rates: Rate[] }>('/api/v1/rates/my-rates');
	}

	async getMyCostCard(): Promise<{ data: { card: RateCard; rates: Rate[] }; requestId: string }> {
		return this.request<{ card: RateCard; rates: Rate[] }>('/api/v1/rates/my-cost');
	}

	async exportRates(cardId: string, format: 'csv' | 'json' = 'csv'): Promise<Blob> {
		const headers = new Headers({ Accept: format === 'csv' ? 'text/csv' : 'application/json' });
		if (accessTokenMemory) headers.set('Authorization', `Bearer ${accessTokenMemory}`);
		const res = await fetch(`${this.baseUrl}/api/v1/rates/export?card_id=${cardId}&format=${format}`, { headers, credentials: 'include' });
		if (!res.ok) throw new ApiClientError(res.status, 'export_failed', 'Export failed');
		return res.blob();
	}

	// -------------------------------------------------------------------------
	// Phase 6: Wallets & Ledger
	// -------------------------------------------------------------------------

	async listWallets(params?: { search?: string; status?: string; page?: number; page_size?: number }): Promise<{
		data: { items: Wallet[]; total: number; page: number; page_size: number };
		requestId: string;
	}> {
		const sp = new URLSearchParams();
		if (params?.search) sp.set('search', params.search);
		if (params?.status) sp.set('status', params.status);
		if (params?.page) sp.set('page', params.page.toString());
		if (params?.page_size) sp.set('page_size', params.page_size.toString());
		return this.request<{ items: Wallet[]; total: number; page: number; page_size: number }>(`/api/v1/wallets?${sp.toString()}`);
	}

	async getMyWallet(): Promise<{ data: Wallet; requestId: string }> {
		return this.request<Wallet>('/api/v1/wallets/my');
	}

	async getWallet(id: string): Promise<{ data: Wallet; requestId: string }> {
		return this.request<Wallet>(`/api/v1/wallets/${id}`);
	}

	async topupWallet(id: string, payload: { amount: string; reason: string; reference?: string; idempotency_key?: string }): Promise<{
		data: { transaction?: LedgerTransaction; change_request?: ChangeRequest; pending_approval: boolean };
		requestId: string;
	}> {
		return this.request<{ transaction?: LedgerTransaction; change_request?: ChangeRequest; pending_approval: boolean }>(
			`/api/v1/wallets/${id}/topup`,
			{ method: 'POST', body: JSON.stringify(payload) }
		);
	}

	async adjustWallet(id: string, payload: { amount: string; direction: 'credit' | 'debit'; reason: string; reference?: string; idempotency_key?: string }): Promise<{
		data: { transaction?: LedgerTransaction; change_request?: ChangeRequest; pending_approval: boolean };
		requestId: string;
	}> {
		return this.request<{ transaction?: LedgerTransaction; change_request?: ChangeRequest; pending_approval: boolean }>(
			`/api/v1/wallets/${id}/adjust`,
			{ method: 'POST', body: JSON.stringify(payload) }
		);
	}

	async freezeWallet(id: string, reason: string): Promise<{ data: { status: string }; requestId: string }> {
		return this.request<{ status: string }>(`/api/v1/wallets/${id}/freeze`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async unfreezeWallet(id: string, reason: string): Promise<{
		data: { change_request?: ChangeRequest; pending_approval: boolean; status?: string };
		requestId: string;
	}> {
		return this.request<{ change_request?: ChangeRequest; pending_approval: boolean; status?: string }>(
			`/api/v1/wallets/${id}/unfreeze`,
			{ method: 'POST', body: JSON.stringify({ reason }) }
		);
	}

	async transferWalletFunds(payload: { destination_wallet_id: string; amount: string; reason: string; idempotency_key?: string }): Promise<{
		data: LedgerTransaction;
		requestId: string;
	}> {
		return this.request<LedgerTransaction>('/api/v1/wallets/transfer', {
			method: 'POST',
			body: JSON.stringify(payload)
		});
	}

	async listWalletHolds(walletId: string, status?: string): Promise<{ data: Hold[]; requestId: string }> {
		const sp = status ? `?status=${status}` : '';
		return this.request<Hold[]>(`/api/v1/wallets/${walletId}/holds${sp}`);
	}

	async getWalletStatement(walletId: string, params?: { start_date?: string; end_date?: string; page?: number; page_size?: number }): Promise<{
		data: { items: StatementItem[]; total: number; page: number; page_size: number };
		requestId: string;
	}> {
		const sp = new URLSearchParams();
		if (params?.start_date) sp.set('start_date', params.start_date);
		if (params?.end_date) sp.set('end_date', params.end_date);
		if (params?.page) sp.set('page', params.page.toString());
		if (params?.page_size) sp.set('page_size', params.page_size.toString());
		return this.request<{ items: StatementItem[]; total: number; page: number; page_size: number }>(`/api/v1/wallets/${walletId}/statement?${sp.toString()}`);
	}

	async exportWalletStatement(walletId: string, format: 'csv' | 'json' = 'csv', params?: { start_date?: string; end_date?: string }): Promise<Blob> {
		const sp = new URLSearchParams();
		sp.set('format', format);
		if (params?.start_date) sp.set('start_date', params.start_date);
		if (params?.end_date) sp.set('end_date', params.end_date);
		const headers = new Headers({ Accept: format === 'csv' ? 'text/csv' : 'application/json' });
		if (accessTokenMemory) headers.set('Authorization', `Bearer ${accessTokenMemory}`);
		const res = await fetch(`${this.baseUrl}/api/v1/wallets/${walletId}/statement/export?${sp.toString()}`, { headers, credentials: 'include' });
		if (!res.ok) throw new ApiClientError(res.status, 'export_failed', 'Statement export failed');
		return res.blob();
	}

	async listLedgerTransactions(params?: {
		wallet_id?: string;
		type?: string;
		start_date?: string;
		end_date?: string;
		min_amount?: string;
		max_amount?: string;
		page?: number;
		page_size?: number;
	}): Promise<{ data: { items: LedgerTransaction[]; total: number; page: number; page_size: number }; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.wallet_id) sp.set('wallet_id', params.wallet_id);
		if (params?.type) sp.set('type', params.type);
		if (params?.start_date) sp.set('start_date', params.start_date);
		if (params?.end_date) sp.set('end_date', params.end_date);
		if (params?.min_amount) sp.set('min_amount', params.min_amount);
		if (params?.max_amount) sp.set('max_amount', params.max_amount);
		if (params?.page) sp.set('page', params.page.toString());
		if (params?.page_size) sp.set('page_size', params.page_size.toString());
		return this.request<{ items: LedgerTransaction[]; total: number; page: number; page_size: number }>(`/api/v1/ledger/transactions?${sp.toString()}`);
	}

	async getLedgerTransaction(id: string): Promise<{ data: LedgerTransaction; requestId: string }> {
		return this.request<LedgerTransaction>(`/api/v1/ledger/transactions/${id}`);
	}

	async reverseLedgerTransaction(id: string, reason: string): Promise<{ data: LedgerTransaction; requestId: string }> {
		return this.request<LedgerTransaction>(`/api/v1/ledger/transactions/${id}/reverse`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async getReconciliationReport(): Promise<{ data: ReconciliationReport[]; requestId: string }> {
		return this.request<ReconciliationReport[]>('/api/v1/ledger/reconcile');
	}

	async runReconciliationRepair(): Promise<{ data: ReconciliationReport[]; requestId: string }> {
		return this.request<ReconciliationReport[]>('/api/v1/ledger/reconcile', { method: 'POST' });
	}

	// -------------------------------------------------------------------------
	// Phase 6: Billing & Invoices
	// -------------------------------------------------------------------------

	async listInvoices(params?: {
		customer_org_id?: string;
		status?: string;
		currency?: string;
		search?: string;
		page?: number;
		page_size?: number;
	}): Promise<{ data: { items: Invoice[]; total: number; page: number; page_size: number }; requestId: string }> {
		const sp = new URLSearchParams();
		if (params?.customer_org_id) sp.set('customer_org_id', params.customer_org_id);
		if (params?.status) sp.set('status', params.status);
		if (params?.currency) sp.set('currency', params.currency);
		if (params?.search) sp.set('search', params.search);
		if (params?.page) sp.set('page', params.page.toString());
		if (params?.page_size) sp.set('page_size', params.page_size.toString());
		return this.request<{ items: Invoice[]; total: number; page: number; page_size: number }>(`/api/v1/invoices?${sp.toString()}`);
	}

	async getInvoice(id: string): Promise<{ data: Invoice; requestId: string }> {
		return this.request<Invoice>(`/api/v1/invoices/${id}`);
	}

	async getPrintableInvoiceHTML(id: string): Promise<string> {
		const headers = new Headers();
		if (accessTokenMemory) headers.set('Authorization', `Bearer ${accessTokenMemory}`);
		const res = await fetch(`${this.baseUrl}/api/v1/invoices/${id}/printable`, { headers, credentials: 'include' });
		if (!res.ok) throw new ApiClientError(res.status, 'printable_failed', 'Failed to fetch printable invoice');
		return res.text();
	}

	async createDraftInvoice(payload: {
		customer_org_id: string;
		currency: string;
		due_date?: string;
		tax_label?: string;
		tax_amount?: string;
		notes?: string;
		lines: Array<{ description: string; quantity: string; unit_price: string }>;
	}): Promise<{ data: Invoice; requestId: string }> {
		return this.request<Invoice>('/api/v1/invoices', {
			method: 'POST',
			body: JSON.stringify(payload)
		});
	}

	async updateDraftInvoice(id: string, payload: {
		currency?: string;
		due_date?: string;
		tax_label?: string;
		tax_amount?: string;
		notes?: string;
		lines?: Array<{ description: string; quantity: string; unit_price: string }>;
	}): Promise<{ data: Invoice; requestId: string }> {
		return this.request<Invoice>(`/api/v1/invoices/${id}`, {
			method: 'PUT',
			body: JSON.stringify(payload)
		});
	}

	async issueInvoice(id: string): Promise<{ data: Invoice; requestId: string }> {
		return this.request<Invoice>(`/api/v1/invoices/${id}/issue`, { method: 'POST' });
	}

	async voidInvoice(id: string, reason: string): Promise<{
		data: { invoice?: Invoice; change_request?: ChangeRequest; pending_approval: boolean };
		requestId: string;
	}> {
		return this.request<{ invoice?: Invoice; change_request?: ChangeRequest; pending_approval: boolean }>(
			`/api/v1/invoices/${id}/void`,
			{ method: 'POST', body: JSON.stringify({ reason }) }
		);
	}

	async createCreditNote(id: string, reason: string): Promise<{ data: Invoice; requestId: string }> {
		return this.request<Invoice>(`/api/v1/invoices/${id}/credit-note`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async recordPayment(id: string, payload: {
		amount: string;
		payment_method: string;
		reference?: string;
		payment_date?: string;
		notes?: string;
	}): Promise<{ data: Payment; requestId: string }> {
		return this.request<Payment>(`/api/v1/invoices/${id}/payments`, {
			method: 'POST',
			body: JSON.stringify(payload)
		});
	}

	async listPayments(params?: { invoice_id?: string; customer_org_id?: string; page?: number; page_size?: number }): Promise<{
		data: { items: Payment[]; total: number; page: number; page_size: number };
		requestId: string;
	}> {
		const sp = new URLSearchParams();
		if (params?.invoice_id) sp.set('invoice_id', params.invoice_id);
		if (params?.customer_org_id) sp.set('customer_org_id', params.customer_org_id);
		if (params?.page) sp.set('page', params.page.toString());
		if (params?.page_size) sp.set('page_size', params.page_size.toString());
		return this.request<{ items: Payment[]; total: number; page: number; page_size: number }>(`/api/v1/payments?${sp.toString()}`);
	}

	// ============================================================================
	// Phase 7: Routing API
	// ============================================================================

	async listRoutePlans(params?: { search?: string; status?: string; strategy?: string; page?: number; page_size?: number }): Promise<{
		data: { items: RoutePlan[]; total: number; page: number; page_size: number };
		requestId: string;
	}> {
		const sp = new URLSearchParams();
		if (params?.search) sp.set('search', params.search);
		if (params?.status) sp.set('status', params.status);
		if (params?.strategy) sp.set('strategy', params.strategy);
		if (params?.page) sp.set('page', params.page.toString());
		if (params?.page_size) sp.set('page_size', params.page_size.toString());
		return this.request<{ items: RoutePlan[]; total: number; page: number; page_size: number }>(`/api/v1/routing/plans?${sp.toString()}`);
	}

	async getRoutePlan(id: string): Promise<{ data: RoutePlan; requestId: string }> {
		return this.request<RoutePlan>(`/api/v1/routing/plans/${id}`);
	}

	async createRoutePlan(payload: {
		name: string;
		code: string;
		description?: string;
		strategy: string;
		notes?: string;
	}): Promise<{ data: RoutePlan; requestId: string }> {
		return this.request<RoutePlan>('/api/v1/routing/plans', {
			method: 'POST',
			body: JSON.stringify(payload)
		});
	}

	async updateRoutePlan(id: string, payload: {
		name?: string;
		description?: string;
		notes?: string;
	}): Promise<{ data: RoutePlan; requestId: string }> {
		return this.request<RoutePlan>(`/api/v1/routing/plans/${id}`, {
			method: 'PUT',
			body: JSON.stringify(payload)
		});
	}

	async activateRoutePlan(id: string, reason?: string): Promise<{ data: { plan: RoutePlan; change_request?: ChangeRequest }; requestId: string }> {
		return this.request<{ plan: RoutePlan; change_request?: ChangeRequest }>(`/api/v1/routing/plans/${id}/activate`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async deactivateRoutePlan(id: string, reason?: string): Promise<{ data: { plan: RoutePlan; change_request?: ChangeRequest }; requestId: string }> {
		return this.request<{ plan: RoutePlan; change_request?: ChangeRequest }>(`/api/v1/routing/plans/${id}/deactivate`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async archiveRoutePlan(id: string): Promise<{ data: RoutePlan; requestId: string }> {
		return this.request<RoutePlan>(`/api/v1/routing/plans/${id}/archive`, {
			method: 'POST'
		});
	}

	async getRoutePlanHistory(id: string): Promise<{ data: RoutePlanHistory[]; requestId: string }> {
		return this.request<RoutePlanHistory[]>(`/api/v1/routing/plans/${id}/history`);
	}

	async rollbackRoutePlan(id: string, historyId: string, reason: string): Promise<{ data: RoutePlan; requestId: string }> {
		return this.request<RoutePlan>(`/api/v1/routing/plans/${id}/rollback`, {
			method: 'POST',
			body: JSON.stringify({ history_id: historyId, reason })
		});
	}

	// Route Plan Entries
	async listRoutePlanEntries(planId: string): Promise<{ data: RoutePlanEntry[]; requestId: string }> {
		return this.request<RoutePlanEntry[]>(`/api/v1/routing/plans/${planId}/entries`);
	}

	async createRoutePlanEntry(planId: string, payload: {
		connection_id: string;
		scope_type: string;
		scope_country_id?: string;
		scope_operator_id?: string;
		scope_prefix?: string;
		priority: number;
		weight: number;
		min_margin_override?: string;
		allowed_sender_id_types?: string[];
		destination_number_format?: string;
		allow_tested_coverage?: boolean;
		reason?: string;
	}): Promise<{ data: { entry: RoutePlanEntry; change_request?: ChangeRequest }; requestId: string }> {
		return this.request<{ entry: RoutePlanEntry; change_request?: ChangeRequest }>(`/api/v1/routing/plans/${planId}/entries`, {
			method: 'POST',
			body: JSON.stringify(payload)
		});
	}

	async updateRoutePlanEntry(planId: string, entryId: string, payload: {
		priority?: number;
		weight?: number;
		min_margin_override?: string;
		allowed_sender_id_types?: string[];
		destination_number_format?: string;
		allow_tested_coverage?: boolean;
		status?: string;
		reason?: string;
	}): Promise<{ data: { entry: RoutePlanEntry; change_request?: ChangeRequest }; requestId: string }> {
		return this.request<{ entry: RoutePlanEntry; change_request?: ChangeRequest }>(`/api/v1/routing/plans/${planId}/entries/${entryId}`, {
			method: 'PUT',
			body: JSON.stringify(payload)
		});
	}

	async deleteRoutePlanEntry(planId: string, entryId: string, reason?: string): Promise<{ data: { change_request?: ChangeRequest }; requestId: string }> {
		return this.request<{ change_request?: ChangeRequest }>(`/api/v1/routing/plans/${planId}/entries/${entryId}`, {
			method: 'DELETE',
			body: JSON.stringify({ reason })
		});
	}

	async rollbackRoutePlanEntry(planId: string, entryId: string, historyId: string, reason: string): Promise<{ data: RoutePlanEntry; requestId: string }> {
		return this.request<RoutePlanEntry>(`/api/v1/routing/plans/${planId}/entries/${entryId}/rollback`, {
			method: 'POST',
			body: JSON.stringify({ history_id: historyId, reason })
		});
	}

	// Customer Route Assignments
	async listCustomerRouteAssignments(params?: { customer_org_id?: string; plan_id?: string; page?: number; page_size?: number }): Promise<{
		data: { items: CustomerRouteAssignment[]; total: number; page: number; page_size: number };
		requestId: string;
	}> {
		const sp = new URLSearchParams();
		if (params?.customer_org_id) sp.set('customer_org_id', params.customer_org_id);
		if (params?.plan_id) sp.set('plan_id', params.plan_id);
		if (params?.page) sp.set('page', params.page.toString());
		if (params?.page_size) sp.set('page_size', params.page_size.toString());
		return this.request<{ items: CustomerRouteAssignment[]; total: number; page: number; page_size: number }>(`/api/v1/routing/assignments?${sp.toString()}`);
	}

	async createCustomerRouteAssignment(payload: {
		customer_org_id: string;
		plan_id: string;
		country_id?: string;
		priority: number;
		reason?: string;
	}): Promise<{ data: { assignment: CustomerRouteAssignment; change_request?: ChangeRequest }; requestId: string }> {
		return this.request<{ assignment: CustomerRouteAssignment; change_request?: ChangeRequest }>('/api/v1/routing/assignments', {
			method: 'POST',
			body: JSON.stringify(payload)
		});
	}

	async updateCustomerRouteAssignment(id: string, payload: {
		priority?: number;
		status?: string;
		reason?: string;
	}): Promise<{ data: { assignment: CustomerRouteAssignment; change_request?: ChangeRequest }; requestId: string }> {
		return this.request<{ assignment: CustomerRouteAssignment; change_request?: ChangeRequest }>(`/api/v1/routing/assignments/${id}`, {
			method: 'PUT',
			body: JSON.stringify(payload)
		});
	}

	async deleteCustomerRouteAssignment(id: string, reason?: string): Promise<{ data: { change_request?: ChangeRequest }; requestId: string }> {
		return this.request<{ change_request?: ChangeRequest }>(`/api/v1/routing/assignments/${id}`, {
			method: 'DELETE',
			body: JSON.stringify({ reason })
		});
	}

	// Routing Rules
	async listRoutingRules(): Promise<{ data: RoutingRule[]; requestId: string }> {
		return this.request<RoutingRule[]>('/api/v1/routing/rules');
	}

	async createRoutingRule(payload: {
		name: string;
		priority: number;
		conditions: Record<string, any>;
		action_type: string;
		action_connection_id?: string;
		action_block_reason_code?: string;
		reason?: string;
	}): Promise<{ data: { rule: RoutingRule; change_request?: ChangeRequest }; requestId: string }> {
		return this.request<{ rule: RoutingRule; change_request?: ChangeRequest }>('/api/v1/routing/rules', {
			method: 'POST',
			body: JSON.stringify(payload)
		});
	}

	async updateRoutingRule(id: string, payload: {
		name?: string;
		priority?: number;
		status?: string;
		conditions?: Record<string, any>;
		action_type?: string;
		action_connection_id?: string;
		action_block_reason_code?: string;
		reason?: string;
	}): Promise<{ data: { rule: RoutingRule; change_request?: ChangeRequest }; requestId: string }> {
		return this.request<{ rule: RoutingRule; change_request?: ChangeRequest }>(`/api/v1/routing/rules/${id}`, {
			method: 'PUT',
			body: JSON.stringify(payload)
		});
	}

	async deleteRoutingRule(id: string, reason?: string): Promise<{ data: { change_request?: ChangeRequest }; requestId: string }> {
		return this.request<{ change_request?: ChangeRequest }>(`/api/v1/routing/rules/${id}`, {
			method: 'DELETE',
			body: JSON.stringify({ reason })
		});
	}

	// Routing Simulator
	async simulateRouting(payload: {
		customer_org_id: string;
		destination_e164?: string;
		destination_number?: string;
		sender_id?: string;
		sender_id_type?: string;
		message_type?: string;
	}): Promise<{ data: SimulateResult; requestId: string }> {
		const dest = payload.destination_e164 || payload.destination_number || '';
		const res = await this.request<any>('/api/v1/routing/simulate', {
			method: 'POST',
			body: JSON.stringify({
				...payload,
				destination_e164: dest,
				destination_number: dest
			})
		});
		const raw = res.data;
		if (raw && !raw.resolve) {
			res.data = {
				resolve: raw,
				trace: raw.trace || []
			} as SimulateResult;
		}
		return res;
	}

	// Connection Overrides / Kill Switch
	async listAllConnections(): Promise<{ data: ProviderConnection[]; requestId: string }> {
		const providersRes = await this.listProviders({ pageSize: 100 });
		const providers = providersRes.data.items || [];
		const allConns: ProviderConnection[] = [];
		await Promise.all(
			providers.map(async (p) => {
				try {
					const cRes = await this.listProviderConnections(p.id);
					allConns.push(...(cRes.data || []));
				} catch {
					// Ignore individual provider errors
				}
			})
		);
		return { data: allConns, requestId: providersRes.requestId };
	}

	async listConnectionOverrides(): Promise<{ data: ConnectionOverride[]; requestId: string }> {
		return this.request<ConnectionOverride[]>('/api/v1/routing/connections/overrides');
	}

	async disableConnection(connectionId: string, reason: string): Promise<{ data: ConnectionOverride; requestId: string }> {
		return this.request<ConnectionOverride>(`/api/v1/routing/connections/${connectionId}/disable`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	async enableConnection(connectionId: string, reason: string): Promise<{ data: { change_request?: ChangeRequest }; requestId: string }> {
		return this.request<{ change_request?: ChangeRequest }>(`/api/v1/routing/connections/${connectionId}/enable`, {
			method: 'POST',
			body: JSON.stringify({ reason })
		});
	}

	// Snapshot & Coverage
	async getSnapshotStatus(): Promise<{ data: SnapshotStatus; requestId: string }> {
		return this.request<SnapshotStatus>('/api/v1/routing/snapshot-status');
	}

	async getCoverageReport(): Promise<{ data: CoverageReportItem[]; requestId: string }> {
		return this.request<CoverageReportItem[]>('/api/v1/routing/coverage-report');
	}

	// Phase 8: Messaging Methods
	async listMessages(params?: {
		page?: number;
		per_page?: number;
		status?: string;
		destination?: string;
		sender_id?: string;
		customer_org_id?: string;
		provider_connection_id?: string;
		date_from?: string;
		date_to?: string;
	}): Promise<{ data: { items: MessageView[]; total: number; page: number; per_page: number }; requestId: string }> {
		const q = new URLSearchParams();
		if (params?.page) q.set('page', String(params.page));
		if (params?.per_page) q.set('per_page', String(params.per_page));
		if (params?.status) q.set('status', params.status);
		if (params?.destination) q.set('destination', params.destination);
		if (params?.sender_id) q.set('sender_id', params.sender_id);
		if (params?.customer_org_id) q.set('customer_org_id', params.customer_org_id);
		if (params?.provider_connection_id) q.set('provider_connection_id', params.provider_connection_id);
		if (params?.date_from) q.set('date_from', params.date_from);
		if (params?.date_to) q.set('date_to', params.date_to);

		const queryStr = q.toString() ? `?${q.toString()}` : '';
		return this.request<{ items: MessageView[]; total: number; page: number; per_page: number }>(`/api/v1/messages${queryStr}`);
	}

	async getMessage(id: string): Promise<{ data: MessageDetailView; requestId: string }> {
		return this.request<MessageDetailView>(`/api/v1/messages/${id}`);
	}

	async testSendMessage(req: TestSendRequest): Promise<{ data: any; requestId: string }> {
		return this.request<any>('/api/v1/messaging/test-send', {
			method: 'POST',
			body: JSON.stringify(req)
		});
	}

	async getSMPPSessions(): Promise<{ data: { sessions: SessionStats[] }; requestId: string }> {
		return this.request<{ sessions: SessionStats[] }>('/api/v1/messaging/smpp/sessions');
	}

	async getQueueStatus(): Promise<{ data: QueueBacklogStats; requestId: string }> {
		return this.request<QueueBacklogStats>('/api/v1/messaging/queue/status');
	}

	async getUnmatchedDLRs(params?: {
		page?: number;
		per_page?: number;
		match_status?: string;
	}): Promise<{ data: { items: UnmatchedDLR[]; total: number; page: number; per_page: number }; requestId: string }> {
		const q = new URLSearchParams();
		if (params?.page) q.set('page', String(params.page));
		if (params?.per_page) q.set('per_page', String(params.per_page));
		if (params?.match_status) q.set('match_status', params.match_status);

		const queryStr = q.toString() ? `?${q.toString()}` : '';
		return this.request<{ items: UnmatchedDLR[]; total: number; page: number; per_page: number }>(`/api/v1/messaging/dlrs/unmatched${queryStr}`);
	}

	async listSettlementExceptions(params?: {
		page?: number;
		per_page?: number;
		resolved?: boolean;
	}): Promise<{ data: { items: SettlementException[]; total: number; page: number; per_page: number }; requestId: string }> {
		const q = new URLSearchParams();
		if (params?.page) q.set('page', String(params.page));
		if (params?.per_page) q.set('per_page', String(params.per_page));
		if (params?.resolved !== undefined) q.set('resolved', String(params.resolved));

		const queryStr = q.toString() ? `?${q.toString()}` : '';
		return this.request<{ items: SettlementException[]; total: number; page: number; per_page: number }>(`/api/v1/messaging/settlement/exceptions${queryStr}`);
	}

	async resolveSettlementException(id: string): Promise<{ data: { success: boolean }; requestId: string }> {
		return this.request<{ success: boolean }>(`/api/v1/messaging/settlement/exceptions/${id}/resolve`, {
			method: 'POST'
		});
	}

	async runReconciliation(): Promise<{ data: ReconciliationReport; requestId: string }> {
		return this.request<ReconciliationReport>('/api/v1/messaging/reconciliation/run', {
			method: 'POST'
		});
	}

	async listReconciliationRuns(limit = 20): Promise<{ data: { items: ReconciliationRun[] }; requestId: string }> {
		return this.request<{ items: ReconciliationRun[] }>(`/api/v1/messaging/reconciliation/history?limit=${limit}`);
	}

	async listDLRMappings(connectionId?: string): Promise<{ data: { items: DLRMapping[] }; requestId: string }> {
		const query = connectionId ? `?connection_id=${connectionId}` : '';
		return this.request<{ items: DLRMapping[] }>(`/api/v1/messaging/dlr-mappings${query}`);
	}

	async createDLRMapping(mapping: Partial<DLRMapping>): Promise<{ data: DLRMapping; requestId: string }> {
		return this.request<DLRMapping>('/api/v1/messaging/dlr-mappings', {
			method: 'POST',
			body: JSON.stringify(mapping)
		});
	}

	async updateDLRMapping(id: string, mapping: Partial<DLRMapping>): Promise<{ data: DLRMapping; requestId: string }> {
		return this.request<DLRMapping>(`/api/v1/messaging/dlr-mappings/${id}`, {
			method: 'PUT',
			body: JSON.stringify(mapping)
		});
	}

	async deleteDLRMapping(id: string): Promise<{ data: { success: boolean }; requestId: string }> {
		return this.request<{ success: boolean }>(`/api/v1/messaging/dlr-mappings/${id}`, {
			method: 'DELETE'
		});
	}
}

export const apiClient = new ApiClient();
