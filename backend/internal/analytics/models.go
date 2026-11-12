package analytics

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Cursor represents the high-water mark for an analytics stream processor.
type Cursor struct {
	Name          string     `json:"name"`
	LastEventID   *uuid.UUID `json:"last_event_id"`
	LastEventTime *time.Time `json:"last_event_time"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// LatencyHistogram stores frequency counts across bounded latency buckets (in ms).
type LatencyHistogram map[string]int64

// CustomerRollup represents a 15-minute aggregated bucket for a customer.
type CustomerRollup struct {
	BucketStart         time.Time        `json:"bucket_start"`
	OrganizationID      uuid.UUID        `json:"organization_id"`
	CustomerID          uuid.UUID        `json:"customer_id"`
	Currency            string           `json:"currency"`
	SubmittedCount      int64            `json:"submitted_count"`
	DeliveredCount      int64            `json:"delivered_count"`
	FailedCount         int64            `json:"failed_count"`
	UndeliveredCount    int64            `json:"undelivered_count"`
	ExpiredCount        int64            `json:"expired_count"`
	UnknownCount        int64            `json:"unknown_count"`
	PartsCount          int64            `json:"parts_count"`
	RevenueCaptured     decimal.Decimal  `json:"revenue_captured"`
	RevenueHeld         decimal.Decimal  `json:"revenue_held"`
	RevenueRefunded     decimal.Decimal  `json:"revenue_refunded"`
	LatencyP50Ms        *int             `json:"latency_p50_ms,omitempty"`
	LatencyP95Ms        *int             `json:"latency_p95_ms,omitempty"`
	LatencyP99Ms        *int             `json:"latency_p99_ms,omitempty"`
	LatencySamplesCount int64            `json:"latency_samples_count"`
	LatencySumMs        int64            `json:"latency_sum_ms"`
	LatencyHistogram    LatencyHistogram `json:"latency_histogram"`
	CreatedAt           time.Time        `json:"created_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
}

