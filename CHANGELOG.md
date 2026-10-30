# Changelog

All notable changes to this SDK are documented here.

## Unreleased

## v0.1.0 — 2026-10-30

- Initial public release of the Kernel payer SDK.
- `FetchWithPayment`, `Retry`, optional `EnsureReady`.
- Clients for AgentPay Preflight and Wallet `payment-approvals` / `payment-authorizations` / `payment-readiness`.
- Explicit `WalletAuthMode` (`project` / `public`): public wallets omit Wallet `X-API-Key`.
- AgentPay Preflight never sends `X-API-Key`; Wallet `APIKey` is project-mode only.
