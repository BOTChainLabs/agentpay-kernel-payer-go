# Changelog

All notable changes to this SDK are documented here.

## Unreleased

## v0.1.1 — 2026-10-30

- `FetchWithPayment` options: `WithPaymentIntentID`, `WithConsentEvidenceID`, `WithOnSigned`.
- `WithOnSigned` runs after Wallet SIGNED and before the `PAYMENT-SIGNATURE` request; hook errors abort the paid send and still return the attempt (`ON_SIGNED_FAILED`).
- Stable intent id is used for `payment-approvals` (`payment-approval:{id}`) and `payment-authorizations` Idempotency-Key.
- Explicit `PaymentIntentID` max **111** bytes (rejects longer); no silent truncation of `payment-approval:{id}`.

## v0.1.0 — 2026-10-30

- Initial public release of the Kernel payer SDK.
- `FetchWithPayment`, `Retry`, optional `EnsureReady`.
- Clients for AgentPay Preflight and Wallet `payment-approvals` / `payment-authorizations` / `payment-readiness`.
- Explicit `WalletAuthMode` (`project` / `public`): public wallets omit Wallet `X-API-Key`.
- AgentPay Preflight never sends `X-API-Key`; Wallet `APIKey` is project-mode only.
