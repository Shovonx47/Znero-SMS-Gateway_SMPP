export interface ResponseMeta {
	request_id: string;
}

export interface ApiSuccess<T> {
	success: true;
	data: T;
	meta: ResponseMeta;
}

export interface ApiErrorDetail {
	code: string;
	message: string;
	details?: unknown;
}

export interface ApiErrorBody {
	success: false;
	error: ApiErrorDetail;
	meta: ResponseMeta;
}

export interface LiveStatus {
	status: string;
	version: string;
	uptime_seconds: number;
}

export interface DependencyCheck {
	status: 'up' | 'down';
	latency_ms: number;
	reason?: string;
}

export interface ReadyReport {
	status: 'up' | 'down';
	checks: Record<string, DependencyCheck>;
	checked_at: string;
}

export interface MigrationState {
	version: number;
	dirty: boolean;
}

export interface SchemaEntry {
	key: string;
	value: string;
	created_at: string;
}

export interface SystemInfo {
	name: string;
	version: string;
	environment: string;
	migration: MigrationState;
	schema: SchemaEntry[];
}

export interface KafkaTestMessage {
	id: string;
	message: string;
	sent_at: string;
}

export interface KafkaPublishResult {
	topic: string;
	message: KafkaTestMessage;
}

// -------------------------------------------------------------
// Phase 2 Types: Auth, Organizations, RBAC, and Audit
// -------------------------------------------------------------

export interface TenantContext {
	actor_user_id: string;
	actor_org_id: string;
	actor_org_type: 'platform' | 'reseller' | 'customer';
	actor_org_path: string;
	is_platform_admin: boolean;
	permissions: Record<string, boolean>;
}

export interface User {
	id: string;
	organization_id: string;
	email: string;
	status: 'active' | 'suspended' | 'invited';
	mfa_enabled: boolean;
	mfa_enrolled_at?: string;
	failed_login_attempts: number;
	locked_until?: string;
	roles: string[];
	role_ids: string[];
	created_at: string;
	updated_at: string;
}

export interface SessionInfo {
	id: string;
	ip_address: string;
	user_agent: string;
	created_at: string;
	last_used_at: string;
	is_current: boolean;
}

export interface LoginResponse {
	access_token?: string;
	expires_in?: number;
	user?: User;
	mfa_required?: boolean;
	mfa_challenge_token?: string;
}

export interface MFASetupResponse {
	secret: string;
	uri: string;
	recovery_codes: string[];
}

export interface Organization {
	id: string;
	name: string;
	type: 'platform' | 'reseller' | 'customer';
	parent_id?: string | null;
	path: string;
	status: 'active' | 'suspended';
	created_at: string;
	updated_at: string;
}

export interface OrgListResult {
	items: Organization[];
	total: number;
	page: number;
	page_size: number;
}

export interface Permission {
	code: string;
	module: string;
	resource: string;
	action: string;
	description: string;
	created_at: string;
}

export interface Role {
	id: string;
	organization_id: string;
	name: string;
	description: string;
	is_system: boolean;
	permissions: string[];
	created_at: string;
	updated_at: string;
}

export interface AuditEntry {
	id: string;
	timestamp: string;
	actor_user_id?: string;
	actor_org_id?: string;
	target_org_id?: string;
	action: string;
	module: string;
	entity_type: string;
	entity_id: string;
	before_value?: unknown;
	after_value?: unknown;
	ip_address: string;
	user_agent: string;
	request_id: string;
	reason?: string;
}

export interface AuditListResult {
	items: AuditEntry[];
	total_count: number;
	page: number;
	page_size: number;
}

export interface LoginHistoryItem {
	id: string;
	email: string;
	success: boolean;
	reason: string;
	ip_address: string;
	user_agent: string;
	created_at: string;
}

export interface PaginatedResult<T> {
	items: T[];
	total: number;
	page: number;
	page_size: number;
}

// -------------------------------------------------------------
// Phase 3 Types: Master Data
// -------------------------------------------------------------

export interface Country {
	id: string;
	name: string;
	iso2: string;
	iso3: string;
	numeric_code: string;
	calling_code: string;
	continent: string;
	region: string;
	currency_code: string;
	timezones: string[];
	numbering_plan_notes: string;
	min_national_length: number;
	max_national_length: number;
	sender_id_policy_notes: string;
	dnd_policy_notes: string;
	consent_requirement_notes: string;
	data_source: string;
	verification_date?: string;
	verification_status: 'unverified' | 'verified';
	status: 'active' | 'inactive' | 'archived';
	version: number;
	created_at: string;
	updated_at: string;
	created_by?: string;
	updated_by?: string;
}

export interface CountryHistory {
	id: string;
	country_id: string;
	version: number;
	snapshot: Country;
	actor_user_id?: string;
	changed_at: string;
	reason: string;
}

export interface CountryListResult {
	items: Country[];
	total_count: number;
	page: number;
	page_size: number;
}

export interface CreateCountryRequest {
	name: string;
	iso2: string;
	iso3: string;
	numeric_code: string;
	calling_code: string;
	continent?: string;
	region?: string;
	currency_code?: string;
	timezones?: string[];
	numbering_plan_notes?: string;
	min_national_length?: number;
	max_national_length?: number;
	sender_id_policy_notes?: string;
	dnd_policy_notes?: string;
	consent_requirement_notes?: string;
	data_source?: string;
	reason?: string;
}

