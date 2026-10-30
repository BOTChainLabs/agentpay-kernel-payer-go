package agentpaypayer

import (
	"errors"
	"fmt"
)

// Stable error codes callers can switch on without parsing free-form messages.
const (
	CodeConfigInvalid             = "CONFIG_INVALID"
	CodePaymentRequiredInvalid    = "PAYMENT_REQUIRED_INVALID"
	CodePaymentProfileUnsupported = "UNSUPPORTED_PAYMENT_PROFILE"
	CodePaymentResourceMismatch   = "PAYMENT_RESOURCE_MISMATCH"
	CodePaymentConstraintMismatch = "PAYMENT_CONSTRAINT_MISMATCH"
	CodePreflightBlocked          = "PREFLIGHT_BLOCKED"
	CodePreflightUnavailable      = "PREFLIGHT_UNAVAILABLE"
	CodeWalletUnavailable         = "WALLET_UNAVAILABLE"
	CodeApprovalFailed            = "APPROVAL_FAILED"
	CodeAuthorizationFailed       = "AUTHORIZATION_FAILED"
	CodeSetupInProgress           = "SETUP_IN_PROGRESS"
	CodeFundRequired              = "FUND_REQUIRED"
	CodeWalletNotReady            = "WALLET_NOT_READY"
	CodeRetryInvalid              = "RETRY_INVALID"
	CodePaymentUnknown            = "PAYMENT_UNKNOWN"
	CodeOnSignedFailed            = "ON_SIGNED_FAILED"
)

var (
	ErrConfigInvalid             = errors.New("payer config is invalid")
	ErrPaymentRequiredInvalid    = errors.New("PAYMENT-REQUIRED header is invalid")
	ErrPaymentProfileUnsupported = errors.New("payment profile is not exact+permit2+upfront")
	ErrPaymentResourceMismatch   = errors.New("PAYMENT-REQUIRED resource.url does not match the request URL")
	ErrPaymentConstraintMismatch = errors.New("PAYMENT-REQUIRED does not match caller payment constraints")
	ErrPreflightBlocked          = errors.New("AgentPay preflight did not approve payment")
	ErrPreflightUnavailable      = errors.New("AgentPay preflight is unavailable")
	ErrWalletUnavailable         = errors.New("wallet service is unavailable")
	ErrApprovalFailed            = errors.New("payment approval failed")
	ErrAuthorizationFailed       = errors.New("payment authorization failed")
	ErrSetupInProgress           = errors.New("permit2 setup is still in progress")
	ErrRetryInvalid              = errors.New("payment attempt cannot be retried")
	ErrPaymentUnknown            = errors.New("paid request result is unknown; do not create a new payment intent")
	ErrOnSignedFailed            = errors.New("on-signed hook failed; paid request was not sent")
)

// Error is the SDK's program-readable failure. Unwrap returns the sentinel
// when one was wrapped; Code is stable for HTTP / logging maps.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.Cause }

func wrapError(code string, sentinel error, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	return &Error{Code: code, Message: msg, Cause: sentinel}
}