// ProviderRollup represents a 15-minute aggregated bucket for an upstream provider connection.
// PLATFORM-ONLY.
type ProviderRollup struct {
	BucketStart         time.Time       `json:"bucket_start"`
	ProviderID          uuid.UUID       `json:"provider_id"`
	ConnectionID        uuid.UUID       `json:"connection_id"`
	Currency            string          `json:"currency"`
	SubmittedCount      int64           `json:"submitted_count"`
	DeliveredCount      int64           `json:"delivered_count"`
	FailedCount         int64           `json:"failed_count"`
	UndeliveredCount    int64           `json:"undelivered_count"`
	ExpiredCount        int64           `json:"expired_count"`
	CostEstimated       decimal.Decimal `json:"cost_estimated"`
	LatencyP50Ms        *int            `json:"latency_p50_ms,omitempty"`
	LatencyP95Ms        *int            `json:"latency_p95_ms,omitempty"`
	LatencySamplesCount int64           `json:"latency_samples_count"`
	LatencySumMs        int64           `json:"latency_sum_ms"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// DestinationRollup represents a 15-minute aggregated bucket by destination country and operator.
type DestinationRollup struct {
	BucketStart      time.Time       `json:"bucket_start"`
	CountryISO       string          `json:"country_iso"`
	OperatorID       uuid.UUID       `json:"operator_id"`
	Currency         string          `json:"currency"`
	SubmittedCount   int64           `json:"submitted_count"`
	DeliveredCount   int64           `json:"delivered_count"`
	FailedCount      int64           `json:"failed_count"`
	UndeliveredCount int64           `json:"undelivered_count"`
	RevenueEstimated decimal.Decimal `json:"revenue_estimated"`
	CostEstimated    decimal.Decimal `json:"cost_estimated"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// ServiceHeartbeat records an active runtime instance's health and metrics.
type ServiceHeartbeat struct {
	Role        string                 `json:"role"`
	InstanceID  string                 `json:"instance_id"`
	Hostname    string                 `json:"hostname"`
	LastSeenAt  time.Time              `json:"last_seen_at"`
	MetricsJSON map[string]interface{} `json:"metrics_json"`
}

// DashboardFilter specifies query parameters for time-series and aggregate reports.
type DashboardFilter struct {
	StartTime    time.Time  `json:"start_time"`
	EndTime      time.Time  `json:"end_time"`
	OrgID        *uuid.UUID `json:"org_id,omitempty"`
	CustomerID   *uuid.UUID `json:"customer_id,omitempty"`
	ProviderID   *uuid.UUID `json:"provider_id,omitempty"`
	ConnectionID *uuid.UUID `json:"connection_id,omitempty"`
	CountryISO   string     `json:"country_iso,omitempty"`
	Currency     string     `json:"currency,omitempty"`
	Timezone     string     `json:"timezone,omitempty"`    // e.g. "Asia/Dhaka", "UTC"
	Granularity  string     `json:"granularity,omitempty"` // "15m", "1h", "1d"
}

// TimeSeriesPoint represents an aggregated data point on a dashboard chart.
type TimeSeriesPoint struct {
	Timestamp     time.Time        `json:"timestamp"`
	Submitted     int64            `json:"submitted"`
	Delivered     int64            `json:"delivered"`
	Failed        int64            `json:"failed"`
	Undelivered   int64            `json:"undelivered"`
	Expired       int64            `json:"expired"`
	DeliveryRate  float64          `json:"delivery_rate"`
	Revenue       decimal.Decimal  `json:"revenue"`
	RevenueHeld   decimal.Decimal  `json:"revenue_held,omitempty"`
	Cost          *decimal.Decimal `json:"cost,omitempty"`           // Platform only
	Margin        *decimal.Decimal `json:"margin,omitempty"`         // Platform only
	MarginPercent *float64         `json:"margin_percent,omitempty"` // Platform only
	LatencyP50Ms  *int             `json:"latency_p50_ms,omitempty"`
	LatencyP95Ms  *int             `json:"latency_p95_ms,omitempty"`
}

// ExecutiveDashboard represents high-level platform KPI summary and trends.
type ExecutiveDashboard struct {
	TotalVolume     int64                      `json:"total_volume"`
	DeliveredVolume int64                      `json:"delivered_volume"`
	FailedVolume    int64                      `json:"failed_volume"`
	DeliveryRate    float64                    `json:"delivery_rate"`
	RevenueByCur    map[string]decimal.Decimal `json:"revenue_by_currency"`
	CostByCur       map[string]decimal.Decimal `json:"cost_by_currency,omitempty"`   // Platform only
	MarginByCur     map[string]decimal.Decimal `json:"margin_by_currency,omitempty"` // Platform only
	LatencyP95Ms    *int                       `json:"latency_p95_ms,omitempty"`
	TimeSeries      []TimeSeriesPoint          `json:"time_series"`
	TopDestinations []DestinationSummary       `json:"top_destinations"`
}

// DestinationSummary gives volume and rate breakdown for a country.
type DestinationSummary struct {
	CountryISO   string  `json:"country_iso"`
	CountryName  string  `json:"country_name,omitempty"`
	Volume       int64   `json:"volume"`
	DeliveryRate float64 `json:"delivery_rate"`
}

// NOCDashboard represents operational and network health statistics for the NOC team.
// Strictly Platform-Only.
type NOCDashboard struct {
	CurrentIngressTPS  float64                 `json:"current_ingress_tps"`
	CurrentEgressTPS   float64                 `json:"current_egress_tps"`
	OutboxDepth        int64                   `json:"outbox_depth"`
	OutboxOldestAgeSec float64                 `json:"outbox_oldest_age_sec"`
	DeliverySuccess24h float64                 `json:"delivery_success_24h"`
	AvgLatencyMs       float64                 `json:"avg_latency_ms"`
	ActiveConnections  []ConnectionHealthPoint `json:"active_connections"`
	FiringAlertsCount  int                     `json:"firing_alerts_count"`
	OpenIncidentsCount int                     `json:"open_incidents_count"`
	Heartbeats         []ServiceHeartbeat      `json:"heartbeats"`
	TimeSeries         []TimeSeriesPoint       `json:"time_series"`
}

// ConnectionHealthPoint represents an upstream connection's current status and performance.
type ConnectionHealthPoint struct {
	ConnectionID   uuid.UUID `json:"connection_id"`
	ConnectionName string    `json:"connection_name"`
	ProviderName   string    `json:"provider_name"`
	Protocol       string    `json:"protocol"`
	Status         string    `json:"status"`
	CurrentTPS     float64   `json:"current_tps"`
	SuccessRate    float64   `json:"success_rate"`
	LatencyMs      int       `json:"latency_ms"`
}

// ResellerDashboard represents reseller-scoped analytics (customers under this reseller).
type ResellerDashboard struct {
	TotalVolume     int64                      `json:"total_volume"`
	DeliveredVolume int64                      `json:"delivered_volume"`
	DeliveryRate    float64                    `json:"delivery_rate"`
	RevenueByCur    map[string]decimal.Decimal `json:"revenue_by_currency"`
	ActiveCustomers int                        `json:"active_customers"`
	TimeSeries      []TimeSeriesPoint          `json:"time_series"`
	TopCustomers    []CustomerSummary          `json:"top_customers"`
}

// CustomerSummary represents a customer's aggregate stats within a reseller or platform view.
type CustomerSummary struct {
	CustomerID   uuid.UUID       `json:"customer_id"`
	CustomerName string          `json:"customer_name"`
	Volume       int64           `json:"volume"`
	DeliveryRate float64         `json:"delivery_rate"`
	Revenue      decimal.Decimal `json:"revenue"`
	Currency     string          `json:"currency"`
}

// CustomerDashboard represents customer-scoped self-service analytics.
type CustomerDashboard struct {
	TotalVolume      int64             `json:"total_volume"`
	DeliveredVolume  int64             `json:"delivered_volume"`
	DeliveryRate     float64           `json:"delivery_rate"`
	TotalSpent       decimal.Decimal   `json:"total_spent"`
	Currency         string            `json:"currency"`
	BalanceRemaining decimal.Decimal   `json:"balance_remaining"`
	TimeSeries       []TimeSeriesPoint `json:"time_series"`
}

// Notification represents an in-app alert or notification.
type Notification struct {
	ID             uuid.UUID  `json:"id"`
	OrganizationID *uuid.UUID `json:"organization_id,omitempty"`
	UserID         *uuid.UUID `json:"user_id,omitempty"`
	Category       string     `json:"category"`
	Severity       string     `json:"severity"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	LinkURL        *string    `json:"link_url,omitempty"`
	ReadAt         *time.Time `json:"read_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// NotificationPreferences represents a user's notification channel choices.
type NotificationPreferences struct {
	ID             uuid.UUID       `json:"id"`
	UserID         uuid.UUID       `json:"user_id"`
	EmailEnabled   bool            `json:"email_enabled"`
	WebhookEnabled bool            `json:"webhook_enabled"`
	WebhookURL     *string         `json:"webhook_url,omitempty"`
	Categories     json.RawMessage `json:"categories"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// AlertRule represents a configured alerting condition.
type AlertRule struct {
	ID              uuid.UUID       `json:"id"`
	Name            string          `json:"name"`
	ConditionType   string          `json:"condition_type"` // e.g. "high_failure_rate", "outbox_age", "discrepancy"
	Threshold       decimal.Decimal `json:"threshold"`
	DurationSeconds int             `json:"duration_seconds"`
	Severity        string          `json:"severity"` // "info", "warning", "critical"
	Scope           string          `json:"scope"`
	Enabled         bool            `json:"enabled"`
	Params          json.RawMessage `json:"params"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// Alert represents an active or resolved alert instance.
type Alert struct {
	ID          uuid.UUID       `json:"id"`
	RuleID      *uuid.UUID      `json:"rule_id,omitempty"`
	Status      string          `json:"status"` // "firing", "resolved", "silenced"
	Severity    string          `json:"severity"`
	Summary     string          `json:"summary"`
	Description string          `json:"description"`
	Labels      json.RawMessage `json:"labels"`
	StartedAt   time.Time       `json:"started_at"`
	ResolvedAt  *time.Time      `json:"resolved_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Incident represents a declared operational incident.
type Incident struct {
	ID         uuid.UUID  `json:"id"`
	Title      string     `json:"title"`
	Severity   string     `json:"severity"` // "minor", "major", "critical"
	Status     string     `json:"status"`   // "investigating", "identified", "monitoring", "resolved"
	LeadUserID *uuid.UUID `json:"lead_user_id,omitempty"`
	RootCause  *string    `json:"root_cause,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// IncidentTimeline represents a timeline entry in an incident investigation.
type IncidentTimeline struct {
	ID           uuid.UUID  `json:"id"`
	IncidentID   uuid.UUID  `json:"incident_id"`
	AuthorUserID *uuid.UUID `json:"author_user_id,omitempty"`
	EntryType    string     `json:"entry_type"` // "status_change", "note", "mitigation"
	Content      string     `json:"content"`
	CreatedAt    time.Time  `json:"created_at"`
}

// SettingItem represents a typed system setting.
type SettingItem struct {
	Key             string          `json:"key"`
	Value           json.RawMessage `json:"value"`
	ValueType       string          `json:"value_type"` // "string", "integer", "boolean", "json"
	Category        string          `json:"category"`
	Description     *string         `json:"description,omitempty"`
	IsSecret        bool            `json:"is_secret"`
	UpdatedByUserID *uuid.UUID      `json:"updated_by_user_id,omitempty"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// SavedFilter represents a user's customized saved filter view.
type SavedFilter struct {
	ID        uuid.UUID       `json:"id"`
	UserID    uuid.UUID       `json:"user_id"`
	ViewName  string          `json:"view_name"`
	Name      string          `json:"filter_name"`
	Query     json.RawMessage `json:"query_json"`
	IsDefault bool            `json:"is_default"`
	CreatedAt time.Time       `json:"created_at"`
}

// UserPreferences represents a user's UI preferences.
type UserPreferences struct {
	UserID          uuid.UUID       `json:"user_id"`
	Theme           string          `json:"theme"`  // "system", "light", "dark"
	Locale          string          `json:"locale"` // "en", "bn"
	Timezone        string          `json:"timezone"`
	DashboardLayout json.RawMessage `json:"dashboard_layout"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// ExportJob represents an asynchronous data export request.
type ExportJob struct {
	ID             uuid.UUID       `json:"id"`
	OrganizationID uuid.UUID       `json:"organization_id"`
	UserID         uuid.UUID       `json:"user_id"`
	ExportType     string          `json:"export_type"` // "messages", "financial", "dlr", "audit"
	Format         string          `json:"format"`      // "csv", "xlsx", "json"
	Status         string          `json:"status"`      // "pending", "processing", "completed", "failed"
	FilterParams   json.RawMessage `json:"filter_params"`
	RowCount       int64           `json:"row_count"`
	FilePath       *string         `json:"file_path,omitempty"`
	ErrorMessage   *string         `json:"error_message,omitempty"`
	StartedAt      *time.Time      `json:"started_at,omitempty"`
	CompletedAt    *time.Time      `json:"completed_at,omitempty"`
	ExpiresAt      *time.Time      `json:"expires_at,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

// DeliveryReportRow provides country/operator delivery performance breakdown.
type DeliveryReportRow struct {
	CountryISO   string  `json:"country_iso"`
	OperatorName string  `json:"operator_name,omitempty"`
	Submitted    int64   `json:"submitted"`
	Delivered    int64   `json:"delivered"`
	Failed       int64   `json:"failed"`
	Undelivered  int64   `json:"undelivered"`
	Expired      int64   `json:"expired"`
	DeliveryRate float64 `json:"delivery_rate"`
	AvgLatencyMs *int    `json:"avg_latency_ms,omitempty"`
}

// DeliveryReport represents the full delivery performance report response.
type DeliveryReport struct {
	PeriodStart    time.Time           `json:"period_start"`
	PeriodEnd      time.Time           `json:"period_end"`
	TotalSubmitted int64               `json:"total_submitted"`
	TotalDelivered int64               `json:"total_delivered"`
	TotalFailed    int64               `json:"total_failed"`
	DeliveryRate   float64             `json:"delivery_rate"`
	Items          []DeliveryReportRow `json:"items"`
}

// FinancialReportRow provides revenue and billing breakdown per customer or interval.
type FinancialReportRow struct {
	Period          time.Time        `json:"period"`
	CustomerID      *uuid.UUID       `json:"customer_id,omitempty"`
	CustomerName    string           `json:"customer_name,omitempty"`
	Currency        string           `json:"currency"`
	Volume          int64            `json:"volume"`
	RevenueCaptured decimal.Decimal  `json:"revenue_captured"`
	RevenueHeld     decimal.Decimal  `json:"revenue_held"`
	RevenueRefunded decimal.Decimal  `json:"revenue_refunded"`
	CostEstimated   *decimal.Decimal `json:"cost_estimated,omitempty"`   // Platform only
	MarginEstimated *decimal.Decimal `json:"margin_estimated,omitempty"` // Platform only
}

// FinancialReport represents the full revenue and billing report response.
type FinancialReport struct {
	PeriodStart       time.Time                  `json:"period_start"`
	PeriodEnd         time.Time                  `json:"period_end"`
	RevenueByCurrency map[string]decimal.Decimal `json:"revenue_by_currency"`
	CostByCurrency    map[string]decimal.Decimal `json:"cost_by_currency,omitempty"` // Platform only
	Items             []FinancialReportRow       `json:"items"`
}

// WidgetDefinition describes an analytics dashboard widget for the registry.
type WidgetDefinition struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name"`
	Description    string                 `json:"description"`
	Category       string                 `json:"category"`        // "kpi", "chart", "table", "gauge"
	SupportedRoles []string               `json:"supported_roles"` // "platform", "reseller", "customer"
	DefaultWidth   int                    `json:"default_width"`   // grid columns 1-12
	DefaultHeight  int                    `json:"default_height"`
	DefaultConfig  map[string]interface{} `json:"default_config"`
}