export interface UpdateCountryRequest {
	name?: string;
	calling_code?: string;
	continent?: string;
	region?: string;
	currency_code?: string;
	timezones?: string[];
	numbering_plan_notes?: string;
	min_national_length?: number;
	max_national_length?: number;
	sender_id_policy_notes?: string;
	dnd_policy_notes?: string;
	consent_requirement_notes?: string;
	data_source?: string;
	verification_status?: string;
	reason?: string;
}

export interface Operator {
	id: string;
	name: string;
	brand_name: string;
	parent_company: string;
	country_id: string;
	mcc: string;
	mnc: string;
	network_technology: string[];
	capabilities: Record<string, unknown>;
	data_source: string;
	verification_status: string;
	effective_date?: string;
	status: string;
	version: number;
	created_at: string;
	updated_at: string;
}

export interface OperatorListResult {
	items: Operator[];
	total_count: number;
	page: number;
	page_size: number;
}

export interface CreateOperatorRequest {
	name: string;
	brand_name?: string;
	parent_company?: string;
	country_id: string;
	mcc: string;
	mnc: string;
	network_technology?: string[];
	capabilities?: Record<string, unknown>;
	data_source?: string;
	reason?: string;
}

export interface UpdateOperatorRequest {
	name?: string;
	brand_name?: string;
	parent_company?: string;
	network_technology?: string[];
	capabilities?: Record<string, unknown>;
	data_source?: string;
	verification_status?: string;
	reason?: string;
}

export interface OperatorHistory {
	id: string;
	operator_id: string;
	version: number;
	snapshot: Operator;
	actor_user_id?: string;
	changed_at: string;
	reason: string;
}

export interface NumberPrefix {
	id: string;
	prefix: string;
	country_id: string;
	operator_id?: string;
	number_type: 'mobile' | 'fixed' | 'other';
	priority: number;
	effective_from?: string;
	effective_to?: string;
	data_source: string;
	status: string;
	version: number;
	created_at: string;
	updated_at: string;
}

export interface PrefixListResult {
	items: NumberPrefix[];
	total_count: number;
	page: number;
	page_size: number;
}

export interface CreatePrefixRequest {
	prefix: string;
	country_id: string;
	operator_id?: string;
	number_type?: string;
	priority?: number;
	effective_from?: string;
	effective_to?: string;
	data_source?: string;
	reason?: string;
}

export interface UpdatePrefixRequest {
	prefix?: string;
	operator_id?: string;
	number_type?: string;
	priority?: number;
	effective_from?: string;
	effective_to?: string;
	data_source?: string;
	reason?: string;
}

export interface PrefixHistory {
	id: string;
	prefix_id: string;
	version: number;
	snapshot: NumberPrefix;
	actor_user_id?: string;
	changed_at: string;
	reason: string;
}

export interface ChangeRequest {
	id: string;
	entity_type: string;
	entity_id?: string;
	operation: string;
	proposed_data: unknown;
	current_data?: unknown;
	status: string;
	maker_user_id: string;
	made_at: string;
	checker_user_id?: string;
	checked_at?: string;
	reject_reason: string;
	created_at: string;
	updated_at: string;
}

export interface ChangeRequestListResult {
	items: ChangeRequest[];
	total_count: number;
	page: number;
	page_size: number;
}

export interface ImportJob {
	id: string;
	entity_type: string;
	file_name: string;
	file_size: number;
	content_type: string;
	status: string;
	total_rows: number;
	valid_rows: number;
	invalid_rows: number;
	imported_rows: number;
	skipped_rows: number;
	allow_partial: boolean;
	error_summary: unknown;
	actor_user_id: string;
	created_at: string;
	completed_at?: string;
}

export interface ImportJobRow {
	id: string;
	job_id: string;
	row_number: number;
	raw_data: unknown;
	status: string;
	errors: Array<{ field: string; message: string }>;
	created_entity_id?: string;
}

export interface ImportJobListResult {
	items: ImportJob[];
	total_count: number;
	page: number;
	page_size: number;
}

export interface ImportJobRowsResult {
	items: ImportJobRow[];
	total_count: number;
	page: number;
	page_size: number;
}

export interface NumberLookupResult {
	original_number: string;
	normalized_e164: string | null;
	country?: Country;
	operator?: Operator;
	number_type: string;
	matched_prefix?: string;
	matched_rule_id?: string;
	is_valid: boolean;
	error?: string;
}

export interface BulkLookupResult {
	results: NumberLookupResult[];
	total: number;
	valid: number;
	invalid: number;
}

export interface TransformRule {
	id: string;
	name: string;
	description: string;
	operation: string;
	match_pattern: string;
	params: Record<string, unknown>;
	scope: string;
	country_id?: string;
	operator_id?: string;
	direction: string;
	priority: number;
	active_from?: string;
	active_to?: string;
	status: string;
	version: number;
	created_at: string;
	updated_at: string;
}

export interface TransformRuleListResult {
	items: TransformRule[];
	total_count: number;
	page: number;
	page_size: number;
}

export interface CreateTransformRuleRequest {
	name: string;
	description?: string;
	operation: string;
	match_pattern: string;
	params?: Record<string, unknown>;
	scope?: string;
	country_id?: string;
	operator_id?: string;
	direction?: string;
	priority?: number;
	active_from?: string;
	active_to?: string;
}

export interface UpdateTransformRuleRequest extends Partial<CreateTransformRuleRequest> {}

