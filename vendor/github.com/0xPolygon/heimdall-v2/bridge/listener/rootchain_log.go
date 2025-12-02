package listener

import (
	"bytes"
	"encoding/json"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/0xPolygon/heimdall-v2/bridge/util"
	"github.com/0xPolygon/heimdall-v2/contracts/stakinginfo"
	"github.com/0xPolygon/heimdall-v2/contracts/statesender"
	"github.com/0xPolygon/heimdall-v2/helper"
)

// handleLog handles the given log
func (rl *RootChainListener) handleLog(vLog types.Log, selectedEvent *abi.Event) {
	rl.Logger.Debug("ReceivedEvent", "eventName", selectedEvent.Name)

	switch selectedEvent.Name {
	case helper.NewHeaderBlockEvent:
		rl.handleNewHeaderBlockLog(vLog, selectedEvent)
	case helper.StakedEvent:
		rl.handleStakedLog(vLog, selectedEvent)
	case helper.StakeUpdateEvent:
		rl.handleStakeUpdateLog(vLog, selectedEvent)
	case helper.SignerChangeEvent:
		rl.handleSignerChangeLog(vLog, selectedEvent)
	case helper.UnstakeInitEvent:
		rl.handleUnstakeInitLog(vLog, selectedEvent)
	case helper.StateSyncedEvent:
		rl.handleStateSyncedLog(vLog, selectedEvent)
	case helper.TopUpFeeEvent:
		rl.handleTopUpFeeLog(vLog, selectedEvent)
	case helper.SlashedEvent:
		rl.handleSlashedLog(vLog, selectedEvent)
	case helper.UnJailedEvent:
		rl.handleUnJailedLog(vLog, selectedEvent)
	}
}

func (rl *RootChainListener) handleNewHeaderBlockLog(vLog types.Log, selectedEvent *abi.Event) {
	logBytes, err := json.Marshal(vLog)
	if err != nil {
		rl.Logger.Error("Failed to marshal log", "Error", err)
	}

	if isCurrentValidator, delay := util.CalculateTaskDelay(selectedEvent, rl.cliCtx.Codec); isCurrentValidator {
		rl.SendTaskWithDelay("sendCheckpointAckToHeimdall", selectedEvent.Name, logBytes, delay, selectedEvent)
	}
}

func (rl *RootChainListener) handleStakedLog(vLog types.Log, selectedEvent *abi.Event) {
	logBytes, err := json.Marshal(vLog)
	if err != nil {
		rl.Logger.Error("Failed to marshal log", "Error", err)
	}

	pubKey := helper.GetPubKey()

	event := new(stakinginfo.StakinginfoStaked)
	if err = helper.UnpackLog(rl.stakingInfoAbi, event, selectedEvent.Name, &vLog); err != nil {
		rl.Logger.Error("Error while parsing event", "name", selectedEvent.Name, "error", err)
	}

	if !helper.IsPubKeyFirstByteValid(pubKey[0:1]) {
		rl.Logger.Error("public key first byte mismatch", "expected", "0x04", "received", pubKey[0:1])
	}

	if bytes.Equal(event.SignerPubkey, pubKey[1:]) {
		delay := util.TaskDelayBetweenEachVal
		rl.SendTaskWithDelay("sendValidatorJoinToHeimdall", selectedEvent.Name, logBytes, delay, event)
	} else if isCurrentValidator, delay := util.CalculateTaskDelay(event, rl.cliCtx.Codec); isCurrentValidator {
		delay = delay + util.TaskDelayBetweenEachVal
		rl.SendTaskWithDelay("sendValidatorJoinToHeimdall", selectedEvent.Name, logBytes, delay, event)
	}
}

func (rl *RootChainListener) handleStakeUpdateLog(vLog types.Log, selectedEvent *abi.Event) {
	logBytes, err := json.Marshal(vLog)
	if err != nil {
		rl.Logger.Error("Failed to marshal log", "Error", err)
	}

	event := new(stakinginfo.StakinginfoStakeUpdate)
	if err = helper.UnpackLog(rl.stakingInfoAbi, event, selectedEvent.Name, &vLog); err != nil {
		rl.Logger.Error("Error while parsing event", "name", selectedEvent.Name, "error", err)
	}

	if util.IsEventSender(event.ValidatorId.Uint64(), rl.cliCtx.Codec) {
		rl.SendTaskWithDelay("sendStakeUpdateToHeimdall", selectedEvent.Name, logBytes, 0, event)
	} else if isCurrentValidator, delay := util.CalculateTaskDelay(event, rl.cliCtx.Codec); isCurrentValidator {
		rl.SendTaskWithDelay("sendStakeUpdateToHeimdall", selectedEvent.Name, logBytes, delay, event)
	}
}

