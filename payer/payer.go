package agentpaypayer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client implements Payer against Wallet + AgentPay HTTP APIs.
// A Client is safe for concurrent use by multiple goroutines.
type Client struct {
	cfg       Config
	http      *http.Client
	wallet    *walletClient
	preflight *preflightClient
}

// New validates Config and returns a Client.
func New(cfg Config) (*Client, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	httpClient := cfg.httpClient()
	return &Client{
		cfg:  cfg,
		http: httpClient,
		wallet: &walletClient{
			baseURL: strings.TrimRight(cfg.WalletBaseURL, "/"),
			apiKey:  cfg.walletAPIKey(),
			http:    httpClient,
		},
		preflight: &preflightClient{
			baseURL: strings.TrimRight(cfg.AgentPayBaseURL, "/"),
			http:    httpClient,
		},
	}, nil
}

// EnsureReady calls POST .../payment-readiness. It may return SETUP_IN_PROGRESS;
// callers should reuse the same logical readiness key via repeated EnsureReady
// with the same ReadyInput (the SDK derives a stable Idempotency-Key).
func (c *Client) EnsureReady(ctx context.Context, in ReadyInput) (ReadyResult, error) {
	if c == nil {
		return ReadyResult{}, wrapError(CodeConfigInvalid, ErrConfigInvalid, "nil client")
	}
	if in.ChainID <= 0 || strings.TrimSpace(in.Asset) == "" || strings.TrimSpace(in.Receiver) == "" {
		return ReadyResult{}, wrapError(CodeConfigInvalid, ErrConfigInvalid, "chain_id, asset, and receiver are required")
	}
	key := fmt.Sprintf("payment-ready:%s:%d:%s", c.cfg.WalletID, in.ChainID, strings.ToLower(strings.TrimSpace(in.Asset)))
	if len(key) > 128 {
		key = key[:128]
	}
	var last ReadyResult
	var lastErr error
	for attempt := 0; attempt < c.cfg.setupAttempts(); attempt++ {
		last, lastErr = c.wallet.ensureReadiness(ctx, c.cfg.WalletID, key, in)
		if lastErr == nil {
			return last, nil
		}
		if !errors.Is(lastErr, ErrSetupInProgress) {
			return ReadyResult{}, lastErr
		}
		if err := sleep(ctx, c.cfg.setupWait()); err != nil {
			return last, lastErr
		}
	}
	if lastErr != nil {
		return last, lastErr
	}
	return last, wrapError(CodeSetupInProgress, ErrSetupInProgress, "payment readiness timed out")
}

// FetchWithPayment sends req; on 402 it runs Preflight → payment-approvals →
// payment-authorizations(auto_setup) and retries with PAYMENT-SIGNATURE.
// Non-402 responses return attempt=nil. pending/unknown responses still return
// the attempt so callers can Retry without creating a new intent.
func (c *Client) FetchWithPayment(ctx context.Context, req *http.Request) (*http.Response, *PaymentAttempt, error) {
	if c == nil {
		return nil, nil, wrapError(CodeConfigInvalid, ErrConfigInvalid, "nil client")
	}
	if req == nil {
		return nil, nil, wrapError(CodeConfigInvalid, ErrConfigInvalid, "request is required")
	}
	snap, err := snapshotRequest(req)
	if err != nil {
		return nil, nil, err
	}
	res, err := c.doSnapshot(ctx, snap, "")
	if err != nil {
		return nil, nil, err
	}
	if res.StatusCode != http.StatusPaymentRequired {
		return res, nil, nil
	}
	_ = res.Body.Close()

	pr, requiredRaw, err := parsePaymentRequiredHeader(res.Header.Get(PaymentRequiredHeader))
	if err != nil {
		return nil, nil, err
	}
	accept, err := assertCanonicalProfile(pr)
	if err != nil {
		return nil, nil, err
	}
	if !resourceURLMatchesRequest(pr.Resource.URL, snap.URL) {
		return nil, nil, wrapError(CodePaymentResourceMismatch, ErrPaymentResourceMismatch,
			"resource.url %q does not match request URL %q", pr.Resource.URL, snap.URL)
	}

	kernelAddress, externalAgentID, err := c.walletIdentity(ctx)
	if err != nil {
		return nil, nil, err
	}

	serviceID := serviceIDFromExtra(accept.Extra)
	configVersion := configVersionFromExtra(accept.Extra)
	binding := requestBindingHash(serviceID, configVersion, snap.Method, snap.URL, snap.Body, snap.Header)

	contextToken, err := c.obtainPaymentContext(ctx, requiredRaw, accept, serviceID, configVersion, binding, kernelAddress, externalAgentID)
	if err != nil {
		return nil, nil, err
	}

	intentID := newIntentID()
	consent := Consent{
		Type:       c.cfg.consentType(),
		EvidenceID: firstNonEmpty(strings.TrimSpace(c.cfg.ConsentEvidenceID), intentID),
	}
	approvalKey := "payment-approval:" + intentID
	if len(approvalKey) > 128 {
		approvalKey = approvalKey[:128]
	}
	approval, err := c.wallet.createApproval(ctx, c.cfg.WalletID, approvalKey, contextToken, consent)
	if err != nil {
		return nil, nil, err
	}

	auth, err := c.authorizeWithSetupRetry(ctx, intentID, contextToken, approval.ApprovalToken)
	if err != nil {
		return nil, nil, err
	}
	signature := base64.StdEncoding.EncodeToString(auth.PaymentPayload)
	attempt := &PaymentAttempt{
		Method:          snap.Method,
		URL:             snap.URL,
		Body:            append([]byte(nil), snap.Body...),
		Header:          snap.Header.Clone(),
		IdempotencyKey:  intentID,
		AuthorizationID: auth.AuthorizationID,
		PaymentPayload:  append(json.RawMessage(nil), auth.PaymentPayload...),
		SignatureHeader: signature,
	}
	paid, err := c.doSnapshot(ctx, snap, signature)
	if err != nil {
		return nil, attempt, wrapError(CodePaymentUnknown, ErrPaymentUnknown, "paid request transport failed: %v", err)
	}
	return paid, attempt, nil
}

