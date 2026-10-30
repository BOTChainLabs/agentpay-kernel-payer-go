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

// FetchOption configures a single FetchWithPayment call.
type FetchOption func(*fetchOptions)

type fetchOptions struct {
	paymentIntentID   string
	consentEvidenceID string
	onSigned          func(context.Context, *PaymentAttempt) error
}

// WithPaymentIntentID sets the stable Wallet Idempotency-Key for
// payment-authorizations and the suffix of payment-approval:{id}.
// Explicit ids must be ≤111 bytes so "payment-approval:{id}" fits the 128-byte
// Idempotency-Key limit without truncation. Callers with durable business ids
// (for example memex invocation_id) should pass them here. When omitted, the
// SDK generates a random id for that call only.
func WithPaymentIntentID(id string) FetchOption {
	return func(o *fetchOptions) {
		o.paymentIntentID = id
	}
}

// WithConsentEvidenceID overrides Config.ConsentEvidenceID for this call.
// When empty, evidence defaults to the resolved payment intent id.
func WithConsentEvidenceID(id string) FetchOption {
	return func(o *fetchOptions) {
		o.consentEvidenceID = id
	}
}

// WithOnSigned registers a hook invoked after Wallet returns a SIGNED
// authorization and before the PAYMENT-SIGNATURE request is sent.
// If the hook returns an error, the paid request is not sent; the attempt is
// still returned so callers can inspect AuthorizationID.
func WithOnSigned(fn func(context.Context, *PaymentAttempt) error) FetchOption {
	return func(o *fetchOptions) {
		o.onSigned = fn
	}
}

// Payer is the frozen public interface.
type Payer interface {
	EnsureReady(ctx context.Context, in ReadyInput) (ReadyResult, error)
	FetchWithPayment(ctx context.Context, req *http.Request, opts ...FetchOption) (*http.Response, *PaymentAttempt, error)
	Retry(ctx context.Context, attempt *PaymentAttempt) (*http.Response, error)
}