export interface TransformPreviewResult {
	original: string;
	transformed: string;
	rule_applied: boolean;
	rule_id?: string;
}

export interface TransformChainResult {
	original: string;
	final: string;
	steps: Array<{ rule_id: string; rule_name: string; before: string; after: string }>;
}

// -------------------------------------------------------------
// Phase 4 Types: Providers, Coverage, Customer Profiles & Credentials, Sender IDs
// -------------------------------------------------------------

export interface SecretMeta {
	set?: boolean;
	configured?: boolean;
	masked_display?: string;
	last_changed?: string;
	changed_at?: string;
}

export interface Provider {
	id: string;
	name: string;
	code: string;
	provider_type: 'smpp' | 'http' | 'other';
	legal_entity_name: string;
	contact_technical: string;
	contact_commercial: string;
	contact_noc: string;
	country_id?: string;
	country_iso2?: string;
	country_name?: string;
	timezone: string;
	currency_code: string;
	notes: string;
	status: 'draft' | 'active' | 'suspended' | 'inactive';
	archived: boolean;
	version: number;
	created_at: string;
	updated_at: string;
	created_by?: string;
	updated_by?: string;
}

export interface ProviderHistory {
	id: string;
	provider_id: string;
	version: number;
	snapshot: Record<string, unknown>;
	actor_user_id?: string;
	changed_at: string;
	reason: string;
}

export interface ProviderMutationResponse {
	provider?: Provider;
	change_request?: ChangeRequest;
	pending_approval: boolean;
}

export interface HTTPConnectorConfig {
	method?: string;
	url_template?: string;
	headers?: Record<string, string>;
	auth_header_name?: string;
	auth_query_param?: string;
	body_format?: 'json' | 'form' | 'none';
	body_template?: string;
	request_timeout_ms?: number;
	max_response_bytes?: number;
	success_http_min?: number;
	success_http_max?: number;
	success_json_path?: string;
	success_equals?: string;
	provider_msg_id_path?: string;
	provider_msg_id_regex?: string;
	error_code_path?: string;
	error_msg_path?: string;
	permanent_http_codes?: number[];
	permanent_error_codes?: string[];
	throttle_http_codes?: number[];
	throttle_error_codes?: string[];
	callback_signature_header?: string;
	callback_signature_algo?: 'hmac_sha256' | 'shared_secret' | 'none';
	callback_msg_id_path?: string;
	callback_status_path?: string;
	callback_error_code_path?: string;
	callback_status_map?: Record<string, string>;
	poll_method?: 'GET' | 'POST';
	poll_url_template?: string;
	poll_interval_sec?: number;
	poll_status_path?: string;
	poll_error_code_path?: string;
	poll_status_map?: Record<string, string>;
}

export interface HTTPTestSendRequest {
	destination?: string;
	sender_id?: string;
	text?: string;
	encoding?: string;
	simulate_mode?: '' | 'accept' | 'reject' | 'throttle' | 'server_err';
}

export interface HTTPTestSendResult {
	success: boolean;
	status: string;
	http_status_code?: number;
	provider_message_id?: string;
	error_code?: string;
	error_message?: string;
	latency_ms: number;
	method: string;
	redacted_url: string;
	redacted_body?: string;
	response_snippet?: string;
	simulated: boolean;
}

export interface ProviderConnection {
	id: string;
	provider_id: string;
	name: string;
	protocol: 'smpp' | 'http';
	role_notes: string;
	status: 'draft' | 'active' | 'disabled';
	archived: boolean;
	max_tps: number;
	smpp_host: string;
	smpp_port?: number;
	smpp_system_id: string;
	smpp_password?: SecretMeta;
	smpp_password_meta?: SecretMeta;
	smpp_system_type: string;
	smpp_bind_type: string;
	smpp_tls: boolean;
	smpp_window_size: number;
	smpp_enquire_link_sec: number;
	smpp_source_ton: number;
	smpp_source_npi: number;
	smpp_dest_ton: number;
	smpp_dest_npi: number;
	smpp_charset: string;
	ip_allowlist_notes: string;
	http_base_url: string;
	http_auth_type: 'none' | 'basic' | 'bearer' | 'header_token' | 'api_key_header' | 'query_token' | 'api_key_query';
	http_credentials?: SecretMeta;
	http_credentials_meta?: SecretMeta;
	http_format_notes: string;
	http_dlr_callback_notes: string;
	http_config?: HTTPConnectorConfig;
	http_dlr_mode?: 'none' | 'callback' | 'polling';
	http_dlr_secret?: SecretMeta;
	http_dlr_callback_path?: string;
	version: number;
	created_at: string;
	updated_at: string;
}

export interface ProviderConnectionHistory {
	id: string;
	connection_id: string;
	version: number;
	snapshot: Record<string, unknown>;
	actor_user_id?: string;
	changed_at: string;
	reason: string;
}

export interface ConnectionMutationResponse {
	connection?: ProviderConnection;
	change_request?: ChangeRequest;
	pending_approval: boolean;
}

export interface ProviderCoverage {
	id: string;
	provider_id: string;
	provider_code?: string;
	provider_name?: string;
	country_id: string;
	country_iso2?: string;
	country_name?: string;
	operator_id?: string;
	operator_name?: string;
	operator_mcc?: string;
	operator_mnc?: string;
	status: 'declared' | 'tested' | 'active' | 'suspended';
	notes: string;
	effective_from?: string;
	effective_to?: string;
	data_source: string;
	archived: boolean;
	version: number;
	created_at: string;
	updated_at: string;
}

