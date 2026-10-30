package agentpaypayer

import (
	"errors"
	"strings"
	"testing"
)

func TestConfigValidateWalletAuthModes(t *testing.T) {
	base := Config{
		WalletBaseURL:   "https://wallet.example",
		AgentPayBaseURL: "https://agentpay.example",
		WalletID:        "wal_1",
	}

	t.Run("project requires APIKey", func(t *testing.T) {
		cfg := base
		cfg.WalletAuthMode = WalletAuthProject
		if err := cfg.validate(); !errors.Is(err, ErrConfigInvalid) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("blank APIKey without public mode is not public", func(t *testing.T) {
		cfg := base
		if err := cfg.validate(); !errors.Is(err, ErrConfigInvalid) || !strings.Contains(err.Error(), string(WalletAuthPublic)) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("public forbids wallet APIKey", func(t *testing.T) {
		cfg := base
		cfg.WalletAuthMode = WalletAuthPublic
		cfg.APIKey = "should-not-be-set"
		if err := cfg.validate(); !errors.Is(err, ErrConfigInvalid) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("public allows empty APIKey", func(t *testing.T) {
		cfg := base
		cfg.WalletAuthMode = WalletAuthPublic
		if err := cfg.validate(); err != nil {
			t.Fatal(err)
		}
		if cfg.walletAPIKey() != "" {
			t.Fatalf("wallet=%q", cfg.walletAPIKey())
		}
	})

	t.Run("project wallet APIKey only for Wallet", func(t *testing.T) {
		cfg := base
		cfg.APIKey = "wallet-key"
		if err := cfg.validate(); err != nil {
			t.Fatal(err)
		}
		if cfg.walletAPIKey() != "wallet-key" {
			t.Fatalf("wallet=%q", cfg.walletAPIKey())
		}
	})

	t.Run("unknown mode rejected", func(t *testing.T) {
		cfg := base
		cfg.WalletAuthMode = "legacy"
		cfg.APIKey = "k"
		if err := cfg.validate(); !errors.Is(err, ErrConfigInvalid) {
			t.Fatalf("err=%v", err)
		}
	})
}
