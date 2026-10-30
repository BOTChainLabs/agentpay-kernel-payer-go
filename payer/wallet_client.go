package agentpaypayer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type walletClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

type walletRecord struct {
	WalletID        string `json:"wallet_id"`
	ExternalAgentID string `json:"external_agent_id"`
	AccountAddress  string `json:"account_address"`
	ChainID         int64  `json:"chain_id"`
	Status          string `json:"status"`
	DeploymentStatus string `json:"deployment_status"`
}

type approvalResponse struct {
	ApprovalToken string `json:"approval_token"`
	ApprovalID    string `json:"approval_id"`
	ExpiresAt     string `json:"expires_at"`
	Replayed      bool   `json:"replayed"`
}

type authorizationResponse struct {
	AuthorizationID string          `json:"authorization_id"`
	Status          string          `json:"status"`
	PaymentPayload  json.RawMessage `json:"payment_payload"`
	ExpiresAt       time.Time       `json:"expires_at"`
	Replayed        bool            `json:"replayed"`
	ErrorCode       string          `json:"error_code"`
	ErrorMessage    string          `json:"error_message"`
	UserOpHash      string          `json:"user_op_hash"`
}

func (c *walletClient) setAPIKey(req *http.Request) {
	if key := strings.TrimSpace(c.apiKey); key != "" {
		req.Header.Set("X-API-Key", key)
	}
}