export interface ProviderCoverageHistory {
	id: string;
	coverage_id: string;
	version: number;
	snapshot: Record<string, unknown>;
	actor_user_id?: string;
	changed_at: string;
	reason: string;
}

export interface CoverageMutationResponse {
	coverage?: ProviderCoverage;
	change_request?: ChangeRequest;
	pending_approval: boolean;
}

export interface CustomerProfile {
	id: string;
	organization_id: string;
	organization_name?: string;
	parent_org_id?: string;
	account_type: 'prepaid' | 'postpaid';
	billing_currency: string;
	timezone: string;
	default_language: string;
	technical_contact: string;
	billing_contact: string;
	credit_limit: number;
	default_max_tps: number;
	daily_message_cap?: number;
	monthly_message_cap?: number;
	allowed_countries: string[];
	destination_policy: 'allow_all' | 'allow_listed' | 'block_listed';
	billing_mode: 'on_provider_accept' | 'on_delivery';
	dlr_wait_hours: number;
	no_dlr_policy: 'retain_until_resolution' | 'immediate_release' | 'capture_at_ttl';
	failed_after_accept_policy: 'keep_charge' | 'refund';
	status: 'draft' | 'active' | 'suspended' | 'closed';
	notes: string;
	archived: boolean;
	version: number;
	created_at: string;
	updated_at: string;
}

export interface CustomerProfileHistory {
	id: string;
	profile_id: string;
	version: number;
	snapshot: Record<string, unknown>;
	actor_user_id?: string;
	changed_at: string;
	reason: string;
}

export interface ProfileMutationResponse {
	profile?: CustomerProfile;
	change_request?: ChangeRequest;
	pending_approval: boolean;
}

export interface CustomerAPIKey {
	id: string;
	organization_id: string;
	organization_name?: string;
	label: string;
	name?: string;
	key_prefix: string;
	scopes: string[];
	ip_allowlist: string[];
	expires_at?: string;
	last_used_at?: string;
	status: 'active' | 'revoked' | 'expired';
	revoked_at?: string;
	created_at: string;
	updated_at: string;
}

export interface CreateAPIKeyResponse {
	api_key: CustomerAPIKey;
	plaintext_key: string;
	warning: string;
}

export interface CustomerSMPPAccount {
	id: string;
	organization_id: string;
	organization_name?: string;
	system_id: string;
	password_meta: SecretMeta;
	ip_allowlist: string[];
	allowed_bind_types: string[];
	max_binds: number;
	max_tps: number;
	status: 'active' | 'disabled' | 'suspended';
	created_at: string;
	updated_at: string;
}

export interface CreateSMPPAccountResponse {
	account: CustomerSMPPAccount;
	plaintext_password: string;
	warning: string;
}

export interface SenderID {
	id: string;
	organization_id: string;
	organization_name?: string;
	sender_id: string;
	sender_type: 'alphanumeric' | 'numeric' | 'shortcode';
	country_id?: string;
	country_iso2?: string;
	country_name?: string;
	country_sender_id_policy_notes?: string;
	status: 'pending' | 'approved' | 'rejected' | 'suspended';
	approval_notes: string;
	effective_from?: string;
	effective_to?: string;
	data_source: string;
	archived: boolean;
	version: number;
	created_at: string;
	updated_at: string;
	created_by?: string;
	updated_by?: string;
}

export interface SenderIDHistory {
	id: string;
	sender_id_ref: string;
	version: number;
	snapshot: Record<string, unknown>;
	actor_user_id?: string;
	changed_at: string;
	reason: string;
}

// -------------------------------------------------------------
// Phase 5: Rating Engine & Rate Cards
// -------------------------------------------------------------

export interface RateCard {
	id: string;
	name: string;
	kind: 'provider_buy' | 'customer_sell';
	owner_org_id: string;
	provider_id?: string;
	currency_code: string;
	channel: string;
	status: 'draft' | 'active' | 'inactive' | 'archived';
	description: string;
	archived: boolean;
	version: number;
	rate_count?: number;
	created_at: string;
	updated_at: string;
	created_by?: string;
	updated_by?: string;
}

export interface RateCardHistory {
	id: string;
	card_id: string;
	name: string;
	kind: string;
	owner_org_id: string;
	provider_id?: string;
	currency_code: string;
	channel: string;
	status: string;
	description: string;
	archived: boolean;
	version: number;
	snapshot: Record<string, unknown>;
	changed_by?: string;
	changed_at: string;
	change_reason: string;
}

export interface RateCardMutationResponse {
	card?: RateCard;
	change_request?: ChangeRequest;
	pending_approval: boolean;
}

export interface Rate {
	id: string;
	card_id: string;
	country_id: string;
	country_iso2?: string;
	country_name?: string;
	operator_id?: string;
	operator_name?: string;
	operator_mcc?: string;
	operator_mnc?: string;
	message_type: string;
	price: string;
	min_billing_note?: string;
	effective_from: string;
	effective_to?: string;
	source: string;
	status: string;
	version: number;
	created_at: string;
	updated_at: string;
	created_by?: string;
	updated_by?: string;
	lowest_buy_cost?: string;
	margin_amount?: string;
	margin_percent?: string;
}

export interface RateHistory {
	id: string;
	rate_id: string;
	card_id: string;
	country_id: string;
	operator_id?: string;
	message_type: string;
	price: string;
	version: number;
	snapshot: Record<string, unknown>;
	changed_by?: string;
	changed_at: string;
	change_reason: string;
}

