//go:build lls_shadow

package eth

// LLS shadow-producer experiment wiring (see core/shadowtrace). Only in
// -tags lls_shadow binaries, and only active with BOR_SHADOW_LLS=1.
//
// Safety model (all four must hold, enforced here and in the miner):
//  1. no sequence-store endpoint is configured — the miner gets a local,
//     network-free ShadowSequencer instead;
//  2. the signer is never an authorized producer (miner log.Crit's if it is),
//     so bor.Seal refuses every block: nothing is signed or broadcast;
//  3. the node never propagates transactions (eth/protocols/eth peer.go);
//  4. injected (fake-signature) txs are never served to peers.

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/shadowtrace"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/miner"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

// validateShadow runs before any sequencer client is built: shadow mode
// refuses a configured sequence store, and RPC reachable from outside the box
// (injected fake-signature txs are visible to pending-tx RPCs).
func validateShadow(stack *node.Node, config *ethconfig.Config) error {
	if !shadowtrace.Enabled() {
		return nil
	}

	if config.SequencerRole != "" || config.SequencerPublisherEndpoint != "" || config.SequencerConsumerEndpoint != "" {
		return errors.New("shadow mode refuses to run with a sequencer role or endpoint configured")
	}

	nc := stack.Config()
	for name, host := range map[string]string{"http": nc.HTTPHost, "ws": nc.WSHost} {
		if host != "" && host != "127.0.0.1" && host != "localhost" && host != "::1" {
			return fmt.Errorf("shadow mode needs %s RPC on loopback only, got %q", name, host)
		}
	}

	return nil
}

// attachShadow switches the node into shadow-producer mode when enabled.
func (s *Ethereum) attachShadow(stack *node.Node, config *ethconfig.Config) error {
	if !shadowtrace.Enabled() {
		return nil
	}

	if err := validateShadow(stack, config); err != nil {
		return err
	}

	if s.miner == nil {
		return errors.New("shadow mode needs the miner (not stateless sync)")
	}

	poll := 100 * time.Millisecond
	if v := os.Getenv("BOR_SHADOW_POLL_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms <= 0 {
			return fmt.Errorf("bad BOR_SHADOW_POLL_MS %q", v)
		}

		poll = time.Duration(ms) * time.Millisecond
	}

	s.miner.SetSequencer(&miner.ShadowSequencer{Poll: poll})

	// Pool arrival times for the trace.
	ch := make(chan core.NewTxsEvent, 4096)
	sub := s.txPool.SubscribeTransactions(ch, false)
	warmer := newShadowWarmer(s)
	s.shadowWatchHeads()

	go func() {
		defer sub.Unsubscribe()

		for {
			select {
			case ev := <-ch:
				now := time.Now()
				for _, tx := range ev.Txs {
					shadowtrace.PoolSeen(tx.Hash(), now)
					warmer.onPoolTx(tx, now)
				}
			case <-sub.Err():
				return
			}
		}
	}()

	api := newShadowAPI(s)
	shadowtrace.OnBuildStart(api.onBuildStart)
	shadowtrace.OnBuildEnd(api.onBuildEnd)

	// Registered APIs are always reachable over IPC; over HTTP/WS only if
	// "shadow" is listed in the module whitelist (keep it out).
	stack.RegisterAPIs([]rpc.API{{Namespace: "shadow", Service: api}})

	log.Info("SHADOW producer mode attached", "poll", poll)

	return nil
}

type injected struct {
	target  uint64 // build number after which it is removed; 0 = next build
	oneShot bool
}

// ShadowAPI injects signature-skipped transactions into the local pool.
type ShadowAPI struct {
	eth *Ethereum

	// opMu serializes inject (gen check + pool admission + tracking) with
	// Clear, so a cleared experiment can never get a late injection.
	opMu sync.Mutex

	mu       sync.Mutex
	injected map[common.Hash]*injected
	pending  []scheduled // replays waiting for the next build start
	gen      uint64      // bumped by Clear; timers of an older gen do nothing
}

type scheduled struct {
	hash   common.Hash
	offset time.Duration
	args   *ShadowInjectArgs
}

func newShadowAPI(e *Ethereum) *ShadowAPI {
	return &ShadowAPI{eth: e, injected: map[common.Hash]*injected{}}
}