func (c *walletClient) getWallet(ctx context.Context, walletID string) (walletRecord, error) {
	u := strings.TrimRight(c.baseURL, "/") + "/v1/wallets/" + url.PathEscape(walletID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return walletRecord{}, wrapError(CodeWalletUnavailable, ErrWalletUnavailable, "build wallet GET: %v", err)
	}
	c.setAPIKey(req)
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return walletRecord{}, wrapError(CodeWalletUnavailable, ErrWalletUnavailable, "wallet GET transport: %v", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return walletRecord{}, wrapError(CodeWalletUnavailable, ErrWalletUnavailable, "read wallet GET: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		return walletRecord{}, decodeWalletError(res.StatusCode, raw, CodeWalletUnavailable, ErrWalletUnavailable)
	}
	var w walletRecord
	if err := json.Unmarshal(raw, &w); err != nil {
		return walletRecord{}, wrapError(CodeWalletUnavailable, ErrWalletUnavailable, "decode wallet GET: %v", err)
	}
	if strings.TrimSpace(w.AccountAddress) == "" {
		return walletRecord{}, wrapError(CodeWalletNotReady, ErrWalletUnavailable, "wallet has no account_address")
	}
	return w, nil
}

func (c *walletClient) createApproval(ctx context.Context, walletID, idempotencyKey, contextToken string, consent Consent) (approvalResponse, error) {
	body, _ := json.Marshal(map[string]any{
		"payment_context_token": contextToken,
		"consent": map[string]string{
			"type":        consent.Type,
			"evidence_id": consent.EvidenceID,
		},
	})
	u := strings.TrimRight(c.baseURL, "/") + "/v1/wallets/" + url.PathEscape(walletID) + "/payment-approvals"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return approvalResponse{}, wrapError(CodeApprovalFailed, ErrApprovalFailed, "build payment-approvals: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.setAPIKey(req)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	res, err := c.http.Do(req)
	if err != nil {
		return approvalResponse{}, wrapError(CodeApprovalFailed, ErrApprovalFailed, "payment-approvals transport: %v", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 128<<10))
	if err != nil {
		return approvalResponse{}, wrapError(CodeApprovalFailed, ErrApprovalFailed, "read payment-approvals: %v", err)
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return approvalResponse{}, decodeWalletError(res.StatusCode, raw, CodeApprovalFailed, ErrApprovalFailed)
	}
	var out approvalResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return approvalResponse{}, wrapError(CodeApprovalFailed, ErrApprovalFailed, "decode payment-approvals: %v", err)
	}
	if strings.TrimSpace(out.ApprovalToken) == "" {
		return approvalResponse{}, wrapError(CodeApprovalFailed, ErrApprovalFailed, "payment-approvals returned an empty token")
	}
	return out, nil
}

func (c *walletClient) createAuthorization(ctx context.Context, walletID, idempotencyKey, contextToken, approvalToken string, autoSetup bool) (authorizationResponse, error) {
	body, _ := json.Marshal(map[string]any{
		"payment_context_token": contextToken,
		"approval_token":        approvalToken,
		"auto_setup":            autoSetup,
	})
	u := strings.TrimRight(c.baseURL, "/") + "/v1/wallets/" + url.PathEscape(walletID) + "/payment-authorizations"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return authorizationResponse{}, wrapError(CodeAuthorizationFailed, ErrAuthorizationFailed, "build payment-authorizations: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.setAPIKey(req)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	res, err := c.http.Do(req)
	if err != nil {
		return authorizationResponse{}, wrapError(CodeAuthorizationFailed, ErrAuthorizationFailed, "payment-authorizations transport: %v", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 256<<10))
	if err != nil {
		return authorizationResponse{}, wrapError(CodeAuthorizationFailed, ErrAuthorizationFailed, "read payment-authorizations: %v", err)
	}
	var out authorizationResponse
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode == http.StatusAccepted || out.ErrorCode == CodeSetupInProgress || out.Status == CodeSetupInProgress {
		return out, wrapError(CodeSetupInProgress, ErrSetupInProgress, "%s", firstNonEmpty(out.ErrorMessage, "permit2 setup is still in progress"))
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return authorizationResponse{}, decodeWalletError(res.StatusCode, raw, CodeAuthorizationFailed, ErrAuthorizationFailed)
	}
	if out.Status != "SIGNED" && out.Status != "PRESENTED" && out.Status != "CONSUMED" {
		return authorizationResponse{}, wrapError(CodeAuthorizationFailed, ErrAuthorizationFailed, "unexpected authorization status %q", out.Status)
	}
	if strings.TrimSpace(out.AuthorizationID) == "" || !json.Valid(out.PaymentPayload) {
		return authorizationResponse{}, wrapError(CodeAuthorizationFailed, ErrAuthorizationFailed, "payment-authorizations did not return a signed payload")
	}
	return out, nil
}

func (c *walletClient) ensureReadiness(ctx context.Context, walletID, idempotencyKey string, in ReadyInput) (ReadyResult, error) {
	body, _ := json.Marshal(map[string]any{
		"chain_id": in.ChainID,
		"asset":    in.Asset,
		"receiver": in.Receiver,
	})
	u := strings.TrimRight(c.baseURL, "/") + "/v1/wallets/" + url.PathEscape(walletID) + "/payment-readiness"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return ReadyResult{}, wrapError(CodeWalletUnavailable, ErrWalletUnavailable, "build payment-readiness: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	c.setAPIKey(req)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	res, err := c.http.Do(req)
	if err != nil {
		return ReadyResult{}, wrapError(CodeWalletUnavailable, ErrWalletUnavailable, "payment-readiness transport: %v", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return ReadyResult{}, wrapError(CodeWalletUnavailable, ErrWalletUnavailable, "read payment-readiness: %v", err)
	}
	var out ReadyResult
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode == http.StatusAccepted || out.Status == "SETUP_IN_PROGRESS" {
		return out, wrapError(CodeSetupInProgress, ErrSetupInProgress, "payment readiness setup is still in progress")
	}
	if res.StatusCode != http.StatusOK {
		return ReadyResult{}, decodeWalletError(res.StatusCode, raw, CodeWalletUnavailable, ErrWalletUnavailable)
	}
	if out.Status == "" {
		out.Status = "READY"
	}
	return out, nil
}

func decodeWalletError(status int, raw []byte, fallbackCode string, sentinel error) error {
	var body struct {
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
		Error        string `json:"error"`
	}
	_ = json.Unmarshal(raw, &body)
	msg := firstNonEmpty(body.ErrorMessage, body.Error, fmt.Sprintf("wallet HTTP %d", status))
	code := body.ErrorCode
	if code == "" {
		code = fallbackCode
	}
	switch code {
	case CodeSetupInProgress:
		return wrapError(code, ErrSetupInProgress, "%s", msg)
	case CodeFundRequired:
		return wrapError(code, sentinel, "%s", msg)
	case CodeWalletNotReady:
		return wrapError(code, sentinel, "%s", msg)
	default:
		return wrapError(code, sentinel, "%s", msg)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
