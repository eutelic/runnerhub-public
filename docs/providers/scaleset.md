# GitHub Runner Scale Set adapter

`scaleset/` is intentionally a nested Go 1.25 module because GitHub's current standalone `actions/scaleset` client requires Go 1.25+, while the main RunnerHub daemon remains Go 1.23 compatible.

The adapter creates a GitHub runner scale set, listens to the message session, scales from the authoritative desired runner count, asks GitHub for JIT configs, and posts those configs to RunnerHub's provider-neutral worker API. Job-started/completed events are fed back to the RunnerHub lifecycle state machine.

The scale-set client is still public preview, so it stays outside the core lifecycle contract and direct JIT registration remains supported.
