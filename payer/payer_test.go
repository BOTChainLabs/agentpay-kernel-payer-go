package agentpaypayer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFetchWithPaymentHappyPath(t *testing.T) {
	var authCalls atomic.Int32
	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/v1/wallets/wal_1"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"wallet_id": "wal_1", "external_agent_id": "agent_1",
				"account_address": "0x1111111111111111111111111111111111111111",
				"status": "ACTIVE", "deployment_status": "DEPLOYED",
			})
		case strings.HasSuffix(r.URL.Path, "/payment-approvals"):
			if r.Header.Get("Idempotency-Key") == "" || r.Header.Get("X-API-Key") != "wallet-key" {
				t.Fatalf("approval headers missing: %+v", r.Header)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"approval_token": "approval.jwt", "approval_id": "approval_1",
				"expires_at": "2099-01-01T00:00:00Z",
			})
		case strings.HasSuffix(r.URL.Path, "/payment-authorizations"):
			authCalls.Add(1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["auto_setup"] != true {
				t.Fatalf("auto_setup=%v", body["auto_setup"])
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_id": "auth_1",
				"status":           "SIGNED",
				"payment_payload":  map[string]any{"x402Version": 2, "payload": map[string]any{"ok": true}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wallet.Close()

	agentPay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/payments:preflight" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-API-Key") != "" {
			t.Fatalf("preflight must omit X-API-Key, got %q", r.Header.Get("X-API-Key"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"ready":                 true,
				"payment_context_token": "context.jwt",
			},
		})
	}))
	defer agentPay.Close()

	providerHits := 0
	var providerURL string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerHits++
		if providerHits == 1 {
			pr := paymentRequiredFixture(providerURL + "/demo/paid-echo")
			raw, _ := json.Marshal(pr)
			w.Header().Set(PaymentRequiredHeader, base64.StdEncoding.EncodeToString(raw))
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"code":"PAYMENT_REQUIRED"}}`))
			return
		}
		if r.Header.Get(PaymentSignatureHeader) == "" {
			t.Fatal("missing PAYMENT-SIGNATURE on paid retry")
		}
		w.Header().Set(PaymentResponseHeader, "response")
		_, _ = w.Write([]byte(`{"ok":true,"echo":"hi"}`))
	}))
	defer provider.Close()
	providerURL = provider.URL

	client, err := New(Config{
		WalletBaseURL:   wallet.URL,
		AgentPayBaseURL: agentPay.URL,
		APIKey:          "wallet-key",
		WalletID:        "wal_1",
		HTTPClient:      &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, provider.URL+"/demo/paid-echo", strings.NewReader(`{"msg":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, attempt, err := client.FetchWithPayment(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || attempt == nil || attempt.AuthorizationID != "auth_1" {
		t.Fatalf("status=%d attempt=%+v", res.StatusCode, attempt)
	}
	if authCalls.Load() != 1 {
		t.Fatalf("expected one authorization call, got %d", authCalls.Load())
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("body=%s", body)
	}
}

func TestFetchWithPaymentRejectsResourceURLMismatch(t *testing.T) {
	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("wallet must not be called on resource mismatch: %s", r.URL.Path)
	}))
	defer wallet.Close()
	agentPay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("preflight must not be called on resource mismatch")
	}))
	defer agentPay.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pr := paymentRequiredFixture("https://other.example/demo/paid-echo")
		raw, _ := json.Marshal(pr)
		w.Header().Set(PaymentRequiredHeader, base64.StdEncoding.EncodeToString(raw))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer provider.Close()

	client, err := New(Config{
		WalletBaseURL: wallet.URL, AgentPayBaseURL: agentPay.URL,
		APIKey: "k", WalletID: "wal_1", HTTPClient: &http.Client{},
		KernelAddress: "0x1111111111111111111111111111111111111111", ExternalAgentID: "agent_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, provider.URL+"/demo/paid-echo", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	_, attempt, err := client.FetchWithPayment(context.Background(), req)
	if attempt != nil || !errors.Is(err, ErrPaymentResourceMismatch) {
		t.Fatalf("err=%v attempt=%v", err, attempt)
	}
	var sdkErr *Error
	if !errors.As(err, &sdkErr) || sdkErr.Code != CodePaymentResourceMismatch {
		t.Fatalf("sdk err=%v", err)
	}
}

func TestFetchWithPaymentRetriesSetupInProgress(t *testing.T) {
	var authCalls atomic.Int32
	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/wallets/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"wallet_id": "wal_1", "external_agent_id": "agent_1",
				"account_address": "0x1111111111111111111111111111111111111111",
			})
		case strings.HasSuffix(r.URL.Path, "/payment-approvals"):
			_ = json.NewEncoder(w).Encode(map[string]any{"approval_token": "a", "approval_id": "approval_1"})
		case strings.HasSuffix(r.URL.Path, "/payment-authorizations"):
			n := authCalls.Add(1)
			if n == 1 {
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error_code": "SETUP_IN_PROGRESS", "status": "SETUP_IN_PROGRESS",
				})
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_id": "auth_2", "status": "SIGNED",
				"payment_payload": map[string]any{"x402Version": 2},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wallet.Close()
	agentPay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"ready": true, "payment_context_token": "ctx",
		}})
	}))
	defer agentPay.Close()
	var providerURL string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(PaymentSignatureHeader) == "" {
			raw, _ := json.Marshal(paymentRequiredFixture(providerURL + "/demo/paid-echo"))
			w.Header().Set(PaymentRequiredHeader, base64.StdEncoding.EncodeToString(raw))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer provider.Close()
	providerURL = provider.URL

	client, err := New(Config{
		WalletBaseURL: wallet.URL, AgentPayBaseURL: agentPay.URL,
		APIKey: "k", WalletID: "wal_1", HTTPClient: &http.Client{},
		SetupRetryWait: 1, SetupMaxAttempts: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, provider.URL+"/demo/paid-echo", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, attempt, err := client.FetchWithPayment(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if attempt.AuthorizationID != "auth_2" || authCalls.Load() != 2 {
		t.Fatalf("authCalls=%d attempt=%+v", authCalls.Load(), attempt)
	}
}

func TestRetryDoesNotCreateNewAuthorization(t *testing.T) {
	walletCalls := 0
	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		walletCalls++
		http.NotFound(w, r)
	}))
	defer wallet.Close()
	hits := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get(PaymentSignatureHeader) != "c2ln" {
			t.Fatalf("signature=%q", r.Header.Get(PaymentSignatureHeader))
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer provider.Close()
	client, err := New(Config{
		WalletBaseURL: wallet.URL, AgentPayBaseURL: wallet.URL,
		APIKey: "k", WalletID: "wal_1", HTTPClient: &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt := &PaymentAttempt{
		Method: http.MethodPost, URL: provider.URL + "/demo/paid-echo",
		Body: []byte(`{}`), Header: http.Header{"Content-Type": []string{"application/json"}},
		SignatureHeader: "c2ln", AuthorizationID: "auth_1",
	}
	res, err := client.Retry(context.Background(), attempt)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if hits != 1 || walletCalls != 0 {
		t.Fatalf("hits=%d walletCalls=%d", hits, walletCalls)
	}
}

func TestFetchWithPaymentBootstrapsReadinessWhenPreflightNeedsApprove(t *testing.T) {
	var readinessCalls atomic.Int32
	var preflightCalls atomic.Int32
	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/wallets/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"wallet_id": "wal_1", "external_agent_id": "agent_1",
				"account_address": "0x1111111111111111111111111111111111111111",
			})
		case strings.HasSuffix(r.URL.Path, "/payment-readiness"):
			readinessCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "READY"})
		case strings.HasSuffix(r.URL.Path, "/payment-approvals"):
			_ = json.NewEncoder(w).Encode(map[string]any{"approval_token": "a", "approval_id": "approval_1"})
		case strings.HasSuffix(r.URL.Path, "/payment-authorizations"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_id": "auth_3", "status": "SIGNED",
				"payment_payload": map[string]any{"x402Version": 2},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wallet.Close()
	agentPay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := preflightCalls.Add(1)
		if n == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"ready": false,
				"recovery_actions": []map[string]any{
					{"type": "APPROVE_PERMIT2"},
				},
				"wallet_readiness_hint": map[string]any{
					"suggested_action": "PAYMENT_AUTHORIZATION_AUTO_SETUP",
				},
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"ready": true, "payment_context_token": "ctx",
		}})
	}))
	defer agentPay.Close()
	var providerURL string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(PaymentSignatureHeader) == "" {
			raw, _ := json.Marshal(paymentRequiredFixture(providerURL + "/demo/paid-echo"))
			w.Header().Set(PaymentRequiredHeader, base64.StdEncoding.EncodeToString(raw))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer provider.Close()
	providerURL = provider.URL

	client, err := New(Config{
		WalletBaseURL: wallet.URL, AgentPayBaseURL: agentPay.URL,
		APIKey: "k", WalletID: "wal_1", HTTPClient: &http.Client{},
		SetupRetryWait: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, provider.URL+"/demo/paid-echo", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, attempt, err := client.FetchWithPayment(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if readinessCalls.Load() != 1 || preflightCalls.Load() != 2 || attempt.AuthorizationID != "auth_3" {
		t.Fatalf("readiness=%d preflight=%d attempt=%+v", readinessCalls.Load(), preflightCalls.Load(), attempt)
	}
}

func TestClientConcurrentFetchWithConfiguredIdentity(t *testing.T) {
	var walletGETs atomic.Int32
	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			walletGETs.Add(1)
			http.NotFound(w, r)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/payment-approvals"):
			_ = json.NewEncoder(w).Encode(map[string]any{"approval_token": "a", "approval_id": "approval_1"})
		case strings.HasSuffix(r.URL.Path, "/payment-authorizations"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_id": "auth_race", "status": "SIGNED",
				"payment_payload": map[string]any{"x402Version": 2},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wallet.Close()
	agentPay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"ready": true, "payment_context_token": "ctx",
		}})
	}))
	defer agentPay.Close()
	var providerURL string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(PaymentSignatureHeader) == "" {
			raw, _ := json.Marshal(paymentRequiredFixture(providerURL + "/demo/paid-echo"))
			w.Header().Set(PaymentRequiredHeader, base64.StdEncoding.EncodeToString(raw))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer provider.Close()
	providerURL = provider.URL

	client, err := New(Config{
		WalletBaseURL: wallet.URL, AgentPayBaseURL: agentPay.URL,
		APIKey: "k", WalletID: "wal_1", HTTPClient: &http.Client{},
		KernelAddress: "0x1111111111111111111111111111111111111111", ExternalAgentID: "agent_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, provider.URL+"/demo/paid-echo", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			res, attempt, err := client.FetchWithPayment(context.Background(), req)
			if err != nil {
				t.Errorf("err=%v", err)
				return
			}
			res.Body.Close()
			if attempt == nil {
				t.Errorf("missing attempt")
			}
		}()
	}
	wg.Wait()
	if walletGETs.Load() != 0 {
		t.Fatalf("configured identity must not GET wallet, got %d", walletGETs.Load())
	}
}