// Retry replays the stored PAYMENT-SIGNATURE against the original request.
// It never starts a new Preflight, approval, or idempotency key.
func (c *Client) Retry(ctx context.Context, attempt *PaymentAttempt) (*http.Response, error) {
	if c == nil {
		return nil, wrapError(CodeConfigInvalid, ErrConfigInvalid, "nil client")
	}
	if attempt == nil || strings.TrimSpace(attempt.SignatureHeader) == "" || strings.TrimSpace(attempt.URL) == "" {
		return nil, wrapError(CodeRetryInvalid, ErrRetryInvalid, "attempt is missing a stored signature")
	}
	snap := requestSnapshot{
		Method: firstNonEmpty(attempt.Method, http.MethodPost),
		URL:    attempt.URL,
		Body:   attempt.Body,
		Header: applyEffectiveHeaders(attempt.Header, attempt.Body),
	}
	res, err := c.doSnapshot(ctx, snap, attempt.SignatureHeader)
	if err != nil {
		return nil, wrapError(CodePaymentUnknown, ErrPaymentUnknown, "retry transport failed: %v", err)
	}
	return res, nil
}

func (c *Client) obtainPaymentContext(ctx context.Context, required json.RawMessage, accept paymentAccept, serviceID string, configVersion int, binding, kernelAddress, externalAgentID string) (string, error) {
	result, err := c.preflight.check(ctx, preflightRequest{
		PaymentRequired:    required,
		PayerAddress:       kernelAddress,
		AccountType:        "KERNEL",
		WalletID:           c.cfg.WalletID,
		ExternalAgentID:    externalAgentID,
		ServiceID:          serviceID,
		ConfigVersion:      configVersion,
		RequestBindingHash: binding,
	})
	if err != nil {
		return "", err
	}
	// Hint is observational only; never used as READY authority or to craft Approve.
	_ = result.WalletReadinessHint

	if result.Ready && result.Token != "" {
		return result.Token, nil
	}
	if result.Token != "" {
		// Token present without ready is unexpected today; prefer the token and
		// let payment-authorizations(auto_setup) finish Permit2 setup.
		return result.Token, nil
	}
	if !result.needsPermit2Approval() {
		return "", wrapError(CodePreflightBlocked, ErrPreflightBlocked, "AgentPay preflight did not return a payment_context_token")
	}

	// Current AgentPay only mints context after allowance is ready. Use the
	// Wallet payment-readiness facade (not raw permit2-approval) once, then
	// Preflight again. auto_setup still covers races after the token exists.
	chainID, err := chainIDFromNetwork(accept.Network)
	if err != nil {
		return "", wrapError(CodePreflightBlocked, ErrPreflightBlocked, "payment network is invalid: %v", err)
	}
	if _, err := c.EnsureReady(ctx, ReadyInput{
		ChainID:  chainID,
		Asset:    accept.Asset,
		Receiver: accept.PayTo,
	}); err != nil {
		return "", err
	}
	result, err = c.preflight.check(ctx, preflightRequest{
		PaymentRequired:    required,
		PayerAddress:       kernelAddress,
		AccountType:        "KERNEL",
		WalletID:           c.cfg.WalletID,
		ExternalAgentID:    externalAgentID,
		ServiceID:          serviceID,
		ConfigVersion:      configVersion,
		RequestBindingHash: binding,
	})
	if err != nil {
		return "", err
	}
	if result.Token == "" {
		return "", wrapError(CodePreflightBlocked, ErrPreflightBlocked, "AgentPay preflight still missing payment_context_token after readiness")
	}
	return result.Token, nil
}