export interface RateMutationResponse {
	rate?: Rate;
	change_request?: ChangeRequest;
	pending_approval: boolean;
}

export interface CustomerRateAssignment {
	id: string;
	customer_org_id: string;
	customer_org_name?: string;
	card_id: string;
	card_name?: string;
	currency_code?: string;
	effective_from: string;
	effective_to?: string;
	status: string;
	version: number;
	created_at: string;
	updated_at: string;
	assigned_by?: string;
}

export interface ResellerCostAssignment {
	id: string;
	reseller_org_id: string;
	reseller_org_name?: string;
	card_id: string;
	card_name?: string;
	currency_code?: string;
	effective_from: string;
	effective_to?: string;
	status: string;
	version: number;
	created_at: string;
	updated_at: string;
	assigned_by?: string;
}

export interface AssignmentMutationResponse {
	assignment?: CustomerRateAssignment | ResellerCostAssignment;
	change_request?: ChangeRequest;
	pending_approval: boolean;
}

export interface RateMatch {
	found: boolean;
	card_id?: string;
	country_id?: string;
	country_iso2?: string;
	country_name?: string;
	operator_id?: string;
	operator_name?: string;
	message_type?: string;
	rate_id?: string;
	price?: string;
	currency?: string;
	effective_from?: string;
	effective_to?: string;
	specificity?: 'operator' | 'country_default';
	resolved_country_iso2?: string;
	resolved_country_name?: string;
	resolved_operator_name?: string;
	resolved_e164?: string;
}

export interface MarginReportItem {
	card_id: string;
	card_name: string;
	country_id: string;
	country_iso2: string;
	country_name: string;
	operator_id?: string;
	operator_name?: string;
	message_type: string;
	sell_price: string;
	lowest_buy_cost?: string;
	lowest_buy_provider?: string;
	margin_amount?: string;
	margin_percent?: string;
	currency: string;
	violation: boolean;
	violation_reason?: string;
	active_providers?: number;
	// Backward compatibility aliases
	sell_card_id?: string;
	sell_card_name?: string;
	has_violation?: boolean;
}

export interface MarginResult {
	comparable: boolean;
	sell_price: string;
	lowest_buy_cost?: string;
	reseller_cost?: string;
	margin_amount?: string;
	margin_percent?: string;
	currency: string;
	violation: boolean;
	reason?: string;
}

// -------------------------------------------------------------------------
// Phase 6: Wallets, Ledger, Billing Types
// -------------------------------------------------------------------------

export interface Wallet {
	id: string;
	organization_id: string;
	organization_name?: string;
	organization_type?: string;
	currency: string;
	status: 'active' | 'frozen' | 'closed';
	account_type: 'prepaid' | 'postpaid';
	low_balance_threshold: string;
	cached_balance: string;
	active_holds: string;
	credit_limit: string;
	available_balance: string;
	version: number;
	created_at: string;
	updated_at: string;
}

export interface LedgerEntry {
	id: string;
	transaction_id: string;
	account_id: string;
	account_code: string;
	account_name: string;
	direction: 'debit' | 'credit';
	amount: string;
	currency: string;
	created_at: string;
}

export interface LedgerTransaction {
	id: string;
	type: 'topup' | 'adjustment' | 'charge' | 'refund' | 'reversal' | 'hold_capture' | 'transfer';
	reference_type?: string;
	reference_id?: string;
	external_ref?: string;
	idempotency_key?: string;
	description: string;
	reason?: string;
	reversal_of_transaction_id?: string;
	tenant_org_id?: string;
	actor_user_id?: string;
	actor_org_id?: string;
	created_at: string;
	entries?: LedgerEntry[];
}

export interface Hold {
	id: string;
	wallet_id: string;
	amount: string;
	currency: string;
	status: 'active' | 'captured' | 'released' | 'expired';
	expires_at: string;
	reference_type?: string;
	reference_id?: string;
	idempotency_key?: string;
	captured_amount: string;
	version: number;
	created_at: string;
	updated_at: string;
}

export interface StatementItem {
	transaction_id: string;
	created_at: string;
	type: string;
	description: string;
	reason?: string;
	external_ref?: string;
	direction: 'debit' | 'credit';
	amount: string;
	running_balance: string;
}

export interface ReconciliationReport {
	wallet_id: string;
	organization_id: string;
	organization_name: string;
	currency: string;
	cached_balance: string;
	computed_balance: string;
	difference: string;
	status: 'matched' | 'mismatch' | 'repaired';
	entries_count: number;
	checked_at: string;
}

export interface InvoiceLine {
	id: string;
	invoice_id: string;
	line_number: number;
	description: string;
	quantity: string;
	unit_price: string;
	amount: string;
	ledger_charge_id?: string;
	created_at: string;
}

export interface Invoice {
	id: string;
	customer_org_id: string;
	customer_org_name?: string;
	billing_period_id?: string;
	currency: string;
	status: 'draft' | 'issued' | 'paid' | 'void';
	invoice_number?: string;
	issue_date?: string;
	due_date?: string;
	subtotal: string;
	tax_label: string;
	tax_amount: string;
	total: string;
	amount_paid: string;
	outstanding_balance: string;
	notes: string;
	void_reason?: string;
	credit_note_for_invoice_id?: string;
	credit_note_for_invoice_number?: string;
	version: number;
	created_by?: string;
	created_at: string;
	updated_at: string;
	lines?: InvoiceLine[];
}

