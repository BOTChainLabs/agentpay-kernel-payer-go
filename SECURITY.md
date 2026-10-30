# Security policy

Do not disclose a suspected vulnerability, payment bypass, replay path, or credential exposure in a public issue. Use GitHub's private security-advisory flow for this repository and include affected versions, reproduction steps, and impact.

Do not include private keys, API keys, payment signatures, or production transaction payloads in a report.

This SDK must never store approval private keys, Kernel owner keys, or treat client-side URLs/prices as payment authority. Wallet and AgentPay remain the source of truth for payment terms and settlement.
