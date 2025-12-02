package helper

import (
	"context"
	"fmt"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	httpClient "github.com/cometbft/cometbft/rpc/client/http"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	cmtTypes "github.com/cometbft/cometbft/types"
	cosmosContext "github.com/cosmos/cosmos-sdk/client"
	"github.com/pkg/errors"
)

const (
	CommitTimeout = 2 * time.Minute
)

// GetNodeStatus returns node status
func GetNodeStatus(cliCtx cosmosContext.Context, _ context.Context) (*ctypes.ResultStatus, error) {
	node, err := cliCtx.GetNode()
	if err != nil {
		return nil, err
	}

	ctx := cliCtx.CmdContext

	if ctx == nil {
		ctxWithTimeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ctx = ctxWithTimeout
	}

	return node.Status(ctx) //nolint:contextcheck
}

// QueryTxWithProof query tx with proof from the node
func QueryTxWithProof(cliCtx cosmosContext.Context, hash []byte) (*ctypes.ResultTx, error) {
	node, err := cliCtx.GetNode()
	if err != nil {
		return nil, err
	}

	ctx := cliCtx.CmdContext

	if ctx == nil {
		ctxWithTimeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ctx = ctxWithTimeout
	}

	return node.Tx(ctx, hash, true)
}

// GetBeginBlockEvents get block through per height
func GetBeginBlockEvents(ctx context.Context, client *httpClient.HTTP, height int64) ([]abci.Event, error) {
	var events []abci.Event
	var err error

	c, cancel := context.WithTimeout(ctx, CommitTimeout)
	defer cancel()

	// get block using the client
	blockResults, err := client.BlockResults(c, &height)
	if err == nil && blockResults != nil {
		events = blockResults.FinalizeBlockEvents
		return events, nil
	}

	// subscriber
	subscriber := fmt.Sprintf("new-block-%v", height)

	// query for event
	query := cmtTypes.QueryForEvent(cmtTypes.EventNewBlock).String()

	// register for the next event of this type
	eventCh, err := client.Subscribe(c, subscriber, query)
	if err != nil {
		return events, errors.Wrap(err, "failed to subscribe")
	}

	// unsubscribe query
	defer func() {
		if unsubscribeErr := client.Unsubscribe(c, subscriber, query); unsubscribeErr != nil && err == nil {
			err = unsubscribeErr
			events = nil // Set events to nil when returning an error
		}
	}()

	for {
		select {
		case event := <-eventCh:
			eventData := event.Data
			switch t := eventData.(type) {
			case cmtTypes.EventDataNewBlock:
				if t.Block.Height == height {
					events = t.ResultFinalizeBlock.GetEvents()
					return events, err
				}
			default:
				Logger.Error("GetBeginBlockEvents", "unexpected event type", fmt.Sprintf("%+v", t))
				return events, fmt.Errorf("unexpected event type: %T", t)
			}
		case <-ctx.Done():
			// Parent context cancelled - return immediately
			return events, ctx.Err()
		case <-c.Done():
			return events, errors.New("timed out waiting for event")
		}
	}
}