export interface Payment {
	id: string;
	invoice_id: string;
	invoice_number?: string;
	customer_org_id: string;
	amount: string;
	currency: string;
	payment_method: string;
	reference: string;
	payment_date: string;
	ledger_transaction_id?: string;
	notes: string;
	created_by?: string;
	created_at: string;
}

// ============================================================================
// Phase 7: Routing Engine Types
// ============================================================================

export type RoutePlanStrategy = 'priority' | 'least_cost' | 'weighted';
export type RoutePlanStatus = 'draft' | 'active' | 'deactivated' | 'archived';

export interface RoutePlan {
	id: string;
	name: string;
	code: string;
	description?: string;
	strategy: RoutePlanStrategy;
	status: RoutePlanStatus;
	notes?: string;
	created_by: string;
	created_at: string;
	updated_at: string;
	version: number;
	entries?: RoutePlanEntry[];
	entries_count?: number;
}

export interface RoutePlanHistory {
	id: string;
	plan_id: string;
	changed_by: string;
	changed_at: string;
	change_type: string;
	snapshot: any;
}

export interface RoutePlanEntry {
	id: string;
	plan_id: string;
	connection_id: string;
	provider_name?: string;
	connection_name?: string;
	scope_type: 'global' | 'country' | 'operator' | 'prefix';
	scope_country_id?: string;
	scope_country_iso2?: string;
	scope_country_name?: string;
	scope_operator_id?: string;
	scope_operator_name?: string;
	scope_prefix?: string;
	priority: number;
	weight: number;
	min_margin_override?: string;
	allowed_sender_id_types: string[];
	destination_number_format: string;
	allow_tested_coverage: boolean;
	status: 'active' | 'disabled';
	version: number;
	created_at: string;
	updated_at: string;
}

export interface RoutePlanEntryHistory {
	id: string;
	entry_id: string;
	changed_by: string;
	changed_at: string;
	change_type: string;
	snapshot: any;
}

export interface CustomerRouteAssignment {
	id: string;
	customer_org_id: string;
	customer_name?: string;
	plan_id: string;
	plan_name?: string;
	plan_code?: string;
	country_id?: string;
	country_iso2?: string;
	country_name?: string;
	priority: number;
	effective_from?: string;
	effective_to?: string;
	status: 'active' | 'disabled';
	version: number;
	created_by: string;
	created_at: string;
	updated_at: string;
}

export interface CustomerRouteAssignmentHistory {
	id: string;
	assignment_id: string;
	changed_by: string;
	changed_at: string;
	change_type: string;
	snapshot: any;
}

export interface RoutingRule {
	id: string;
	name: string;
	priority: number;
	status: 'active' | 'disabled';
	effective_from?: string;
	effective_to?: string;
	conditions: Record<string, any>;
	action_type: 'block' | 'force' | 'avoid' | 'prefer';
	action_connection_id?: string;
	action_connection_name?: string;
	action_block_reason_code?: string;
	version: number;
	created_by: string;
	created_at: string;
	updated_at: string;
}

export interface RoutingRuleHistory {
	id: string;
	rule_id: string;
	changed_by: string;
	changed_at: string;
	change_type: string;
	snapshot: any;
}

export interface ConnectionOverride {
	id: string;
	connection_id: string;
	provider_id?: string;
	provider_name?: string;
	connection_name?: string;
	state: 'disabled';
	reason: string;
	set_by: string;
	set_at: string;
}

export interface ExclusionReason {
	connection_id?: string;
	provider_id?: string;
	provider_name?: string;
	connection_name?: string;
	reason: string;
	reason_code?: string;
	detail?: string;
}

export interface Candidate {
	connection_id: string;
	provider_id: string;
	provider_name?: string;
	connection_name?: string;
	priority: number;
	weight: number;
	estimated_buy_rate?: string;
	estimated_sell_rate?: string;
	currency_code?: string;
	margin_percent?: string;
}

export interface ResolveResult {
	candidate?: Candidate;
	candidates: Candidate[];
	all_candidates?: Candidate[];
	excluded: ExclusionReason[];
	exclusions?: ExclusionReason[];
	rules_applied: string[];
	plan_used: string;
	plan_id?: string;
	strategy_used?: RoutePlanStrategy;
	snapshot_built_at?: string;
	snapshot_age_ms?: number;
}

export interface SimulateResult {
	resolve: ResolveResult;
	trace: string[];
}

export interface SnapshotStatus {
	built_at: string;
	age_ms: number;
	plans_count: number;
	assignments_count: number;
	rules_count: number;
	overrides_count: number;
	connections_count: number;
}

export interface CoverageReportItem {
	country_id: string;
	country_iso2: string;
	country_name: string;
	operator_id?: string;
	operator_name?: string;
	candidate_count: number;
	active_providers: string[];
	is_routable: boolean;
}

// Phase 8: Messaging Types
export interface MessageView {
	id: string;
	created_at: string;
	customer_org_id: string;
	client_reference?: string;
	original_destination: string;
	destination: string;
	sender_id: string;
	sender_id_type: string;
	encoding: string;
	body?: string;
	body_length: number;
	total_parts: number;
	status: string;
	failure_reason?: string;
	sell_price: string;
	sell_currency: string;
	attempts: number;
	expires_at: string;
	submitted_at?: string;
	delivered_at?: string;

