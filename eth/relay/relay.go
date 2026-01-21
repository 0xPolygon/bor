package relay

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/event"
)

type Config struct {
	enablePreconf   bool
	enablePrivateTx bool
	acceptPreconfTx bool
	acceptPrivateTx bool
}

// RelayService handles all preconf and private transaction related services
type RelayService struct {
	config         Config
	privateTxStore *PrivateTxStore
}

func Init(enablePreconf, enablePrivateTx, acceptPreconfTx, acceptPrivateTx bool) *RelayService {
	config := Config{
		enablePreconf:   enablePreconf,
		enablePrivateTx: enablePrivateTx,
		acceptPreconfTx: acceptPreconfTx,
		acceptPrivateTx: acceptPrivateTx,
	}
	var privateTxStore *PrivateTxStore
	if acceptPrivateTx {
		privateTxStore = NewPrivateTxStore()
	}
	return &RelayService{
		config:         config,
		privateTxStore: privateTxStore,
	}
}

func (s *RelayService) RecordPrivateTx(hash common.Hash) {
	if s.privateTxStore != nil {
		s.privateTxStore.Add(hash)
	}
}

func (s *RelayService) PurgePrivateTx(hash common.Hash) {
	if s.privateTxStore != nil {
		s.privateTxStore.Purge(hash)
	}
}

func (s *RelayService) GetPrivateTxGetter() PrivateTxGetter {
	var getter PrivateTxGetter
	if s.privateTxStore != nil {
		getter = s.privateTxStore
	}
	return getter
}

func (s *RelayService) SetchainEventSubFn(fn func(ch chan<- core.ChainEvent) event.Subscription) {
	if s.privateTxStore != nil {
		s.privateTxStore.chainEventSubFn = fn
	}
}

func (s *RelayService) PreconfEnabled() bool {
	return s.config.enablePreconf
}

func (s *RelayService) PrivateTxEnabled() bool {
	return s.config.enablePrivateTx
}

func (s *RelayService) AcceptPreconfTxs() bool {
	return s.config.acceptPreconfTx
}

func (s *RelayService) AcceptPrivateTxs() bool {
	return s.config.acceptPrivateTx
}
