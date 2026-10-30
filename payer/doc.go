// Package agentpaypayer is the Kernel payer SDK for AgentPay on BOT Chain.
//
// It orchestrates 402 → Preflight → payment-approvals → payment-authorizations
// (auto_setup) → PAYMENT-SIGNATURE retry. It never holds an approval or Kernel
// private key, never writes Permit2 Approve calldata itself, and never treats
// AgentPay wallet_readiness_hint as an authoritative READY signal.
package agentpaypayer
