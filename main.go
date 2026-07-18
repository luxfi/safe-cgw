// Command safe-cgw is a minimal Safe Client Gateway for Lux-family networks
// (Lux 96369, Zoo 200200, Pars 494949).
//
// The upstream Safe{Wallet} web app talks to a Client Gateway (CGW) for chain
// configuration and Safe account state. The public gateway
// (safe-client.safe.global) does not know Lux chains, so the app cannot load
// chain config or any Safe. This service serves exactly the read surface the
// web app needs to render and operate:
//
//   - GET /v1|/v2/chains                     -> the Lux/Zoo/Pars chain list
//   - GET /v1|/v2/chains/{id}                -> a single chain config
//   - GET /v1/chains/{id}/safes/{addr}       -> SafeInfo, synthesized from RPC
//   - GET /v1/chains/{id}/safes/{addr}/nonces
//   - GET /v1/chains/{id}/safes/{addr}/balances/{fiat} -> native balance (RPC)
//   - collectibles / transactions / messages -> empty pages (indexer follow-up)
//
// SafeInfo is read directly from the chain (owners, threshold, nonce, version,
// modules, fallback handler, guard, singleton) via eth_call / eth_getStorageAt,
// so a Safe is loadable WITHOUT a Transaction Service. Off-chain data that
// genuinely needs an indexer (tx history/queue, ERC20/NFT balances, collected
// signatures) is returned as empty collections until the full Safe
// Transaction Service is stood up.
//
// Pure standard library. No external dependencies.
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed chains.json
var chainsJSON []byte

// Well-known Safe storage slots (keccak256 of the manager namespace strings).
const (
	slotSingleton       = "0x0000000000000000000000000000000000000000000000000000000000000000"
	slotFallbackHandler = "0x6c9a6c4a39284e37ed1cf53d337577d14212a4870fb976a4366c693b939918d5"
	slotGuard           = "0x4a204f620c8c5ccdca3fd54d003badd85ba500436a431f0cbda4f558c93c34c8"
)

// Safe method selectors.
const (
	selGetOwners      = "0xa0e67e2b"
	selGetThreshold   = "0xe75235b8"
	selNonce          = "0xaffed0e0"
	selVersion        = "0xffa1ad74"
	selModulesPaged   = "0xcc2f8452" // getModulesPaginated(address,uint256)
	zeroAddr          = "0x0000000000000000000000000000000000000000"
	modulesPageArgs   = "0000000000000000000000000000000000000000000000000000000000000001" + // start = SENTINEL (0x1)
		"0000000000000000000000000000000000000000000000000000000000000064" // pageSize = 100
)

var (
	chainList  []map[string]any
	chainIndex = map[string]map[string]any{}
	rpcByChain = map[string]string{}
	httpClient = &http.Client{Timeout: 15 * time.Second}

	// SafeL2 1.5.0 singletons per chain (org-safes deployments). Advertised via
	// /about/master-copies so the app marks a Safe's implementation as official.
	singletonByChain = map[string]string{
		"96369":  "0xED96250C6cDca3B5e5F8F066Fc3e200Ebe08BA87",
		"200200": "0xF034942c1140125b5c278aE9cEE1B488e915B2FE",
		"494949": "0xc65ea8882020af7cda7854d590c6fcd34bf364ec",
	}
)

