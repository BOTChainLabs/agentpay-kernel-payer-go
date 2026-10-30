# AgentPay Kernel Payer SDK for Go

Official Go SDK for paying AgentPay-protected resources from a BOT Chain Kernel wallet.

The SDK handles `402 Payment Required` → AgentPay Preflight → Wallet `payment-approvals` → Wallet `payment-authorizations` (`auto_setup`) → `PAYMENT-SIGNATURE` retry. It never holds an approval or Kernel private key, never invents Permit2 Approve calldata, and never treats `wallet_readiness_hint` as an authoritative READY signal.

## Install

```bash
go get github.com/BOTChainLabs/agentpay-kernel-payer-go@v0.1.0-rc.1
```

During local development inside `wallet-service`, use a `replace` to this directory. Published consumers must depend on a git tag, not `replace`.

## Compatibility

| Item | Supported value |
| --- | --- |
| Wallet API | `/v1/wallets/{id}/payment-approvals`, `payment-authorizations`, optional `payment-readiness` |
| AgentPay API | `/v1/payments:preflight` (Kernel) |
| x402 | v2 |
| Profile | `exact + permit2 + upfront` |
| Chains | BOT Chain testnet `968`, mainnet `677` |

## Quick start

```go
package main

import (
    "context"
    "net/http"
    "os"
    "strings"

    "github.com/BOTChainLabs/agentpay-kernel-payer-go/payer"
)

func main() {
    // Project wallet (default): Wallet X-API-Key required; Preflight never sends it.
    client, err := agentpaypayer.New(agentpaypayer.Config{
        WalletBaseURL:   "https://wallet-api.example",
        AgentPayBaseURL: "https://agentpay-api.example",
        APIKey:          os.Getenv("WALLET_API_KEY"),
        WalletID:        os.Getenv("WALLET_ID"),
    })
    if err != nil {
        panic(err)
    }

    // Public wallet: must opt in explicitly. Wallet and Preflight both omit X-API-Key.
    //
    // client, err = agentpaypayer.New(agentpaypayer.Config{
    //     WalletBaseURL:   "https://wallet-api.example",
    //     AgentPayBaseURL: "https://agentpay-api.example",
    //     WalletID:        os.Getenv("WALLET_ID"),
    //     WalletAuthMode:  agentpaypayer.WalletAuthPublic,
    // })

    req, _ := http.NewRequest(http.MethodPost, "https://agentpay-api.example/demo/paid-echo", strings.NewReader(`{"message":"hi"}`))
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Accept", "application/json")

    res, attempt, err := client.FetchWithPayment(context.Background(), req)
    if err != nil {
        panic(err)
    }
    defer res.Body.Close()

    // Only retry after a signed payment attempt. Plain upstream 5xx has attempt=nil.
    if attempt != nil && (res.StatusCode == http.StatusAccepted || res.StatusCode >= 500) {
        res, err = client.Retry(context.Background(), attempt)
        _ = res
        _ = err
    }
}
```

### Wallet auth modes

| Mode | How to select | Wallet `X-API-Key` | AgentPay Preflight `X-API-Key` |
| --- | --- | --- | --- |
| Project (default) | omit `WalletAuthMode`, or set `WalletAuthProject` | required (`APIKey`) | never sent |
| Public | set `WalletAuthMode: WalletAuthPublic` | omitted; `APIKey` must be empty | never sent |

A blank `APIKey` without `WalletAuthPublic` is a config error, not silent public mode.

## Security

Configure Wallet project API keys only in your server-side secret store. Do not ship approval keys, Kernel owner keys, or production credentials in this SDK. See [SECURITY.md](SECURITY.md).

## Development

```bash
go test ./...
go vet ./...
```

## License

Apache-2.0. See [LICENSE](LICENSE).