func TestPreflightErrorSentinels(t *testing.T) {
	cases := []struct {
		status int
		is     error
		code   string
	}{
		{http.StatusBadRequest, ErrPreflightBlocked, CodePreflightBlocked},
		{http.StatusUnauthorized, ErrPreflightBlocked, CodePreflightBlocked},
		{http.StatusInternalServerError, ErrPreflightUnavailable, CodePreflightUnavailable},
		{http.StatusBadGateway, ErrPreflightUnavailable, CodePreflightUnavailable},
	}
	for _, tc := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": "X", "message": "boom"},
			})
		}))
		client := &preflightClient{baseURL: server.URL, http: &http.Client{}}
		_, err := client.check(context.Background(), preflightRequest{PaymentRequired: json.RawMessage(`{}`)})
		server.Close()
		if !errors.Is(err, tc.is) {
			t.Fatalf("status=%d err=%v want Is(%v)", tc.status, err, tc.is)
		}
		var sdkErr *Error
		if !errors.As(err, &sdkErr) || sdkErr.Code != tc.code {
			t.Fatalf("status=%d sdk err=%v", tc.status, err)
		}
	}
}

func TestNewRequiresConfig(t *testing.T) {
	_, err := New(Config{})
	if err == nil || !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestFetchWithPaymentPublicWalletOmitsAPIKeys(t *testing.T) {
	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "" {
			t.Fatalf("public wallet must omit X-API-Key, got %q", r.Header.Get("X-API-Key"))
		}
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/wallets/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"wallet_id": "wal_public", "external_agent_id": "agent_1",
				"account_address": "0x1111111111111111111111111111111111111111",
			})
		case strings.HasSuffix(r.URL.Path, "/payment-approvals"):
			_ = json.NewEncoder(w).Encode(map[string]any{"approval_token": "a", "approval_id": "approval_1"})
		case strings.HasSuffix(r.URL.Path, "/payment-authorizations"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_id": "auth_public", "status": "SIGNED",
				"payment_payload": map[string]any{"x402Version": 2},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wallet.Close()
	agentPay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "" {
			t.Fatalf("preflight must omit X-API-Key, got %q", r.Header.Get("X-API-Key"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"ready": true, "payment_context_token": "ctx",
		}})
	}))
	defer agentPay.Close()
	var providerURL string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(PaymentSignatureHeader) == "" {
			raw, _ := json.Marshal(paymentRequiredFixture(providerURL + "/demo/paid-echo"))
			w.Header().Set(PaymentRequiredHeader, base64.StdEncoding.EncodeToString(raw))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer provider.Close()
	providerURL = provider.URL

	client, err := New(Config{
		WalletBaseURL: wallet.URL, AgentPayBaseURL: agentPay.URL,
		WalletID: "wal_public", WalletAuthMode: WalletAuthPublic,
		HTTPClient: &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, provider.URL+"/demo/paid-echo", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	res, attempt, err := client.FetchWithPayment(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if attempt == nil || attempt.AuthorizationID != "auth_public" {
		t.Fatalf("attempt=%+v", attempt)
	}
}

func TestFetchWithPaymentProjectWalletOmitsPreflightAPIKey(t *testing.T) {
	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "wallet-key" {
			t.Fatalf("wallet key = %q", r.Header.Get("X-API-Key"))
		}
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/wallets/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"wallet_id": "wal_1", "external_agent_id": "agent_1",
				"account_address": "0x1111111111111111111111111111111111111111",
			})
		case strings.HasSuffix(r.URL.Path, "/payment-approvals"):
			_ = json.NewEncoder(w).Encode(map[string]any{"approval_token": "a", "approval_id": "approval_1"})
		case strings.HasSuffix(r.URL.Path, "/payment-authorizations"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_id": "auth_project", "status": "SIGNED",
				"payment_payload": map[string]any{"x402Version": 2},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wallet.Close()
	agentPay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "" {
			t.Fatalf("preflight must omit X-API-Key even in project mode, got %q", r.Header.Get("X-API-Key"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"ready": true, "payment_context_token": "ctx",
		}})
	}))
	defer agentPay.Close()
	var providerURL string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(PaymentSignatureHeader) == "" {
			raw, _ := json.Marshal(paymentRequiredFixture(providerURL + "/demo/paid-echo"))
			w.Header().Set(PaymentRequiredHeader, base64.StdEncoding.EncodeToString(raw))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer provider.Close()
	providerURL = provider.URL

	client, err := New(Config{
		WalletBaseURL: wallet.URL, AgentPayBaseURL: agentPay.URL,
		WalletID: "wal_1", APIKey: "wallet-key", HTTPClient: &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, provider.URL+"/demo/paid-echo", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	res, attempt, err := client.FetchWithPayment(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if attempt == nil || attempt.AuthorizationID != "auth_project" {
		t.Fatalf("attempt=%+v", attempt)
	}
}

func TestResolvePaymentIntentIDRejectsTooLongWithoutTruncation(t *testing.T) {
	long := strings.Repeat("a", maxExplicitPaymentIntentIDLen+1)
	_, err := resolvePaymentIntentID(long)
	if err == nil {
		t.Fatal("expected error for oversized PaymentIntentID")
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != CodeConfigInvalid {
		t.Fatalf("err=%v", err)
	}
	key := paymentApprovalIdempotencyKey(strings.Repeat("b", maxExplicitPaymentIntentIDLen))
	if len(key) != maxIdempotencyKeyLen {
		t.Fatalf("approval key len=%d want %d", len(key), maxIdempotencyKeyLen)
	}
}

func TestFetchWithPaymentEnforcesPaymentConstraintsOnSingle402(t *testing.T) {
	var providerHits atomic.Int32
	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/v1/wallets/wal_1"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"wallet_id": "wal_1", "external_agent_id": "agent_1",
				"account_address": "0x1111111111111111111111111111111111111111",
			})
		case strings.HasSuffix(r.URL.Path, "/payment-approvals"):
			_ = json.NewEncoder(w).Encode(map[string]any{"approval_token": "a", "approval_id": "approval_1"})
		case strings.HasSuffix(r.URL.Path, "/payment-authorizations"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_id": "auth_c", "status": "SIGNED",
				"payment_payload": map[string]any{"x402Version": 2},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wallet.Close()
	agentPay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"ready": true, "payment_context_token": "context.jwt"},
		})
	}))
	defer agentPay.Close()
	var providerURL string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerHits.Add(1)
		if r.Header.Get(PaymentSignatureHeader) == "" {
			raw, _ := json.Marshal(paymentRequiredFixture(providerURL + "/demo/paid-echo"))
			w.Header().Set(PaymentRequiredHeader, base64.StdEncoding.EncodeToString(raw))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer provider.Close()
	providerURL = provider.URL

	client, err := New(Config{
		WalletBaseURL: wallet.URL, AgentPayBaseURL: agentPay.URL,
		APIKey: "wallet-key", WalletID: "wal_1", HTTPClient: &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, provider.URL+"/demo/paid-echo", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	_, _, err = client.FetchWithPayment(context.Background(), req, WithPaymentConstraints(PaymentConstraints{
		ServiceID: "svc_demo_echo", ConfigVersion: 1, Network: "eip155:968",
		Asset: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Amount: "1000000",
		PayTo: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if providerHits.Load() != 2 { // one 402 + one paid
		t.Fatalf("providerHits=%d", providerHits.Load())
	}
	req2, _ := http.NewRequest(http.MethodPost, provider.URL+"/demo/paid-echo", strings.NewReader(`{}`))
	req2.Header.Set("Content-Type", "application/json")
	_, _, err = client.FetchWithPayment(context.Background(), req2, WithPaymentConstraints(PaymentConstraints{
		ServiceID: "svc_other", ConfigVersion: 1, Network: "eip155:968",
		Asset: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Amount: "1000000",
		PayTo: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}))
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != CodePaymentConstraintMismatch {
		t.Fatalf("err=%v", err)
	}
}

func TestFetchWithPaymentUsesStableIntentAndOnSignedBeforePaidRequest(t *testing.T) {
	const intentID = "inv_stable_001"
	var (
		approvalKey string
		authKey     string
		consentBody map[string]any
		paidHits    atomic.Int32
		hookAuthID  string
		hookBefore  atomic.Bool
	)
	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/v1/wallets/wal_1"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"wallet_id": "wal_1", "external_agent_id": "agent_1",
				"account_address": "0x1111111111111111111111111111111111111111",
			})
		case strings.HasSuffix(r.URL.Path, "/payment-approvals"):
			approvalKey = r.Header.Get("Idempotency-Key")
			_ = json.NewDecoder(r.Body).Decode(&consentBody)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"approval_token": "approval.jwt", "approval_id": "approval_1",
			})
		case strings.HasSuffix(r.URL.Path, "/payment-authorizations"):
			authKey = r.Header.Get("Idempotency-Key")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_id": "auth_stable",
				"status":           "SIGNED",
				"payment_payload":  map[string]any{"x402Version": 2},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wallet.Close()
	agentPay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"ready": true, "payment_context_token": "context.jwt"},
		})
	}))
	defer agentPay.Close()
	var providerURL string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(PaymentSignatureHeader) == "" {
			raw, _ := json.Marshal(paymentRequiredFixture(providerURL + "/demo/paid-echo"))
			w.Header().Set(PaymentRequiredHeader, base64.StdEncoding.EncodeToString(raw))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		if !hookBefore.Load() {
			t.Fatal("paid request arrived before OnSigned completed")
		}
		paidHits.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer provider.Close()
	providerURL = provider.URL

	client, err := New(Config{
		WalletBaseURL: wallet.URL, AgentPayBaseURL: agentPay.URL,
		APIKey: "wallet-key", WalletID: "wal_1", HTTPClient: &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, provider.URL+"/demo/paid-echo", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	res, attempt, err := client.FetchWithPayment(context.Background(), req,
		WithPaymentIntentID(intentID),
		WithConsentEvidenceID(intentID),
		WithOnSigned(func(_ context.Context, a *PaymentAttempt) error {
			hookAuthID = a.AuthorizationID
			hookBefore.Store(true)
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if approvalKey != "payment-approval:"+intentID {
		t.Fatalf("approval idempotency=%q", approvalKey)
	}
	if authKey != intentID {
		t.Fatalf("authorization idempotency=%q", authKey)
	}
	consent, _ := consentBody["consent"].(map[string]any)
	if consent["evidence_id"] != intentID {
		t.Fatalf("consent=%v", consentBody)
	}
	if attempt == nil || attempt.IdempotencyKey != intentID || attempt.AuthorizationID != "auth_stable" {
		t.Fatalf("attempt=%+v", attempt)
	}
	if hookAuthID != "auth_stable" || paidHits.Load() != 1 {
		t.Fatalf("hookAuthID=%q paidHits=%d", hookAuthID, paidHits.Load())
	}
}

func TestFetchWithPaymentOnSignedErrorAbortsPaidRequest(t *testing.T) {
	var paidHits atomic.Int32
	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/v1/wallets/wal_1"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"wallet_id": "wal_1", "external_agent_id": "agent_1",
				"account_address": "0x1111111111111111111111111111111111111111",
			})
		case strings.HasSuffix(r.URL.Path, "/payment-approvals"):
			_ = json.NewEncoder(w).Encode(map[string]any{"approval_token": "a", "approval_id": "approval_1"})
		case strings.HasSuffix(r.URL.Path, "/payment-authorizations"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_id": "auth_abort", "status": "SIGNED",
				"payment_payload": map[string]any{"x402Version": 2},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer wallet.Close()
	agentPay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"ready": true, "payment_context_token": "context.jwt"},
		})
	}))
	defer agentPay.Close()
	var providerURL string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(PaymentSignatureHeader) == "" {
			raw, _ := json.Marshal(paymentRequiredFixture(providerURL + "/demo/paid-echo"))
			w.Header().Set(PaymentRequiredHeader, base64.StdEncoding.EncodeToString(raw))
			w.WriteHeader(http.StatusPaymentRequired)
			return
		}
		paidHits.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer provider.Close()
	providerURL = provider.URL

	client, err := New(Config{
		WalletBaseURL: wallet.URL, AgentPayBaseURL: agentPay.URL,
		APIKey: "wallet-key", WalletID: "wal_1", HTTPClient: &http.Client{},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, provider.URL+"/demo/paid-echo", strings.NewReader(`{}`))
	res, attempt, err := client.FetchWithPayment(context.Background(), req,
		WithPaymentIntentID("inv_abort"),
		WithOnSigned(func(context.Context, *PaymentAttempt) error {
			return errors.New("db write failed")
		}),
	)
	if res != nil {
		t.Fatal("expected nil response when OnSigned fails")
	}
	if attempt == nil || attempt.AuthorizationID != "auth_abort" {
		t.Fatalf("attempt=%+v", attempt)
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.Code != CodeOnSignedFailed || !errors.Is(err, ErrOnSignedFailed) {
		t.Fatalf("err=%v", err)
	}
	if paidHits.Load() != 0 {
		t.Fatalf("paid request must not be sent, hits=%d", paidHits.Load())
	}
}

func paymentRequiredFixture(resourceURL string) map[string]any {
	return map[string]any{
		"x402Version": 2,
		"resource":    map[string]string{"url": resourceURL},
		"accepts": []map[string]any{{
			"scheme":  "exact",
			"network": "eip155:968",
			"asset":   "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"amount":  "1000000",
			"payTo":   "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			"extra": map[string]any{
				"assetTransferMethod": "permit2",
				"paymentFlow":         "upfront",
				"serviceId":           "svc_demo_echo",
				"configVersion":       1,
			},
		}},
	}
}