func main() {
	if err := json.Unmarshal(chainsJSON, &chainList); err != nil {
		log.Fatalf("parse chains.json: %v", err)
	}
	for _, c := range chainList {
		id, _ := c["chainId"].(string)
		chainIndex[id] = c
		rpc := ""
		if u, ok := c["rpcUri"].(map[string]any); ok {
			rpc, _ = u["value"].(string)
		}
		if env := os.Getenv("RPC_" + id); env != "" {
			rpc = env
		}
		rpcByChain[id] = rpc
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	// {$} anchors the root so it does NOT act as a catch-all prefix — otherwise
	// every unmatched path returns the banner JSON and the app parses it as the
	// wrong type (e.g. a missing lastSync -> "Invalid time value" crash).
	mux.HandleFunc("GET /{$}", handleRoot)

	mux.HandleFunc("GET /v1/chains", handleChains)
	mux.HandleFunc("GET /v2/chains", handleChains)
	mux.HandleFunc("GET /v1/chains/{chainId}", handleChain)
	mux.HandleFunc("GET /v2/chains/{chainId}", handleChain)
	mux.HandleFunc("GET /v1/chains/{chainId}/about", handleAbout)
	mux.HandleFunc("GET /v1/chains/{chainId}/about/indexing", handleIndexing)
	mux.HandleFunc("GET /v1/chains/{chainId}/about/master-copies", handleMasterCopies)
	mux.HandleFunc("GET /v1/chains/{chainId}/safe-apps", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, []any{}) })

	mux.HandleFunc("GET /v1/chains/{chainId}/safes/{address}", handleSafe)
	mux.HandleFunc("GET /v1/chains/{chainId}/safes/{address}/nonces", handleNonces)
	mux.HandleFunc("GET /v1/chains/{chainId}/safes/{address}/balances/{fiat}", handleBalances)

	// Owned-safes lookups need an indexer; return empty so the sidebar renders.
	mux.HandleFunc("GET /v1/chains/{chainId}/owners/{ownerAddress}/safes", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"safes": []any{}}) })
	mux.HandleFunc("GET /v1/safes", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, []any{}) })
	mux.HandleFunc("GET /v2/safes", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, []any{}) })

	// Off-chain collections — empty until the Transaction Service is deployed.
	empty := func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, emptyPage()) }
	mux.HandleFunc("GET /v1/chains/{chainId}/safes/{address}/collectibles", empty)
	mux.HandleFunc("GET /v2/chains/{chainId}/safes/{address}/collectibles", empty)
	mux.HandleFunc("GET /v1/chains/{chainId}/safes/{address}/transactions/history", empty)
	mux.HandleFunc("GET /v1/chains/{chainId}/safes/{address}/transactions/queued", empty)
	mux.HandleFunc("GET /v1/chains/{chainId}/safes/{address}/messages", empty)
	mux.HandleFunc("GET /v1/chains/{chainId}/safes/{address}/incoming-transfers", empty)

	// Clean 404 for any other read the app probes (security, positions, gas-price,
	// relay, delegates, …). RTK Query treats 404 as "no data" and renders empty
	// states — never the banner, which would poison typed parsers.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { writeErr(w, 404, "not found") })

	addr := ":" + envOr("PORT", "3000")
	log.Printf("safe-cgw listening on %s for chains %v", addr, keys(rpcByChain))
	if err := http.ListenAndServe(addr, withCORS(mux)); err != nil {
		log.Fatal(err)
	}
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"service": "lux-safe-cgw",
		"chains":  keys(chainIndex),
	})
}

func handleChains(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"count": len(chainList), "next": nil, "previous": nil, "results": chainList,
	})
}

func handleChain(w http.ResponseWriter, r *http.Request) {
	c, ok := chainIndex[r.PathValue("chainId")]
	if !ok {
		writeErr(w, 404, "chain not found")
		return
	}
	writeJSON(w, 200, c)
}

func handleAbout(w http.ResponseWriter, r *http.Request) {
	if _, ok := chainIndex[r.PathValue("chainId")]; !ok {
		writeErr(w, 404, "chain not found")
		return
	}
	writeJSON(w, 200, map[string]any{
		"transactionServiceBaseUri": envOr("PUBLIC_URL", "https://safe-cgw.lux.network"),
		"name":                      "lux-safe-cgw",
		"version":                   "1.0.0",
		"buildNumber":               "1",
	})
}

// handleIndexing reports the chain as synced (this gateway reads live from RPC,
// so there is no index lag). The app's status widget reads only synced+lastSync.
func handleIndexing(w http.ResponseWriter, r *http.Request) {
	chainID := r.PathValue("chainId")
	rpc := rpcByChain[chainID]
	if rpc == "" {
		writeErr(w, 404, "chain not supported")
		return
	}
	var block int64
	if bn, err := rpcCall(rpc, "eth_blockNumber", []any{}); err == nil {
		block = decodeUint(bn).Int64()
	}
	nowISO := time.Now().UTC().Format(time.RFC3339)
	writeJSON(w, 200, map[string]any{
		"currentBlockNumber":         block,
		"currentBlockTimestamp":      nowISO,
		"erc20BlockNumber":           block,
		"erc20BlockTimestamp":        nowISO,
		"erc20Synced":                true,
		"masterCopiesBlockNumber":    block,
		"masterCopiesBlockTimestamp": nowISO,
		"masterCopiesSynced":         true,
		"synced":                     true,
		"lastSync":                   time.Now().UnixMilli(),
	})
}

