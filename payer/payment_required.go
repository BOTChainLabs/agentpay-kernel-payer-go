package agentpaypayer

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	PaymentRequiredHeader  = "PAYMENT-REQUIRED"
	PaymentSignatureHeader = "PAYMENT-SIGNATURE"
	PaymentResponseHeader  = "PAYMENT-RESPONSE"

	canonicalScheme             = "exact"
	canonicalAssetTransferMethod = "permit2"
	canonicalPaymentFlow        = "upfront"
)

// paymentRequired is the subset of x402 v2 PaymentRequired the Kernel payer
// needs. Unknown fields are ignored so Provider extensions stay forward-compatible.
type paymentRequired struct {
	X402Version int `json:"x402Version"`
	Resource    struct {
		URL string `json:"url"`
	} `json:"resource"`
	Accepts []paymentAccept `json:"accepts"`
}

type paymentAccept struct {
	Scheme  string         `json:"scheme"`
	Network string         `json:"network"`
	Asset   string         `json:"asset"`
	Amount  string         `json:"amount"`
	PayTo   string         `json:"payTo"`
	Extra   map[string]any `json:"extra"`
}

func parsePaymentRequiredHeader(header string) (paymentRequired, json.RawMessage, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return paymentRequired{}, nil, wrapError(CodePaymentRequiredInvalid, ErrPaymentRequiredInvalid, "missing PAYMENT-REQUIRED")
	}
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil || !json.Valid(raw) {
		return paymentRequired{}, nil, wrapError(CodePaymentRequiredInvalid, ErrPaymentRequiredInvalid, "PAYMENT-REQUIRED is not valid base64 JSON")
	}
	var pr paymentRequired
	if err := json.Unmarshal(raw, &pr); err != nil {
		return paymentRequired{}, nil, wrapError(CodePaymentRequiredInvalid, ErrPaymentRequiredInvalid, "PAYMENT-REQUIRED JSON is invalid")
	}
	return pr, append(json.RawMessage(nil), raw...), nil
}

func assertCanonicalProfile(pr paymentRequired) (paymentAccept, error) {
	if pr.X402Version != 2 {
		return paymentAccept{}, wrapError(CodePaymentProfileUnsupported, ErrPaymentProfileUnsupported, "x402Version must be 2")
	}
	if strings.TrimSpace(pr.Resource.URL) == "" {
		return paymentAccept{}, wrapError(CodePaymentProfileUnsupported, ErrPaymentProfileUnsupported, "resource.url is required")
	}
	if len(pr.Accepts) != 1 {
		return paymentAccept{}, wrapError(CodePaymentProfileUnsupported, ErrPaymentProfileUnsupported, "exactly one accepts entry is required")
	}
	a := pr.Accepts[0]
	if a.Scheme != canonicalScheme {
		return paymentAccept{}, wrapError(CodePaymentProfileUnsupported, ErrPaymentProfileUnsupported, "scheme must be exact")
	}
	if extraString(a.Extra, "assetTransferMethod") != canonicalAssetTransferMethod ||
		extraString(a.Extra, "paymentFlow") != canonicalPaymentFlow {
		return paymentAccept{}, wrapError(CodePaymentProfileUnsupported, ErrPaymentProfileUnsupported, "profile must be permit2 + upfront")
	}
	if strings.TrimSpace(a.Network) == "" || strings.TrimSpace(a.Asset) == "" ||
		strings.TrimSpace(a.Amount) == "" || strings.TrimSpace(a.PayTo) == "" {
		return paymentAccept{}, wrapError(CodePaymentProfileUnsupported, ErrPaymentProfileUnsupported, "accepts fields are incomplete")
	}
	if serviceIDFromExtra(a.Extra) == "" || configVersionFromExtra(a.Extra) < 1 {
		return paymentAccept{}, wrapError(CodePaymentProfileUnsupported, ErrPaymentProfileUnsupported, "serviceId and configVersion are required")
	}
	return a, nil
}

// resourceURLMatchesRequest rejects PaymentRequired documents that name a
// different payment target than the request about to be retried. AgentPay
// service directory uses exact resource URL equality; the SDK also accepts the
// same scheme/host/path/query after HTTP host and query canonicalization so
// cosmetic differences cannot be confused with a different resource.
func resourceURLMatchesRequest(resourceURL, requestURL string) bool {
	resourceURL = strings.TrimSpace(resourceURL)
	requestURL = strings.TrimSpace(requestURL)
	if resourceURL == "" || requestURL == "" {
		return false
	}
	if resourceURL == requestURL {
		return true
	}
	a, err := url.Parse(resourceURL)
	if err != nil || a.Scheme == "" || a.Host == "" {
		return false
	}
	b, err := url.Parse(requestURL)
	if err != nil || b.Scheme == "" || b.Host == "" {
		return false
	}
	if !strings.EqualFold(a.Scheme, b.Scheme) {
		return false
	}
	if !sameHTTPHost(a, b) {
		return false
	}
	return requestBindingTarget(resourceURL) == requestBindingTarget(requestURL)
}

func sameHTTPHost(a, b *url.URL) bool {
	if strings.EqualFold(a.Host, b.Host) {
		return true
	}
	return strings.EqualFold(a.Hostname(), b.Hostname()) && effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}

func extraString(extra map[string]any, key string) string {
	if extra == nil {
		return ""
	}
	v, ok := extra[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}

func serviceIDFromExtra(extra map[string]any) string {
	return strings.TrimSpace(extraString(extra, "serviceId"))
}

func configVersionFromExtra(extra map[string]any) int {
	if extra == nil {
		return 0
	}
	v, ok := extra["configVersion"]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	default:
		n, _ := strconv.Atoi(fmt.Sprint(t))
		return n
	}
}

func chainIDFromNetwork(network string) (int64, error) {
	const prefix = "eip155:"
	network = strings.TrimSpace(network)
	if !strings.HasPrefix(network, prefix) {
		return 0, fmt.Errorf("network is not eip155")
	}
	return strconv.ParseInt(strings.TrimPrefix(network, prefix), 10, 64)
}
