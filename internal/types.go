package buzz

import (
	"net/http"
	"net/url"
	"sync"
	"time"
)

var corsHeaders = map[string]string{
	"Access-Control-Allow-Origin":  "*",
	"Access-Control-Allow-Methods": "GET, HEAD, POST, PUT, DELETE, OPTIONS",
	"Access-Control-Allow-Headers": "Content-Type, Authorization, X-Api-Key, X-Goog-Api-Key, Anthropic-Version, Anthropic-Beta",
	"X-Proxy-Version":              "local-go-v1",
}

const (
	adminSessionTTL         = 7 * 24 * time.Hour
	adminSessionRenewBefore = 3 * 24 * time.Hour
)

type Config struct {
	Server struct {
		Addr string `yaml:"addr"`
	} `yaml:"server"`
	Upstream struct {
		BaseURL string `yaml:"base_url"`
		Timeout string `yaml:"timeout"`
	} `yaml:"upstream"`
	Database DatabaseConfig `yaml:"database"`
	Redis    RedisConfig    `yaml:"redis"`
	Auth     struct {
		Tokens []AuthToken `yaml:"tokens"`
	} `yaml:"auth"`
	Retry struct {
		MaxAttempts     int `yaml:"max_attempts"`
		CooldownSeconds int `yaml:"cooldown_seconds"`
	} `yaml:"retry"`
}

type AuthToken struct {
	ID                   int64     `yaml:"-" json:"id"`
	UserID               int64     `yaml:"-" json:"user_id"`
	UserName             string    `yaml:"-" json:"user_name,omitempty"`
	Name                 string    `yaml:"name" json:"name"`
	Token                string    `yaml:"token,omitempty" json:"token,omitempty"`
	Valid                bool      `yaml:"valid" json:"valid"`
	WeeklyQuotaCredits   int64     `yaml:"-" json:"-"`
	LifetimeQuotaCredits int64     `yaml:"-" json:"-"`
	QuotaAnchor          time.Time `yaml:"-" json:"-"`
}

type UserAPIKeyDetails struct {
	ID          int64      `json:"id"`
	UserID      int64      `json:"user_id"`
	Name        string     `json:"name"`
	Token       string     `json:"token,omitempty"`
	Valid       bool       `json:"valid"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	Requests    int64      `json:"requests"`
	TotalTokens int64      `json:"total_tokens"`
}

type APIKey struct {
	ID                   int64  `yaml:"-" json:"id"`
	ProviderID           int64  `yaml:"-" json:"provider_id,omitempty"`
	ProviderName         string `yaml:"-" json:"provider_name,omitempty"`
	ProviderType         string `yaml:"-" json:"provider_type,omitempty"`
	ProviderKeyID        int64  `yaml:"-" json:"provider_key_id,omitempty"`
	Name                 string `yaml:"name" json:"name"`
	Key                  string `yaml:"key" json:"key,omitempty"`
	Enabled              bool   `yaml:"-" json:"enabled"`
	DisabledStatus       int    `yaml:"-" json:"disabled_status,omitempty"`
	DisabledErrorCode    string `yaml:"-" json:"disabled_error_code,omitempty"`
	DisabledErrorMessage string `yaml:"-" json:"disabled_error_message,omitempty"`
	DisabledErrorBody    string `yaml:"-" json:"disabled_error_body,omitempty"`
	DisabledAt           string `yaml:"-" json:"disabled_at,omitempty"`
}

type KeyError struct {
	Key       string `json:"key"`
	Model     string `json:"model"`
	Status    int    `json:"status"`
	Message   string `json:"message"`
	UpdatedAt string `json:"updated_at"`
}

type Stats struct {
	StartedAt   time.Time           `json:"started_at"`
	Requests    int64               `json:"requests"`
	ByUser      map[string]int64    `json:"by_user"`
	ByKey       map[string]int64    `json:"by_key"`
	Exhausted   map[string]string   `json:"exhausted"`
	RPDLike     map[string]bool     `json:"rpd_like"`
	KeyErrors   map[string]KeyError `json:"key_errors"`
	LastUpdated time.Time           `json:"last_updated"`
}

type KeyState struct {
	keys         []APIKey
	next         int
	cooldown     time.Duration
	rpdCooldown  time.Duration
	exhausted    map[string]time.Time
	cooldownHits map[string]int
	rpdLike      map[string]bool
	errors       map[string]KeyError
	mu           sync.Mutex
}

type Server struct {
	cfg           Config
	adminDir      string
	store         *Store
	runtimeCache  *RuntimeCache
	adminAPI      http.Handler
	upstream      *url.URL
	client        *http.Client
	providers     map[string]Provider
	authTokens    map[string]AuthToken
	adminSessions map[string]SessionUser
	routeNext     map[string]int
	routeSessions map[string]RouteSession
	keyState      *KeyState
	usageCh       chan UsageRecord
	stats         Stats
	statsMu       sync.Mutex
	routeMu       sync.Mutex
	runtimeMu     sync.Mutex
	toolSigMu     sync.Mutex
	toolSigs      map[string]toolSignatureEntry
}

type toolSignatureEntry struct {
	signature string
	expiresAt time.Time
	updatedAt time.Time
}

type AdminConfig struct {
	Addr            string   `json:"addr"`
	UpstreamBaseURL string   `json:"upstream_base_url"`
	Timeout         string   `json:"timeout"`
	MaxAttempts     int      `json:"max_attempts"`
	CooldownSeconds int      `json:"cooldown_seconds"`
	Tokens          []string `json:"tokens"`
}

type AdminData struct {
	Config      AdminConfig `json:"config"`
	Users       []AppUser   `json:"users"`
	UserAPIKeys []AuthToken `json:"user_api_keys"`
}

type AppUser struct {
	ID                   int64     `json:"id"`
	Username             string    `json:"username"`
	Role                 string    `json:"role"`
	Valid                bool      `json:"valid"`
	WeeklyQuotaCredits   int64     `json:"weekly_quota_credits"`
	LifetimeQuotaCredits int64     `json:"lifetime_quota_credits"`
	QuotaAnchor          time.Time `json:"quota_anchor_at"`
	CreatedAt            time.Time `json:"created_at"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