func handleMasterCopies(w http.ResponseWriter, r *http.Request) {
	chainID := r.PathValue("chainId")
	if _, ok := chainIndex[chainID]; !ok {
		writeErr(w, 404, "chain not found")
		return
	}
	out := []any{}
	if s := singletonByChain[chainID]; s != "" {
		out = append(out, map[string]any{"address": s, "version": "1.5.0"})
	}
	writeJSON(w, 200, out)
}

// handleSafe synthesizes SafeInfo from on-chain reads (no Transaction Service).
func handleSafe(w http.ResponseWriter, r *http.Request) {
	chainID := r.PathValue("chainId")
	address := strings.ToLower(r.PathValue("address"))
	rpc, ok := rpcByChain[chainID]
	if !ok || rpc == "" {
		writeErr(w, 404, "chain not supported")
		return
	}

	thresholdHex, err := ethCall(rpc, address, selGetThreshold)
	if err != nil || !hasValue(thresholdHex) {
		// Not a deployed Safe on this chain.
		writeErr(w, 404, "Safe not found")
		return
	}
	threshold := decodeUint(thresholdHex)
	if threshold.Sign() == 0 {
		writeErr(w, 404, "Safe not found")
		return
	}

	ownersHex, _ := ethCall(rpc, address, selGetOwners)
	owners := decodeAddressArray(ownersHex, 0)
	nonceHex, _ := ethCall(rpc, address, selNonce)
	versionHex, _ := ethCall(rpc, address, selVersion)
	version := decodeString(versionHex)
	if version == "" {
		version = "1.5.0"
	}
	modulesHex, _ := ethCall(rpc, address, selModulesPaged+modulesPageArgs)
	modules := decodeAddressArray(modulesHex, 0)

	implementation := readAddressSlot(rpc, address, slotSingleton)
	fallbackHandler := readAddressSlot(rpc, address, slotFallbackHandler)
	guard := readAddressSlot(rpc, address, slotGuard)

	tag := fmt.Sprintf("%d", time.Now().Unix())
	out := map[string]any{
		"address":                    addrInfo(address),
		"chainId":                    chainID,
		"nonce":                      decodeUint(nonceHex).Int64(),
		"threshold":                  threshold.Int64(),
		"owners":                     addrInfoList(owners),
		"implementation":             addrInfo(orZero(implementation)),
		"implementationVersionState": versionState(version, chainID),
		"modules":                    addrInfoListOrNull(modules),
		"fallbackHandler":            addrInfoOrNull(fallbackHandler),
		"guard":                      addrInfoOrNull(guard),
		"version":                    version,
		"collectiblesTag":            tag,
		"txQueuedTag":                tag,
		"txHistoryTag":               tag,
		"messagesTag":                tag,
	}
	writeJSON(w, 200, out)
}

func handleNonces(w http.ResponseWriter, r *http.Request) {
	rpc := rpcByChain[r.PathValue("chainId")]
	address := strings.ToLower(r.PathValue("address"))
	if rpc == "" {
		writeErr(w, 404, "chain not supported")
		return
	}
	nonceHex, err := ethCall(rpc, address, selNonce)
	if err != nil || !hasValue(nonceHex) {
		writeErr(w, 404, "Safe not found")
		return
	}
	n := decodeUint(nonceHex).Int64()
	writeJSON(w, 200, map[string]any{"currentNonce": n, "recommendedNonce": n})
}

// handleBalances returns the Safe's native-token balance from RPC. ERC-20/NFT
// balances require the token indexer (Transaction Service) — empty for now.
func handleBalances(w http.ResponseWriter, r *http.Request) {
	chainID := r.PathValue("chainId")
	address := strings.ToLower(r.PathValue("address"))
	rpc := rpcByChain[chainID]
	c := chainIndex[chainID]
	if rpc == "" || c == nil {
		writeErr(w, 404, "chain not supported")
		return
	}
	native, _ := c["nativeCurrency"].(map[string]any)
	balHex, err := rpcCall(rpc, "eth_getBalance", []any{address, "latest"})
	items := []any{}
	if err == nil {
		wei := decodeUint(balHex).String()
		items = append(items, map[string]any{
			"tokenInfo": map[string]any{
				"type":     "NATIVE_TOKEN",
				"address":  zeroAddr,
				"decimals": native["decimals"],
				"symbol":   native["symbol"],
				"name":     native["name"],
				"logoUri":  native["logoUri"],
			},
			"balance":        wei,
			"fiatBalance":    "0",
			"fiatConversion": "0",
		})
	}
	writeJSON(w, 200, map[string]any{"fiatTotal": "0", "items": items})
}

