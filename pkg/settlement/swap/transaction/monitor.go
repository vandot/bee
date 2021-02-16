package transaction

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/bee/pkg/logging"
)

const (
	RpcErrorTransactionNotFound = "not found"
)

type Monitor = *monitor

type TransactionCallback = func(*types.Receipt, error) error

type TransactionWatch struct {
	TxHash   common.Hash
	Callback TransactionCallback
}

type monitor struct {
	lock   sync.Mutex
	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc

	logger  logging.Logger
	backend Backend
	txs     map[*TransactionWatch]struct{}
}

func NewMonitor(backend Backend, logger logging.Logger) Monitor {
	ctx, cancel := context.WithCancel(context.Background())
	return &monitor{
		ctx:     ctx,
		cancel:  cancel,
		logger:  logger,
		backend: backend,
		txs:     make(map[*TransactionWatch]struct{}),
	}
}

func (m *monitor) WatchTransaction(watch TransactionWatch) {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.txs[&watch] = struct{}{}
}

func (m *monitor) Start() {
	m.wg.Add(1)
	go m.loop()
}

// tryGetReceipt tries to get the receipt from the backend
// treats "not found" errors as pending transactions and every other error as an actual one
func (m *monitor) tryGetReceipt(ctx context.Context, txHash common.Hash) (receipt *types.Receipt, err error) {
	_, pending, err := m.backend.TransactionByHash(ctx, txHash)
	if err != nil {
		// some clients report "not found" as error, others just return a nil receipt
		// other errors should not be ignored which is why we match here explicitly
		if err.Error() == RpcErrorTransactionNotFound {
			m.logger.Tracef("could not find transaction %x on backend", txHash)
			return nil, nil
		} else {
			return nil, fmt.Errorf("could not get transaction from backend: %w", err)
		}
	}

	if pending {
		m.logger.Tracef("awaiting for transaction %x to be mined", txHash)
		return nil, nil
	}

	receipt, err = m.backend.TransactionReceipt(ctx, txHash)
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		return nil, errors.New("did not get receipt")
	}
	return receipt, nil
}

func (m *monitor) loop() {
	defer m.wg.Done()
	for {
		var watches []*TransactionWatch
		m.lock.Lock()
		for watch := range m.txs {
			watches = append(watches, watch)
		}
		m.lock.Unlock()

		for _, watch := range watches {
			receipt, err := m.tryGetReceipt(m.ctx, watch.TxHash)
			if err != nil {
				m.logger.Errorf("failed to get receipt for tx %x: %v", receipt, err)
				continue
			}
			if receipt != nil {
				m.lock.Lock()
				delete(m.txs, watch)
				m.lock.Unlock()
				watch.Callback(receipt, nil)
			}
		}

		select {
		case <-time.After(1 * time.Second):
		case <-m.ctx.Done():
			break
		}
	}
}

func (m *monitor) Close() {
	m.cancel()
	m.wg.Wait()
}
