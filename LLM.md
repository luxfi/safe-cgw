# safe-cgw — Lux Safe Client Gateway

Minimal Safe Client Gateway (CGW) for the Lux-family networks. The upstream
Safe{Wallet} web app (`luxfi/safe-wallet`, brand `lux`) talks to a CGW for
chain configuration and Safe account state. The public gateway
`safe-client.safe.global` does not know Lux chains, so the app cannot load
chain config or any Safe. This service serves the read surface the app needs.

## Chains

| chainId | name | RPC |
|---------|------|-----|
| 96369  | Lux  | https://api.lux.network/v1/bc/C/rpc |
| 200200 | Zoo  | https://api.zoo.network/v1/bc/C/rpc |
| 494949 | Pars | https://api.pars.network/v1/bc/C/rpc |

Chain configs (SafeL2 1.5.0 singleton/factory/handler/multisend, explorer,
native symbol) are in `chains.json`, sourced from
`~/work/lux/standard/deployments/org-safes/*.json`.

## Endpoints

- `GET /v1|/v2/chains` — chain list (paginated envelope)
- `GET /v1|/v2/chains/{id}` — one chain
- `GET /v1/chains/{id}/safes/{addr}` — **SafeInfo synthesized from RPC**
  (owners, threshold, nonce, version, modules, fallback handler, guard,
  singleton) via `eth_call` + `eth_getStorageAt`. A Safe is loadable with NO
  Transaction Service.
- `GET /v1/chains/{id}/safes/{addr}/nonces` — current/recommended nonce (RPC)
- `GET /v1/chains/{id}/safes/{addr}/balances/{fiat}` — native balance (RPC)
- collectibles / transactions (history, queued) / messages — **empty pages**

## What is intentionally empty (Transaction Service follow-up)

ERC-20 & NFT balances, tx history, the signing queue, and collected off-chain
signatures require an indexer. Until the full Safe Transaction Service is
deployed, those endpoints return empty collections. The app renders and
operates (connect wallet, RPC reads, client-side sign + execute) without them.

## Read-only

The gateway performs only `eth_call`, `eth_getStorageAt`, and `eth_getBalance`.
It never sends transactions and never mutates any Safe.

## Build / deploy

- Image: `ghcr.io/luxfi/safe-cgw` — built by `.github/workflows/docker.yml` on
  the `lux-build` ARC pool (multi-arch, `latest=false`, semver tags).
- Deploy: `luxfi/universe` → `k8s/lux-k8s/safe-cgw/` → `safe-cgw.lux.network`.
- Pure Go standard library. `GOWORK=off go build .` to compile locally.
