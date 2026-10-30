package agentpaypayer

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	defaultSetupMaxAttempts = 8
	defaultSetupRetryWait   = 2 * time.Second
	defaultHTTPTimeout      = 60 * time.Second
)

// WalletAuthMode selects how Wallet HTTP calls are authenticated.
// An empty value means WalletAuthProject. Public wallets must set
// WalletAuthPublic explicitly; a blank APIKey alone is never treated as public.
type WalletAuthMode string

const (
	WalletAuthProject WalletAuthMode = "project"
	WalletAuthPublic  WalletAuthMode = "public"
)

// Config is the frozen public surface for constructing a Kernel payer.
// Optional fields below the required URLs/IDs tune auth, HTTP, and Kernel
// preflight binding; leave them empty for defaults.
type Config struct {
	WalletBaseURL   string
	AgentPayBaseURL string
	WalletID        string

	// WalletAuthMode defaults to WalletAuthProject when empty.
	WalletAuthMode WalletAuthMode

	// APIKey is the Wallet project X-API-Key. Required for WalletAuthProject.
	// Must be empty for WalletAuthPublic (Wallet calls omit X-API-Key).
	// Never sent to AgentPay Preflight.
	APIKey string

	// HTTPClient is optional. When nil, a client with a 60s timeout is used.
	HTTPClient *http.Client

	// KernelAddress is the Kernel account used as payer_address in Preflight.
	// When empty, the SDK loads it from GET /v1/wallets/{WalletID}.
	KernelAddress string

	// ExternalAgentID is required by AgentPay Kernel Preflight. When empty,
	// the SDK loads it from the wallet record.
	ExternalAgentID string

	// ConsentType defaults to EXPLICIT. ConsentEvidenceID defaults to the
	// payment intent id generated for each FetchWithPayment attempt.
	ConsentType       string
	ConsentEvidenceID string

	// SetupMaxAttempts / SetupRetryWait bound SETUP_IN_PROGRESS retries for
	// payment-authorizations and payment-readiness. Defaults: 8 × 2s.
	SetupMaxAttempts int
	SetupRetryWait   time.Duration
}

func (c Config) validate() error {
	if strings.TrimSpace(c.WalletBaseURL) == "" {
		return fmt.Errorf("%w: WalletBaseURL is required", ErrConfigInvalid)
	}
	if strings.TrimSpace(c.AgentPayBaseURL) == "" {
		return fmt.Errorf("%w: AgentPayBaseURL is required", ErrConfigInvalid)
	}
	if strings.TrimSpace(c.WalletID) == "" {
		return fmt.Errorf("%w: WalletID is required", ErrConfigInvalid)
	}
	mode, err := c.resolvedWalletAuthMode()
	if err != nil {
		return err
	}
	apiKey := strings.TrimSpace(c.APIKey)
	switch mode {
	case WalletAuthProject:
		if apiKey == "" {
			return fmt.Errorf("%w: APIKey is required for project Wallet auth (set WalletAuthMode=%q for public wallets)", ErrConfigInvalid, WalletAuthPublic)
		}
	case WalletAuthPublic:
		if apiKey != "" {
			return fmt.Errorf("%w: APIKey must be empty when WalletAuthMode=%q", ErrConfigInvalid, WalletAuthPublic)
		}
	}
	return nil
}

func (c Config) resolvedWalletAuthMode() (WalletAuthMode, error) {
	switch WalletAuthMode(strings.ToLower(strings.TrimSpace(string(c.WalletAuthMode)))) {
	case "", WalletAuthProject:
		return WalletAuthProject, nil
	case WalletAuthPublic:
		return WalletAuthPublic, nil
	default:
		return "", fmt.Errorf("%w: unknown WalletAuthMode %q", ErrConfigInvalid, c.WalletAuthMode)
	}
}

func (c Config) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: defaultHTTPTimeout}
}

func (c Config) walletAPIKey() string {
	mode, err := c.resolvedWalletAuthMode()
	if err != nil || mode == WalletAuthPublic {
		return ""
	}
	return strings.TrimSpace(c.APIKey)
}

func (c Config) setupAttempts() int {
	if c.SetupMaxAttempts > 0 {
		return c.SetupMaxAttempts
	}
	return defaultSetupMaxAttempts
}

func (c Config) setupWait() time.Duration {
	if c.SetupRetryWait > 0 {
		return c.SetupRetryWait
	}
	return defaultSetupRetryWait
}

func (c Config) consentType() string {
	if v := strings.TrimSpace(c.ConsentType); v != "" {
		return v
	}
	return "EXPLICIT"
}