// ShadowInjectArgs describes a tx to inject. Unset fees are derived from the
// current head's base fee.
type ShadowInjectArgs struct {
	From      common.Address  `json:"from"`
	To        *common.Address `json:"to"`
	Data      hexutil.Bytes   `json:"data"`
	Gas       hexutil.Uint64  `json:"gas"`
	Value     *hexutil.Big    `json:"value"`
	GasTipCap *hexutil.Big    `json:"tip"`
	GasFeeCap *hexutil.Big    `json:"feeCap"`
	// Keep stays in the pool until shadow_clear; default is one-shot: removed
	// after the build it was injected for, so the next one is cold again.
	Keep bool `json:"keep"`
}

// InjectTx adds args as a tx whose sender is taken as given (no signature).
func (a *ShadowAPI) InjectTx(args ShadowInjectArgs) (common.Hash, error) {
	return a.inject(&args, 0)
}

// ReplayTx re-injects a historical mainnet tx: same sender, target, data,
// value and gas; nonce = sender's current pool nonce.
func (a *ShadowAPI) ReplayTx(hash common.Hash, keep *bool) (common.Hash, error) {
	args, err := a.replayArgs(hash)
	if err != nil {
		return common.Hash{}, err
	}

	if keep != nil {
		args.Keep = *keep
	}

	return a.inject(args, 0)
}

// Schedule replays hashes[i] at offsetsMs[i] after the start of the next
// build (one-shot). Offsets are relative to the build's start (before the
// bor.Prepare wait), the same base as the trace's *Ms fields.
func (a *ShadowAPI) Schedule(hashes []common.Hash, offsetsMs []int64) (int, error) {
	if len(hashes) != len(offsetsMs) {
		return 0, errors.New("hashes and offsetsMs differ in length")
	}

	batch := make([]scheduled, 0, len(hashes))

	for i, h := range hashes {
		args, err := a.replayArgs(h)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", h, err)
		}

		batch = append(batch, scheduled{hash: h, offset: time.Duration(offsetsMs[i]) * time.Millisecond, args: args})
	}

	a.mu.Lock()
	a.pending = append(a.pending, batch...)
	a.mu.Unlock()

	return len(batch), nil
}

// Clear removes every injected tx still in the pool.
func (a *ShadowAPI) Clear() int {
	a.opMu.Lock()
	defer a.opMu.Unlock()

	a.mu.Lock()
	hashes := make([]common.Hash, 0, len(a.injected))
	for h := range a.injected {
		hashes = append(hashes, h)
	}
	a.injected = map[common.Hash]*injected{}
	a.pending = nil
	a.gen++
	a.mu.Unlock()

	n := 0
	for _, h := range hashes {
		if a.eth.txPool.ShadowRemove(h) {
			n++
		}
	}

	return n
}

// Status reports the injected txs still tracked.
func (a *ShadowAPI) Status() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()

	inj := make(map[string]uint64, len(a.injected))
	for h, r := range a.injected {
		inj[h.Hex()] = r.target
	}

	return map[string]any{"injected": inj, "scheduled": len(a.pending), "currentBuild": shadowtrace.Current()}
}

func (a *ShadowAPI) onBuildStart(number uint64, start time.Time) {
	a.mu.Lock()
	for _, r := range a.injected {
		if r.oneShot && r.target == 0 {
			r.target = number
		}
	}
	batch := a.pending
	a.pending = nil
	gen := a.gen
	a.mu.Unlock()

	for _, sc := range batch {
		sc := sc
		delay := time.Until(start.Add(sc.offset))

		time.AfterFunc(max(delay, 0), func() {
			h, err := a.injectGen(sc.args, number, &gen)
			if err != nil {
				log.Info("SHADOW inject failed", "number", number, "orig", sc.hash, "err", err)
				return
			}

			log.Info("SHADOW injected", "number", number, "orig", sc.hash, "hash", h, "offsetMs", sc.offset.Milliseconds())
		})
	}
}

func (a *ShadowAPI) onBuildEnd(number uint64) {
	a.mu.Lock()
	var drop []common.Hash
	for h, r := range a.injected {
		if r.oneShot && r.target != 0 && r.target <= number {
			drop = append(drop, h)
			delete(a.injected, h)
		}
	}
	a.mu.Unlock()

	for _, h := range drop {
		a.eth.txPool.ShadowRemove(h)
	}
}

