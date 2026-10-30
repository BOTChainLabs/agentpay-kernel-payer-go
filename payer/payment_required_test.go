package agentpaypayer

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

func TestParseAndAssertCanonicalProfile(t *testing.T) {
	raw := []byte(`{
	  "x402Version": 2,
	  "resource": {"url": "https://agentpay.example/demo/paid-echo"},
	  "accepts": [{
	    "scheme": "exact",
	    "network": "eip155:968",
	    "asset": "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	    "amount": "1000000",
	    "payTo": "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	    "extra": {
	      "assetTransferMethod": "permit2",
	      "paymentFlow": "upfront",
	      "serviceId": "svc_demo_echo",
	      "configVersion": 1
	    }
	  }]
	}`)
	header := base64.StdEncoding.EncodeToString(raw)
	pr, decoded, err := parsePaymentRequiredHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != string(raw) {
		t.Fatalf("decoded snapshot mismatch")
	}
	accept, err := assertCanonicalProfile(pr)
	if err != nil {
		t.Fatal(err)
	}
	if serviceIDFromExtra(accept.Extra) != "svc_demo_echo" || configVersionFromExtra(accept.Extra) != 1 {
		t.Fatalf("service binding mismatch: %+v", accept.Extra)
	}
	chainID, err := chainIDFromNetwork(accept.Network)
	if err != nil || chainID != 968 {
		t.Fatalf("chain id = %d err=%v", chainID, err)
	}
}

func TestAssertCanonicalProfileRejectsEOAShape(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"x402Version": 2,
		"resource":    map[string]string{"url": "https://example/pay"},
		"accepts": []map[string]any{{
			"scheme":  "exact",
			"network": "eip155:968",
			"asset":   "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"amount":  "1",
			"payTo":   "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			"extra": map[string]any{
				"assetTransferMethod": "erc20",
				"paymentFlow":         "upfront",
				"serviceId":           "svc",
				"configVersion":       1,
			},
		}},
	})
	pr, _, err := parsePaymentRequiredHeader(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assertCanonicalProfile(pr); err == nil {
		t.Fatal("expected unsupported profile")
	}
}

func TestResourceURLMatchesRequest(t *testing.T) {
	req := "https://agentpay.example/demo/paid-echo?b=2&a=1"
	if !resourceURLMatchesRequest(req, req) {
		t.Fatal("exact match failed")
	}
	if !resourceURLMatchesRequest("https://AgentPay.Example/demo/paid-echo?a=1&b=2", req) {
		t.Fatal("canonical host/query match failed")
	}
	if !resourceURLMatchesRequest("https://agentpay.example:443/demo/paid-echo?a=1&b=2", req) {
		t.Fatal("default https port should match")
	}
	if resourceURLMatchesRequest("https://other.example/demo/paid-echo", req) {
		t.Fatal("different host must not match")
	}
	if resourceURLMatchesRequest("https://agentpay.example/other", req) {
		t.Fatal("different path must not match")
	}
	if resourceURLMatchesRequest("http://agentpay.example/demo/paid-echo?a=1&b=2", req) {
		t.Fatal("different scheme must not match")
	}
}

func TestRequestBindingHashUsesEffectiveHeaders(t *testing.T) {
	jsonHeaders := applyEffectiveHeaders(http.Header{
		"Accept":       []string{"application/json"},
		"Content-Type": []string{"application/json"},
	}, []byte(`{}`))
	const wantJSONPost = "0x5de446e95086e3a07b2a28a3e24521ebbc1edac66b3a7ee8857980e08192c3ef"
	got := requestBindingHash("svc_demo_echo", 1, "POST", "https://agentpay.example/demo/paid-echo", []byte(`{}`), jsonHeaders)
	if got != wantJSONPost {
		t.Fatalf("json post binding = %s want %s", got, wantJSONPost)
	}

	emptyGET := applyEffectiveHeaders(nil, nil)
	if emptyGET.Get("Content-Type") != "" {
		t.Fatal("empty body must not invent Content-Type")
	}
	if emptyGET.Get("Accept") != "application/json" {
		t.Fatalf("Accept default = %q", emptyGET.Get("Accept"))
	}
	getHash := requestBindingHash("svc_demo_echo", 1, "GET", "https://agentpay.example/demo/paid-echo", nil, emptyGET)
	if getHash == wantJSONPost {
		t.Fatal("GET without Content-Type must not share the JSON POST binding")
	}

	custom := applyEffectiveHeaders(http.Header{"Accept": []string{"application/vnd.example+json"}}, []byte(`{}`))
	customHash := requestBindingHash("svc_demo_echo", 1, "POST", "https://agentpay.example/demo/paid-echo", []byte(`{}`), custom)
	if customHash == wantJSONPost {
		t.Fatal("custom Accept must change the binding hash")
	}
	again := requestBindingHash("svc_demo_echo", 1, "POST", "https://agentpay.example/demo/paid-echo", []byte(`{}`), custom)
	if customHash != again {
		t.Fatal("custom header binding must be deterministic")
	}
}