	// Platform only
	provider_connection_id?: string;
	provider_message_id?: string;
	estimated_buy_cost?: string;
	actual_buy_cost?: string;
	buy_currency?: string;
	route_plan_id?: string;
	margin?: string;
}

export interface MessagePart {
	id: string;
	message_id: string;
	part_number: number;
	total_parts: number;
	status: string;
	provider_message_id?: string;
	updated_at: string;
}

export interface MessageAttempt {
	id: string;
	attempt_number: number;
	connection_id: string;
	status: string;
	provider_message_id?: string;
	error_code?: string;
	error_message?: string;
	buy_cost?: string;
	buy_currency?: string;
	latency_ms?: number;
	started_at: string;
	finished_at?: string;
}

export interface MessageEvent {
	id: string;
	event_type: string;
	from_status?: string;
	to_status: string;
	actor_type: string;
	details?: any;
	created_at: string;
}

export interface MessageDLR {
	id: string;
	connection_id?: string;
	provider_message_id: string;
	status: string;
	error_code?: string;
	raw_receipt?: string;
	received_at: string;
}

export interface HoldSummary {
	hold_id: string;
	wallet_id: string;
	original_hold: string;
	captured?: string;
	status: string;
	expires_at: string;
}

export interface MessageDetailView {
	message: MessageView;
	parts: MessagePart[];
	attempts: MessageAttempt[];
	events: MessageEvent[];
	dlrs: MessageDLR[];
	hold_info?: HoldSummary;
}

export interface SessionStats {
	connection_id: string;
	bound: boolean;
	in_flight: number;
	current_tps: number;
	success_count: number;
	failure_count: number;
	last_error_code: string;
	reconnect_count: number;
}

export interface QueueBacklogStats {
	pending_count: number;
	sending_count: number;
	oldest_pending_time?: string;
	oldest_age_seconds: number;
}

export interface UnmatchedDLR {
	id: string;
	connection_id?: string;
	provider_message_id: string;
	status: string;
	error_code?: string;
	raw_receipt?: string;
	match_status: string;
	retry_count: number;
	last_retry_at?: string;
	received_at: string;
}

export interface SettlementException {
	id: string;
	message_id: string;
	message_created_at: string;
	customer_org_id?: string;
	hold_id?: string;
	exception_type: string;
	severity: string;
	details: any;
	resolved: boolean;
	resolved_by?: string;
	resolved_at?: string;
	created_at: string;
}

export interface ReconciliationRun {
	id: string;
	started_at: string;
	completed_at?: string;
	messages_checked: number;
	discrepancy_count: number;
	summary_json: any;
	initiated_by?: string;
}

export interface ReconciliationReport {
	messages_checked: number;
	terminal_count: number;
	in_flight_count: number;
	unresolved_unknown: number;
	unmatched_dlrs_count: number;
	discrepancies: any[];
	discrepancy_count: number;
	ledger_matches: boolean;
}

export interface DLRMapping {
	id: string;
	connection_id: string;
	upstream_stat: string;
	upstream_err?: string;
	mapped_status: string;
	description?: string;
	version: number;
	created_at: string;
	updated_at: string;
}

export interface TestSendRequest {
	customer_org_id: string;
	destination: string;
	sender_id: string;
	text: string;
	validity_seconds?: number;
}

// Phase 8B: Customer Edge, Webhooks, SMPP Server, Inbound MO Types
export interface CustomerSessionInfo {
	id: string;
	account_id: string;
	customer_org_id: string;
	system_id: string;
	bind_type: string;
	remote_ip: string;
	bound: boolean;
	in_flight: number;
	bound_at: string;
}

export interface InboundNumber {
	id: string;
	number_raw: string;
	number_e164: string;
	country_id?: string;
	provider_connection_id?: string;
	customer_org_id?: string;
	type: string;
	status: string;
	capabilities: string[];
	version: number;
	created_at: string;
	updated_at: string;
}

export interface InboundMessage {
	id: string;
	created_at: string;
	source_raw: string;
	source_e164: string;
	destination_raw: string;
	destination_e164: string;
	inbound_number_id?: string;
	customer_org_id?: string;
	provider_connection_id?: string;
	provider_message_id?: string;
	encoding: string;
	dedupe_hash: string;
	status: string;
}

export interface CustomerWebhook {
	id: string;
	customer_org_id: string;
	url: string;
	events: string[];
	status: string;
	consecutive_failures: number;
	version: number;
	created_at: string;
	updated_at: string;
	secret?: string; // Only present on create
}

export interface CustomerWebhookDelivery {
	id: string;
	webhook_id: string;
	customer_org_id: string;
	event_id: string;
	event_type: string;
	payload: any;
	status: string;
	attempts: number;
	max_attempts: number;
	next_attempt_at: string;
	created_at: string;
	delivered_at?: string;
}

export interface CustomerWebhookAttempt {
	id: string;
	delivery_id: string;
	attempt_number: number;
	response_code?: number;
	latency_ms: number;
	response_snippet?: string;
	error_class?: string;
	created_at: string;
}

export interface CustomerUsage {
	tps_limit: number;
	daily_count: number;
	daily_cap: number;
	monthly_count: number;
	monthly_cap: number;
	balance: string;
	available: string;
	credit_limit: string;
	currency: string;
}




// --- Phase 9 Visibility Types ---

