package model

import "time"

type EntryKind string

const (
	KindSubscription EntryKind = "subscription"
	KindBalance      EntryKind = "balance"
)

type StatusLevel string

const (
	StatusOK       StatusLevel = "ok"
	StatusWarning  StatusLevel = "warning"
	StatusCritical StatusLevel = "critical"
	StatusStale    StatusLevel = "stale"
	StatusError    StatusLevel = "error"
	StatusDisabled StatusLevel = "disabled"
)

// EntryConfig is persisted in the owner-only local config directory (0600).
// Secrets never leave the server in plaintext API responses.
type EntryConfig struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Provider        string            `json:"provider"`
	Kind            EntryKind         `json:"kind"`
	Badge           string            `json:"badge,omitempty"`
	Enabled         bool              `json:"enabled"`
	Pinned          bool              `json:"pinned"`
	SortOrder       int               `json:"sortOrder"`
	RefreshSeconds  int               `json:"refreshSeconds"`
	DashboardURL    string            `json:"dashboardUrl,omitempty"`
	Notes           string            `json:"notes,omitempty"`

	// Optional recurring cost metadata (for personal subscription burn overview)
	MonthlyCost     *float64          `json:"monthlyCost,omitempty"`
	CostCurrency    string            `json:"costCurrency,omitempty"`

	// Credential & source settings (kept server-side; masked in public views)
	AuthMode        string            `json:"authMode,omitempty"` // "oauth", "token", "file", "keychain", "none"
	Secret          string            `json:"secret,omitempty"`   // API Key / Bearer access token / header value
	RefreshToken    string            `json:"refreshToken,omitempty"`
	TokenExpiresAt  *time.Time        `json:"tokenExpiresAt,omitempty"`
	AccountEmail    string            `json:"accountEmail,omitempty"`
	ProjectID       string            `json:"projectId,omitempty"`
	CredentialFile  string            `json:"credentialFile,omitempty"`
	AccountID       string            `json:"accountId,omitempty"`

	// Endpoint / Custom HTTP / Relay settings
	BaseURL         string            `json:"baseUrl,omitempty"`
	Method          string            `json:"method,omitempty"`
	AuthHeader      string            `json:"authHeader,omitempty"`
	AuthPrefix      string            `json:"authPrefix,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`

	// Custom JSON extractor & Manual entry parameters
	Currency        string            `json:"currency,omitempty"` // "CNY", "USD", etc.
	Scale           float64           `json:"scale,omitempty"`    // Multiplier for raw numeric fields (default 1)
	LowThreshold    *float64          `json:"lowThreshold,omitempty"`
	WarnPercent     *float64          `json:"warnPercent,omitempty"` // Default 75

	// JSON dot-paths for custom_http
	BalancePath     string            `json:"balancePath,omitempty"`
	UsedPath        string            `json:"usedPath,omitempty"`
	LimitPath       string            `json:"limitPath,omitempty"`
	UsedPercentPath string            `json:"usedPercentPath,omitempty"`
	ResetAtPath     string            `json:"resetAtPath,omitempty"`
	PlanPath        string            `json:"planPath,omitempty"`

	// Manual subscription / manual balance fields
	ManualCycle     string            `json:"manualCycle,omitempty"`   // "monthly", "yearly", "weekly", "none"
	BillingDay      int               `json:"billingDay,omitempty"`    // 1..31 for monthly cycle
	NextResetDate   string            `json:"nextResetDate,omitempty"` // YYYY-MM-DD override
	PlanLabel       string            `json:"planLabel,omitempty"`
	QuotaUsed       *float64          `json:"quotaUsed,omitempty"`
	QuotaTotal      *float64          `json:"quotaTotal,omitempty"`
	QuotaUnit       string            `json:"quotaUnit,omitempty"`
	ManualBalance   *float64          `json:"manualBalance,omitempty"`

	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
}

// EntryConfigPublic is safe to send to Web UI / Mac Client for editing.
type EntryConfigPublic struct {
	EntryConfig
	HasSecret       bool   `json:"hasSecret"`
	HasRefreshToken bool   `json:"hasRefreshToken"`
	SecretHint      string `json:"secretHint,omitempty"`
}

func (c EntryConfig) Public() EntryConfigPublic {
	pub := EntryConfigPublic{
		EntryConfig:     c,
		HasSecret:       c.Secret != "" || c.RefreshToken != "" || c.AuthMode == "file" || c.AuthMode == "keychain",
		HasRefreshToken: c.RefreshToken != "",
		SecretHint:      MaskSecret(c.Secret),
	}
	pub.Secret = ""
	pub.RefreshToken = ""
	return pub
}

func MaskSecret(s string) string {
	n := len(s)
	if n == 0 {
		return ""
	}
	if n <= 6 {
		return "••••"
	}
	prefix := s[:3]
	suffix := s[n-4:]
	return prefix + "••••" + suffix
}