func (c *Client) authorizeWithSetupRetry(ctx context.Context, intentID, contextToken, approvalToken string) (authorizationResponse, error) {
	var lastErr error
	for attempt := 0; attempt < c.cfg.setupAttempts(); attempt++ {
		auth, err := c.wallet.createAuthorization(ctx, c.cfg.WalletID, intentID, contextToken, approvalToken, true)
		if err == nil {
			return auth, nil
		}
		lastErr = err
		if !errors.Is(err, ErrSetupInProgress) {
			return authorizationResponse{}, err
		}
		if sleepErr := sleep(ctx, c.cfg.setupWait()); sleepErr != nil {
			return authorizationResponse{}, lastErr
		}
	}
	if lastErr != nil {
		return authorizationResponse{}, lastErr
	}
	return authorizationResponse{}, wrapError(CodeSetupInProgress, ErrSetupInProgress, "payment-authorizations setup timed out")
}

// walletIdentity returns Kernel + external agent ids without mutating Client.
// Values come from Config when set; otherwise one Wallet GET fills locals only.
func (c *Client) walletIdentity(ctx context.Context) (kernelAddress, externalAgentID string, err error) {
	kernelAddress = strings.TrimSpace(c.cfg.KernelAddress)
	externalAgentID = strings.TrimSpace(c.cfg.ExternalAgentID)
	if kernelAddress != "" && externalAgentID != "" {
		return kernelAddress, externalAgentID, nil
	}
	w, err := c.wallet.getWallet(ctx, c.cfg.WalletID)
	if err != nil {
		return "", "", err
	}
	if kernelAddress == "" {
		kernelAddress = strings.TrimSpace(w.AccountAddress)
	}
	if externalAgentID == "" {
		externalAgentID = strings.TrimSpace(w.ExternalAgentID)
	}
	if kernelAddress == "" || externalAgentID == "" {
		return "", "", wrapError(CodeWalletNotReady, ErrWalletUnavailable, "wallet is missing account_address or external_agent_id")
	}
	return kernelAddress, externalAgentID, nil
}

type requestSnapshot struct {
	Method string
	URL    string
	Body   []byte
	Header http.Header
}

func snapshotRequest(req *http.Request) (requestSnapshot, error) {
	if req.URL == nil {
		return requestSnapshot{}, wrapError(CodeConfigInvalid, ErrConfigInvalid, "request URL is required")
	}
	body, err := readAllBody(req)
	if err != nil {
		return requestSnapshot{}, err
	}
	header := req.Header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	header.Del(PaymentSignatureHeader)
	u := req.URL.String()
	if req.URL.Scheme == "" || req.URL.Host == "" {
		return requestSnapshot{}, wrapError(CodeConfigInvalid, ErrConfigInvalid, "request URL must be absolute")
	}
	return requestSnapshot{
		Method: firstNonEmpty(req.Method, http.MethodGet),
		URL:    u,
		Body:   body,
		Header: applyEffectiveHeaders(header, body),
	}, nil
}

func readAllBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, 2<<20+1))
	_ = req.Body.Close()
	if err != nil {
		return nil, wrapError(CodeConfigInvalid, ErrConfigInvalid, "read request body: %v", err)
	}
	if len(raw) > 2<<20 {
		return nil, wrapError(CodeConfigInvalid, ErrConfigInvalid, "request body exceeds 2 MiB")
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw)), nil
	}
	req.ContentLength = int64(len(raw))
	return raw, nil
}

func (c *Client) doSnapshot(ctx context.Context, snap requestSnapshot, signature string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, snap.Method, snap.URL, bytes.NewReader(snap.Body))
	if err != nil {
		return nil, wrapError(CodeConfigInvalid, ErrConfigInvalid, "build request: %v", err)
	}
	req.Header = applyEffectiveHeaders(snap.Header, snap.Body)
	if signature != "" {
		req.Header.Set(PaymentSignatureHeader, signature)
	}
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client.Do(req)
}

func newIntentID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("payment-intent-%d", time.Now().UnixNano())
	}
	return "payment-intent-" + hex.EncodeToString(b[:])
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
