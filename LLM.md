# safe-cgw — Lux Safe Client Gateway

Minimal Safe Client Gateway (CGW) for the Lux-family networks. The upstream
Safe{Wallet} web app (`luxfi/safe-wallet`, brand `lux`) talks to a CGW for
chain configuration and Safe account state. The public gateway
`safe-client.safe.global` does not know Lux chains, so the app cannot load
chain config or any Safe. This service serves the read surface the app needs.

## Chains

| chainId | name | RPC |
|---------|------|-----|
| 96369  | Lux   | https://api.lux.network/v1/bc/C/rpc   |
| 200200 | Zoo   | https://api.zoo.network/v1/bc/C/rpc  |
| 494949 | Pars  | https://api.pars.network/v1/bc/C/rpc |
| 36963  | Hanzo | https://api.hanzo.network/v1/bc/C/rpc |

Every chain answers `/v1/bc/C/rpc`; there is no `/ext/` path on any of them.

Chain and native-token icons resolve to `safe.lux.network/brand/<org>/mark.svg`.
The brand repo owns the pixels and the app already serves every brand's mark, so
the icon improves with the app and the gateway holds no artwork. The three
`cdn.*.network` hosts the config used to name serve nothing: `cdn.lux.network`
404s, `cdn.zoo.network` does not resolve, `cdn.pars.network` 522s.

## Features

`features` in `chains.json` is the app's capability list. A name the app does
not know is silently ignored, so the list carries only names the app reads:

| feature | why |
|---------|-----|
| `MY_ACCOUNTS` | the account list. Without it `/welcome/accounts` renders chrome and no `<main>` — a blank landing page. |
| `ERC721`, `DEFAULT_TOKENLIST`, `SAFE_APPS`, `SPENDING_LIMIT`, `EIP1559`, `SAFE_TX_GAS_OPTIONAL` | read surfaces the gateway already serves |
| `CONTRACT_INTERACTION` | gateway-side transaction classification |

Deliberately absent:

- `MULTI_SEND` — no MultiSend or MultiSendCallOnly is deployed on any of these
  chains. `eth_getCode` on the 1.5.0 / 1.4.1 / 1.3.0 canonical addresses returns
  `0x`. Claiming the capability makes the app throw on every page.
- `NATIVE_WALLETCONNECT` — WalletConnect needs a project id. Until one is issued
  and read from KMS, the SDK initialises empty and every session request 400s.
- `OIDC_AUTH` — identity sign-in follows the brand's Hanzo IAM, which every host
  resolves, so the app offers it unconditionally rather than per chain.

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
  the forge (`git.hanzo.ai`), runner label `lux-build-linux-amd64` (multi-arch,
  `latest=false`, semver tags). The GitHub mirror has no runners.
- Deploy: `lux/universe` → `deploy/hanzo/lux-safe-cgw.yaml` → `safe-cgw.lux.network`.
- Pure Go standard library. `GOWORK=off go build .` to compile locally.
