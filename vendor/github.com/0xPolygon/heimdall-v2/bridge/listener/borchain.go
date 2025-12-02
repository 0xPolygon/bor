package listener

import (
	"context"
	"time"

	"github.com/RichardKnop/machinery/v1/tasks"

	"github.com/0xPolygon/heimdall-v2/helper"
)

// BorChainListener - Listens to and processes headerBlocks from the bor chain
type BorChainListener struct {
	BaseListener
}

// Start starts new block subscription
func (ml *BorChainListener) Start() error {
	ml.Logger.Info("Starting")

	// create cancellable context
	ctx, cancelSubscription := context.WithCancel(context.Background())
	ml.cancelSubscription = cancelSubscription

	// create cancellable context
	headerCtx, cancelHeaderProcess := context.WithCancel(context.Background())
	ml.cancelHeaderProcess = cancelHeaderProcess

	// start the header process
	go ml.StartHeaderProcess(headerCtx)

	// start go routine to poll for the new header using the client object
	ml.Logger.Info("Start polling for header blocks", "pollInterval", helper.GetConfig().CheckpointPollInterval)

	// start polling for the latest block in the bor child chain (replace with finalized block once we have it implemented)
	go ml.StartPolling(ctx, helper.GetConfig().CheckpointPollInterval, nil)

	return nil
}

// ProcessHeader - process header block from the bor chain
func (ml *BorChainListener) ProcessHeader(newHeader *blockHeader) {
	ml.Logger.Debug("New block detected", "blockNumber", newHeader.header.Number)
	// Marshall header block and publish to queue
	headerBytes, err := newHeader.header.MarshalJSON()
	if err != nil {
		ml.Logger.Error("Error marshalling header block", "error", err)
		return
	}

	ml.sendTaskWithDelay("sendCheckpointToHeimdall", headerBytes, 0)
}

func (ml *BorChainListener) sendTaskWithDelay(taskName string, headerBytes []byte, delay time.Duration) {
	// create the machinery task
	signature := &tasks.Signature{
		Name: taskName,
		Args: []tasks.Arg{
			{
				Type:  "string",
				Value: string(headerBytes),
			},
		},
	}
	signature.RetryCount = 3

	// add delay for the task so that multiple validators won't send same transaction at same time
	eta := time.Now().Add(delay)
	signature.ETA = &eta

	ml.Logger.Debug("Sending task", "taskName", taskName, "currentTime", time.Now(), "delayTime", eta)

	_, err := ml.queueConnector.Server.SendTask(signature)
	if err != nil {
		ml.Logger.Error("Error sending task", "taskName", taskName, "error", err)
	}
}