export interface TimeSeriesPoint {
	bucket: string;
	submitted: number;
	delivered: number;
	failed: number;
	expired: number;
	delivery_rate: number;
	avg_latency_ms: number;
	p95_latency_ms: number;
	revenue_by_cur: Record<string, string>;
	cost_by_cur?: Record<string, string>;
	margin_by_cur?: Record<string, string>;
}

export interface ExecutiveDashboard {
	total_submitted: number;
	total_delivered: number;
	total_failed: number;
	delivery_rate: number;
	revenue_by_cur: Record<string, string>;
	cost_by_cur: Record<string, string>;
	margin_by_cur: Record<string, string>;
	time_series: TimeSeriesPoint[];
	top_customers?: Array<{
		customer_id: string;
		customer_name: string;
		submitted_count: number;
		delivered_count: number;
		revenue: string;
		currency: string;
	}>;
	provider_breakdown?: Array<{
		provider_id: string;
		provider_name: string;
		connection_count: number;
		submitted_count: number;
		delivered_count: number;
		delivery_rate: number;
		avg_latency_ms: number;
		cost: string;
		currency: string;
	}>;
}

export interface NOCDashboard {
	system_health: 'healthy' | 'degraded' | 'critical';
	current_tps: number;
	inbound_tps: number;
	outbound_tps: number;
	active_connections_count: number;
	firing_alerts_count: number;
	total_queued_messages: number;
	connections: Array<{
		id: string;
		provider_id: string;
		name: string;
		protocol: string;
		status: string;
		current_tps: number;
		latency_ms: number;
		queue_depth: number;
		error_rate: number;
	}>;
	recent_alerts: FiringAlert[];
	queue_depths: Record<string, number>;
}

export interface ResellerDashboard {
	total_submitted: number;
	total_delivered: number;
	total_failed: number;
	delivery_rate: number;
	revenue_by_cur: Record<string, string>;
	time_series: TimeSeriesPoint[];
	top_customers: Array<{
		customer_id: string;
		customer_name: string;
		submitted_count: number;
		delivered_count: number;
		revenue: string;
		currency: string;
	}>;
}

export interface CustomerDashboard {
	total_submitted: number;
	total_delivered: number;
	total_failed: number;
	delivery_rate: number;
	spend_by_cur: Record<string, string>;
	time_series: TimeSeriesPoint[];
	error_breakdown: Array<{
		error_code: string;
		count: number;
		percentage: number;
	}>;
}

export interface LiveTelemetry {
	current_tps: number;
	queued_messages: number;
	active_connections: number;
	latency_p95_ms: number;
	firing_alerts_count: number;
	timestamp: string;
}

export interface NotificationItem {
	id: string;
	user_id: string;
	organization_id: string;
	title: string;
	message: string;
	severity: 'info' | 'warning' | 'error' | 'critical';
	category: string;
	is_read: boolean;
	read_at?: string;
	created_at: string;
}

export interface AlertRule {
	id: string;
	name: string;
	description?: string;
	metric_name: string;
	condition: '>' | '<' | '>=' | '<=' | '==' | '!=';
	threshold: number;
	duration_seconds: number;
	severity: 'info' | 'warning' | 'error' | 'critical';
	is_enabled: boolean;
	silence_until?: string;
	status: 'ok' | 'firing' | 'silenced' | 'unknown';
	last_evaluated_at?: string;
}

export interface FiringAlert {
	id: string;
	rule_id: string;
	rule_name: string;
	severity: 'info' | 'warning' | 'error' | 'critical';
	current_value: number;
	threshold: number;
	message: string;
	started_at: string;
}

export interface Incident {
	id: string;
	title: string;
	severity: 'low' | 'medium' | 'high' | 'critical';
	status: 'open' | 'investigating' | 'identified' | 'monitoring' | 'resolved';
	summary?: string;
	resolution_notes?: string;
	lead_user_id?: string;
	declared_at: string;
	identified_at?: string;
	resolved_at?: string;
	created_at: string;
	updated_at: string;
	timeline?: IncidentTimeline[];
}

export interface IncidentTimeline {
	id: string;
	incident_id: string;
	event_type: string;
	notes: string;
	actor_user_id?: string;
	created_at: string;
}

export interface SettingItem {
	key: string;
	category: string;
	value_type: 'string' | 'integer' | 'boolean' | 'float' | 'json' | 'secret';
	value: any;
	default_value: any;
	description?: string;
	is_secret: boolean;
	is_read_only: boolean;
	updated_at: string;
}

export interface SearchResultItem {
	id: string;
	type: 'message' | 'customer' | 'organization' | 'provider' | 'incident' | 'alert';
	title: string;
	subtitle?: string;
	status?: string;
	route?: string;
}

export interface SavedFilter {
	id: string;
	organization_id: string;
	user_id: string;
	view: string;
	name: string;
	filter_params: any;
	is_default: boolean;
	created_at: string;
}

export interface UserPreferences {
	user_id: string;
	theme: 'system' | 'light' | 'dark';
	locale: 'en' | 'bn';
	dashboard_layout?: any;
	updated_at: string;
}

export interface ExportJob {
	id: string;
	organization_id: string;
	user_id: string;
	export_type: 'messages' | 'delivery_report' | 'financial_report';
	format: 'csv' | 'json' | 'xlsx';
	status: 'pending' | 'processing' | 'completed' | 'failed';
	filter_params: any;
	row_count: number;
	file_path?: string;
	error_message?: string;
	started_at?: string;
	completed_at?: string;
	expires_at?: string;
	created_at: string;
}