// QuotaWindow represents a rolling or cyclical consumption limit (e.g. 5h, 7d, monthly).
type QuotaWindow struct {
	Key              string     `json:"key"`
	Label            string     `json:"label"`
	UsedPercent      float64    `json:"usedPercent"`
	RemainingPercent float64    `json:"remainingPercent"`
	UsedDisplay      string     `json:"usedDisplay,omitempty"`
	ResetsAt         *time.Time `json:"resetsAt,omitempty"`
	WindowSeconds    int64      `json:"windowSeconds,omitempty"`
	PaceDeltaPercent *float64   `json:"paceDeltaPercent,omitempty"` // Positive = burning faster than elapsed window
}

type KVDetail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// BalanceInfo represents prepaid credits / pay-as-you-go account balance.
type BalanceInfo struct {
	Currency     string     `json:"currency"`
	Symbol       string     `json:"symbol"`
	Available    float64    `json:"available"`
	Used         *float64   `json:"used,omitempty"`
	Limit        *float64   `json:"limit,omitempty"`
	LowThreshold *float64   `json:"lowThreshold,omitempty"`
	IsAvailable  *bool      `json:"isAvailable,omitempty"`
	Details      []KVDetail `json:"details,omitempty"`
}

// EntrySnapshot is the unified live/cached state rendered by the menu bar app and the web UI.
type EntrySnapshot struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Provider       string        `json:"provider"`
	ProviderLabel  string        `json:"providerLabel"`
	Kind           EntryKind     `json:"kind"`
	Badge          string        `json:"badge"`
	Enabled        bool          `json:"enabled"`
	Pinned         bool          `json:"pinned"`
	SortOrder      int           `json:"sortOrder"`
	PlanLabel      string        `json:"planLabel,omitempty"`
	AccountEmail   string        `json:"accountEmail,omitempty"`
	DashboardURL   string        `json:"dashboardUrl,omitempty"`
	Notes          string        `json:"notes,omitempty"`
	MonthlyCost    *float64      `json:"monthlyCost,omitempty"`
	CostCurrency   string        `json:"costCurrency,omitempty"`

	Status         StatusLevel   `json:"status"`
	StatusReason   string        `json:"statusReason,omitempty"`
	PrimaryMetric  string        `json:"primaryMetric"` // Compact summary e.g. "剩余 72%" or "¥48.50"

	Windows        []QuotaWindow `json:"windows,omitempty"`
	Balance        *BalanceInfo  `json:"balance,omitempty"`

	FetchedAt      *time.Time    `json:"fetchedAt,omitempty"`
	NextRefreshAt  *time.Time    `json:"nextRefreshAt,omitempty"`
	Error          string        `json:"error,omitempty"`
}

type CurrencyTotal struct {
	Currency string  `json:"currency"`
	Symbol   string  `json:"symbol"`
	Amount   float64 `json:"amount"`
}

type SummaryStats struct {
	TotalEntries       int             `json:"totalEntries"`
	EnabledEntries     int             `json:"enabledEntries"`
	SubscriptionCount  int             `json:"subscriptionCount"`
	BalanceCount       int             `json:"balanceCount"`
	WarningCount       int             `json:"warningCount"`
	CriticalCount      int             `json:"criticalCount"`
	AvailableBalances  []CurrencyTotal `json:"availableBalances"`
	MonthlyFixedCosts  []CurrencyTotal `json:"monthlyFixedCosts"`
}

type SnapshotResponse struct {
	GeneratedAt time.Time       `json:"generatedAt"`
	Summary     SummaryStats    `json:"summary"`
	Entries     []EntrySnapshot `json:"entries"`
}

// ProviderFieldSchema describes a form input for independent entry creation in Web & Mac UI.
type ProviderFieldSchema struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"` // "text", "secret", "number", "select"
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Default     string   `json:"default,omitempty"`
	Options     []Option `json:"options,omitempty"`
}

type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type ProviderDescriptor struct {
	ID                    string                `json:"id"`
	Name                  string                `json:"name"`
	DefaultBadge          string                `json:"defaultBadge"`
	DefaultKind           EntryKind             `json:"defaultKind"`
	SupportsKindSwitch    bool                  `json:"supportsKindSwitch,omitempty"`
	SupportsOAuthFlow     bool                  `json:"supportsOAuthFlow,omitempty"`
	OAuthButtonLabel      string                `json:"oauthButtonLabel,omitempty"`
	DefaultRefreshSeconds int                   `json:"defaultRefreshSeconds"`
	DefaultDashboardURL   string                `json:"defaultDashboardUrl,omitempty"`
	Description           string                `json:"description"`
	Fields                []ProviderFieldSchema `json:"fields"`
}

type DiscoveryHint struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	SuggestedName string `json:"suggestedName"`
	Note          string `json:"note"`
}

func CurrencySymbol(code string) string {
	switch code {
	case "CNY", "RMB":
		return "¥"
	case "USD":
		return "$"
	case "EUR":
		return "€"
	case "HKD":
		return "HK$"
	case "JPY":
		return "JP¥"
	default:
		if code == "" {
			return "$"
		}
		return code + " "
	}
}
