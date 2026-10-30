package agentpaypayer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// requestBindingHash matches memex / Wallet attestation binding
// (agentpay-request-v1). Semantic headers must be the effective headers that
// doSnapshot will send, not hard-coded JSON defaults.
func requestBindingHash(serviceID string, configVersion int, method, resourceURL string, body []byte, header http.Header) string {
	bodyDigest := sha256.Sum256(body)
	headersJSON, _ := json.Marshal(bindingSemanticHeaders(header))
	fields := []string{
		"agentpay-request-v1",
		serviceID,
		fmt.Sprintf("%d", configVersion),
		strings.ToUpper(method),
		requestBindingTarget(resourceURL),
		string(headersJSON),
		hex.EncodeToString(bodyDigest[:]),
	}
	var canonical bytes.Buffer
	for _, field := range fields {
		_, _ = fmt.Fprintf(&canonical, "%d:%s", len([]byte(field)), field)
	}
	sum := sha256.Sum256(canonical.Bytes())
	return "0x" + hex.EncodeToString(sum[:])
}

// bindingSemanticHeaders keeps Accept / Content-Type only when present after
// effective-header defaults. Keys are lower-case; encoding/json sorts them.
func bindingSemanticHeaders(header http.Header) map[string]string {
	out := make(map[string]string, 2)
	if v := strings.TrimSpace(header.Get("Accept")); v != "" {
		out["accept"] = v
	}
	if v := strings.TrimSpace(header.Get("Content-Type")); v != "" {
		out["content-type"] = v
	}
	return out
}

func requestBindingTarget(resourceURL string) string {
	parsed, err := url.Parse(resourceURL)
	if err != nil {
		return "/"
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return path
	}
	encoded := query.Encode()
	if encoded == "" {
		return path
	}
	return path + "?" + encoded
}

// applyEffectiveHeaders mirrors doSnapshot defaults so binding and wire headers
// stay identical. Accept defaults to application/json; Content-Type defaults to
// application/json only when the body is non-empty.
func applyEffectiveHeaders(header http.Header, body []byte) http.Header {
	out := header.Clone()
	if out == nil {
		out = make(http.Header)
	}
	if out.Get("Accept") == "" {
		out.Set("Accept", "application/json")
	}
	if out.Get("Content-Type") == "" && len(body) > 0 {
		out.Set("Content-Type", "application/json")
	}
	return out
}