func (a *ShadowAPI) replayArgs(hash common.Hash) (*ShadowInjectArgs, error) {
	found, tx, _, number, _ := a.eth.APIBackend.GetTransaction(hash)
	if !found || tx == nil {
		return nil, errors.New("tx not found (needs the tx index)")
	}

	head := a.eth.blockchain.CurrentHeader()
	signer := types.MakeSigner(a.eth.blockchain.Config(), new(big.Int).SetUint64(number), head.Time)

	from, err := types.Sender(signer, tx)
	if err != nil {
		return nil, fmt.Errorf("sender: %w", err)
	}

	return &ShadowInjectArgs{
		From: from, To: tx.To(), Data: tx.Data(), Gas: hexutil.Uint64(tx.Gas()),
		Value: (*hexutil.Big)(tx.Value()), GasTipCap: (*hexutil.Big)(tx.GasTipCap()),
	}, nil
}

// minShadowTip keeps injected txs above the pool's tip floor.
var minShadowTip = big.NewInt(30 * params.GWei)

func (a *ShadowAPI) inject(args *ShadowInjectArgs, target uint64) (common.Hash, error) {
	return a.injectGen(args, target, nil)
}

var errShadowStale = errors.New("scheduled injection cancelled by shadow_clear")

// injectGen injects under opMu; with gen set it refuses when Clear ran since
// the injection was scheduled.
func (a *ShadowAPI) injectGen(args *ShadowInjectArgs, target uint64, gen *uint64) (common.Hash, error) {
	a.opMu.Lock()
	defer a.opMu.Unlock()

	if gen != nil {
		a.mu.Lock()
		stale := a.gen != *gen
		a.mu.Unlock()

		if stale {
			return common.Hash{}, errShadowStale
		}
	}

	cfg := a.eth.blockchain.Config()
	head := a.eth.blockchain.CurrentHeader()

	tip := new(big.Int).Set(minShadowTip)
	if args.GasTipCap != nil && args.GasTipCap.ToInt().Cmp(tip) > 0 {
		tip = new(big.Int).Set(args.GasTipCap.ToInt())
	}

	feeCap := new(big.Int).Add(tip, new(big.Int).Mul(head.BaseFee, big.NewInt(2)))
	if args.GasFeeCap != nil {
		feeCap = args.GasFeeCap.ToInt()
	}

	value := new(big.Int)
	if args.Value != nil {
		value = args.Value.ToInt()
	}

	inner := &types.DynamicFeeTx{
		ChainID:   cfg.ChainID,
		Nonce:     a.eth.txPool.PoolNonce(args.From),
		GasTipCap: tip,
		GasFeeCap: feeCap,
		Gas:       uint64(args.Gas),
		To:        args.To,
		Value:     value,
		Data:      args.Data,
		// Fake signature: never verified (the sender cache is pre-filled),
		// and the tx never leaves this node.
		V: big.NewInt(0), R: big.NewInt(1), S: big.NewInt(1),
	}
	tx := types.NewTx(inner)

	// Pre-fill the sender for every signer the node may use for it: the
	// pool's (latest) and the one the miner/prefetcher derive per header.
	types.ShadowSetSender(tx, types.LatestSigner(cfg), args.From)
	if !types.LatestSigner(cfg).Equal(types.MakeSigner(cfg, new(big.Int).Add(head.Number, common.Big1), head.Time)) {
		return common.Hash{}, errors.New("pool and miner signers differ; sender cache would miss")
	}

	// Mark before the pool sees it, so it can never be served to a peer.
	shadowtrace.MarkInjected(tx.Hash(), time.Now())

	if errs := a.eth.txPool.Add([]*types.Transaction{tx}, true); len(errs) > 0 && errs[0] != nil {
		return common.Hash{}, errs[0]
	}

	// Register after admission: if its build already ended, the next
	// onBuildEnd retires it (target <= number), so nothing is left behind.
	a.mu.Lock()
	a.injected[tx.Hash()] = &injected{target: target, oneShot: !args.Keep}
	a.mu.Unlock()

	return tx.Hash(), nil
}