func (rl *RootChainListener) handleSignerChangeLog(vLog types.Log, selectedEvent *abi.Event) {
	logBytes, err := json.Marshal(vLog)
	if err != nil {
		rl.Logger.Error("Failed to marshal log", "Error", err)
	}

	pubKey := helper.GetPubKey()

	event := new(stakinginfo.StakinginfoSignerChange)
	if err = helper.UnpackLog(rl.stakingInfoAbi, event, selectedEvent.Name, &vLog); err != nil {
		rl.Logger.Error("Error while parsing event", "name", selectedEvent.Name, "error", err)
	}

	if bytes.Equal(event.SignerPubkey, pubKey[1:]) && helper.IsPubKeyFirstByteValid(pubKey[0:1]) {
		rl.SendTaskWithDelay("sendSignerChangeToHeimdall", selectedEvent.Name, logBytes, 0, event)
	} else if isCurrentValidator, delay := util.CalculateTaskDelay(event, rl.cliCtx.Codec); isCurrentValidator {
		rl.SendTaskWithDelay("sendSignerChangeToHeimdall", selectedEvent.Name, logBytes, delay, event)
	}
}

func (rl *RootChainListener) handleUnstakeInitLog(vLog types.Log, selectedEvent *abi.Event) {
	logBytes, err := json.Marshal(vLog)
	if err != nil {
		rl.Logger.Error("Failed to marshal log", "Error", err)
	}

	event := new(stakinginfo.StakinginfoUnstakeInit)
	if err = helper.UnpackLog(rl.stakingInfoAbi, event, selectedEvent.Name, &vLog); err != nil {
		rl.Logger.Error("Error while parsing event", "name", selectedEvent.Name, "error", err)
	}

	if util.IsEventSender(event.ValidatorId.Uint64(), rl.cliCtx.Codec) {
		rl.SendTaskWithDelay("sendUnstakeInitToHeimdall", selectedEvent.Name, logBytes, 0, event)
	} else if isCurrentValidator, delay := util.CalculateTaskDelay(event, rl.cliCtx.Codec); isCurrentValidator {
		rl.SendTaskWithDelay("sendUnstakeInitToHeimdall", selectedEvent.Name, logBytes, delay, event)
	}
}

func (rl *RootChainListener) handleStateSyncedLog(vLog types.Log, selectedEvent *abi.Event) {
	logBytes, err := json.Marshal(vLog)
	if err != nil {
		rl.Logger.Error("Failed to marshal log", "Error", err)
	}

	event := new(statesender.StatesenderStateSynced)
	if err = helper.UnpackLog(rl.stateSenderAbi, event, selectedEvent.Name, &vLog); err != nil {
		rl.Logger.Error("Error while parsing event", "name", selectedEvent.Name, "error", err)
	}

	rl.Logger.Info("StateSyncedEvent: detected", "stateSyncId", event.Id)

	if isCurrentValidator, delay := util.CalculateTaskDelay(event, rl.cliCtx.Codec); isCurrentValidator {
		rl.SendTaskWithDelay("sendStateSyncedToHeimdall", selectedEvent.Name, logBytes, delay, event)
	}
}

func (rl *RootChainListener) handleTopUpFeeLog(vLog types.Log, selectedEvent *abi.Event) {
	logBytes, err := json.Marshal(vLog)
	if err != nil {
		rl.Logger.Error("Failed to marshal log", "Error", err)
	}

	event := new(stakinginfo.StakinginfoTopUpFee)
	if err = helper.UnpackLog(rl.stakingInfoAbi, event, selectedEvent.Name, &vLog); err != nil {
		rl.Logger.Error("Error while parsing event", "name", selectedEvent.Name, "error", err)
	}

	if bytes.Equal(event.User.Bytes(), helper.GetAddress()) {
		rl.SendTaskWithDelay("sendTopUpFeeToHeimdall", selectedEvent.Name, logBytes, 0, event)
	} else if isCurrentValidator, delay := util.CalculateTaskDelay(event, rl.cliCtx.Codec); isCurrentValidator {
		rl.SendTaskWithDelay("sendTopUpFeeToHeimdall", selectedEvent.Name, logBytes, delay, event)
	}
}

func (rl *RootChainListener) handleSlashedLog(vLog types.Log, selectedEvent *abi.Event) {
	logBytes, err := json.Marshal(vLog)
	if err != nil {
		rl.Logger.Error("Failed to marshal log", "Error", err)
	}

	if isCurrentValidator, delay := util.CalculateTaskDelay(selectedEvent, rl.cliCtx.Codec); isCurrentValidator {
		rl.SendTaskWithDelay("sendTickAckToHeimdall", selectedEvent.Name, logBytes, delay, selectedEvent)
	}
}

func (rl *RootChainListener) handleUnJailedLog(vLog types.Log, selectedEvent *abi.Event) {
	logBytes, err := json.Marshal(vLog)
	if err != nil {
		rl.Logger.Error("Failed to marshal log", "Error", err)
	}

	event := new(stakinginfo.StakinginfoUnJailed)
	if err = helper.UnpackLog(rl.stakingInfoAbi, event, selectedEvent.Name, &vLog); err != nil {
		rl.Logger.Error("Error while parsing event", "name", selectedEvent.Name, "error", err)
	}

	if util.IsEventSender(event.ValidatorId.Uint64(), rl.cliCtx.Codec) {
		rl.SendTaskWithDelay("sendUnjailToHeimdall", selectedEvent.Name, logBytes, 0, event)
	} else if isCurrentValidator, delay := util.CalculateTaskDelay(event, rl.cliCtx.Codec); isCurrentValidator {
		rl.SendTaskWithDelay("sendUnjailToHeimdall", selectedEvent.Name, logBytes, delay, event)
	}
}
