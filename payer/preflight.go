package agentpaypayer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type preflightClient struct {
	baseURL string
	http    *http.Client
}

type preflightRequest struct {
	PaymentRequired    json.RawMessage `json:"payment_required"`
	PayerAddress       string          `json:"payer_address"`
	AccountType        string          `json:"account_type"`
	WalletID           string          `json:"wallet_id"`
	ExternalAgentID    string          `json:"external_agent_id"`
	ServiceID          string          `json:"service_id"`
	ConfigVersion      int             `json:"config_version"`
	RequestBindingHash string          `json:"request_binding_hash"`
}

type preflightResult struct {
	Ready                 bool
	Token                 string
	RecoveryActions       []string
	WalletReadinessHint   json.RawMessage
}

func (c *preflightClient) check(ctx context.Context, in preflightRequest) (preflightResult, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return preflightResult{}, wrapError(CodePreflightUnavailable, ErrPreflightUnavailable, "encode preflight body: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.baseURL, "/")+"/v1/payments:preflight", bytes.NewReader(body))
	if err != nil {
		return preflightResult{}, wrapError(CodePreflightUnavailable, ErrPreflightUnavailable, "build preflight request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return preflightResult{}, wrapError(CodePreflightUnavailable, ErrPreflightUnavailable, "preflight transport: %v", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 256<<10))
	if err != nil {
		return preflightResult{}, wrapError(CodePreflightUnavailable, ErrPreflightUnavailable, "read preflight body: %v", err)
	}
	var envelope struct {
		Data struct {
			Ready               bool            `json:"ready"`
			PaymentContextToken string          `json:"payment_context_token"`
			RecoveryActions     []struct {
				Type string `json:"type"`
			} `json:"recovery_actions"`
			WalletReadinessHint json.RawMessage `json:"wallet_readiness_hint"`
		} `json:"data"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return preflightResult{}, wrapError(CodePreflightUnavailable, ErrPreflightUnavailable, "decode preflight body: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		msg := envelope.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("AgentPay preflight HTTP %d", res.StatusCode)
		}
		if res.StatusCode >= 400 && res.StatusCode < 500 {
			return preflightResult{}, wrapError(CodePreflightBlocked, ErrPreflightBlocked, "%s", msg)
		}
		return preflightResult{}, wrapError(CodePreflightUnavailable, ErrPreflightUnavailable, "%s", msg)
	}
	actions := make([]string, 0, len(envelope.Data.RecoveryActions))
	for _, a := range envelope.Data.RecoveryActions {
		if a.Type != "" {
			actions = append(actions, a.Type)
		}
	}
	return preflightResult{
		Ready:               envelope.Data.Ready,
		Token:               strings.TrimSpace(envelope.Data.PaymentContextToken),
		RecoveryActions:     actions,
		WalletReadinessHint: envelope.Data.WalletReadinessHint,
	}, nil
}

func (r preflightResult) needsPermit2Approval() bool {
	for _, a := range r.RecoveryActions {
		if a == "APPROVE_PERMIT2" {
			return true
		}
	}
	return false
}
