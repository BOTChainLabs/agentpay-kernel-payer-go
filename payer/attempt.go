package agentpaypayer

import (
	"context"
	"encoding/json"
	"net/http"
)

// PaymentAttempt stores the original unpaid request plus the signed payload so
// Retry can replay without a new Preflight, approval, or idempotency key.
type PaymentAttempt struct {
	Method          string
	URL             string
	Body            []byte
	Header          http.Header
	IdempotencyKey  string
	AuthorizationID string
	PaymentPayload  json.RawMessage
	SignatureHeader string
}

// ReadyInput is the optional payment-readiness request.
type ReadyInput struct {
	ChainID  int64
	Asset    string
	Receiver string
}

// ReadyResult mirrors Wallet payment-readiness.
type ReadyResult struct {
	Status     string `json:"status"`
	UserOpHash string `json:"user_op_hash,omitempty"`
	Replayed   bool   `json:"replayed,omitempty"`
}

// Consent is forwarded to POST .../payment-approvals. Terms still come only
// from the verified payment context on the Wallet side.
type Consent struct {
	Type       string `json:"type"`
	EvidenceID string `json:"evidence_id"`
}

// Payer is the frozen public interface.
type Payer interface {
	EnsureReady(ctx context.Context, in ReadyInput) (ReadyResult, error)
	FetchWithPayment(ctx context.Context, req *http.Request) (*http.Response, *PaymentAttempt, error)
	Retry(ctx context.Context, attempt *PaymentAttempt) (*http.Response, error)
}