// ---- Ethereum JSON-RPC helpers -------------------------------------------

func ethCall(rpc, to, data string) (string, error) {
	return rpcCall(rpc, "eth_call", []any{map[string]string{"to": to, "data": data}, "latest"})
}

func readAddressSlot(rpc, addr, slot string) string {
	hex, err := rpcCall(rpc, "eth_getStorageAt", []any{addr, slot, "latest"})
	if err != nil {
		return zeroAddr
	}
	return wordToAddress(hex)
}

func rpcCall(rpc, method string, params []any) (string, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	resp, err := httpClient.Post(rpc, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Result string          `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Error) > 0 && string(out.Error) != "null" {
		return "", fmt.Errorf("rpc error: %s", out.Error)
	}
	return out.Result, nil
}

// ---- ABI decode (stdlib only) --------------------------------------------

func hexToBytes(s string) []byte {
	s = strings.TrimPrefix(s, "0x")
	if len(s)%2 == 1 {
		s = "0" + s
	}
	b := make([]byte, len(s)/2)
	for i := 0; i < len(b); i++ {
		b[i] = hexNibble(s[2*i])<<4 | hexNibble(s[2*i+1])
	}
	return b
}

func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

func hasValue(hex string) bool {
	return len(strings.TrimPrefix(hex, "0x")) >= 2
}

func decodeUint(hex string) *big.Int {
	n := new(big.Int)
	n.SetString(strings.TrimPrefix(hex, "0x"), 16)
	return n
}

func wordToAddress(hex string) string {
	b := hexToBytes(hex)
	if len(b) < 20 {
		return zeroAddr
	}
	return "0x" + toHex(b[len(b)-20:])
}

// decodeAddressArray reads a dynamic address[] whose head word is at index
// headWord (byte offset headWord*32). Returns nil on malformed input.
func decodeAddressArray(hex string, headWord int) []string {
	b := hexToBytes(hex)
	hoff := headWord * 32
	if len(b) < hoff+32 {
		return nil
	}
	off := int(new(big.Int).SetBytes(b[hoff : hoff+32]).Int64())
	if off < 0 || off+32 > len(b) {
		return nil
	}
	n := int(new(big.Int).SetBytes(b[off : off+32]).Int64())
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		s := off + 32 + i*32
		if s+32 > len(b) {
			break
		}
		out = append(out, "0x"+toHex(b[s+12:s+32]))
	}
	return out
}

func decodeString(hex string) string {
	b := hexToBytes(hex)
	if len(b) < 64 {
		return ""
	}
	off := int(new(big.Int).SetBytes(b[0:32]).Int64())
	if off < 0 || off+32 > len(b) {
		return ""
	}
	n := int(new(big.Int).SetBytes(b[off : off+32]).Int64())
	if off+32+n > len(b) {
		return ""
	}
	return string(bytes.TrimRight(b[off+32:off+32+n], "\x00"))
}

func toHex(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[2*i] = hexdigits[v>>4]
		out[2*i+1] = hexdigits[v&0x0f]
	}
	return string(out)
}

// ---- response shaping -----------------------------------------------------

func addrInfo(a string) map[string]any {
	return map[string]any{"value": a, "name": nil, "logoUri": nil}
}
func addrInfoList(as []string) []any {
	out := make([]any, 0, len(as))
	for _, a := range as {
		out = append(out, addrInfo(a))
	}
	return out
}
func addrInfoListOrNull(as []string) any {
	if len(as) == 0 {
		return nil
	}
	return addrInfoList(as)
}
func addrInfoOrNull(a string) any {
	if a == "" || a == zeroAddr {
		return nil
	}
	return addrInfo(a)
}
func orZero(a string) string {
	if a == "" {
		return zeroAddr
	}
	return a
}
func versionState(version, chainID string) string {
	if c := chainIndex[chainID]; c != nil {
		if rec, _ := c["recommendedMasterCopyVersion"].(string); rec != "" && rec == version {
			return "UP_TO_DATE"
		}
	}
	return "UNKNOWN"
}

func emptyPage() map[string]any {
	return map[string]any{"count": 0, "next": nil, "previous": nil, "results": []any{}}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"code": code, "message": msg})
}

func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
