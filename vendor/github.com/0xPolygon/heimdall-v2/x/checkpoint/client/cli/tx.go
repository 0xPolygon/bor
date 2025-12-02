package cli

import (
	"bytes"
	"fmt"
	"math/big"
	"strconv"

	"cosmossdk.io/core/address"
	"github.com/cosmos/cosmos-sdk/client"
	codec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/ethereum/go-ethereum/common"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/0xPolygon/heimdall-v2/common/cli"
	"github.com/0xPolygon/heimdall-v2/helper"
	chainmanagerTypes "github.com/0xPolygon/heimdall-v2/x/chainmanager/types"
	checkpointTypes "github.com/0xPolygon/heimdall-v2/x/checkpoint/types"
	stakeTypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
)

var logger = helper.Logger.With("module", "checkpoint/client/cli")

// NewTxCmd returns a root CLI command handler for all x/checkpoint transaction commands.
func NewTxCmd() *cobra.Command {
	txCmd := &cobra.Command{
		Use:                        checkpointTypes.ModuleName,
		Short:                      "Commands for the Checkpoint module",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}

	addressCodec := codec.NewHexCodec()

	txCmd.AddCommand(
		SendCheckpointCmd(addressCodec),
		SendCheckpointAckCmd(),
	)

	return txCmd
}

// SendCheckpointCmd returns a CLI command handler for creating a MsgCheckpoint transaction.
func SendCheckpointCmd(addressCodec address.Codec) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send-checkpoint",
		Short: "Send a checkpoint to CometBFT and Ethereum",
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			if viper.GetBool(FlagAutoConfigure) {
				chainManagerQueryClient := chainmanagerTypes.NewQueryClient(clientCtx)
				stakeQueryClient := stakeTypes.NewQueryClient(clientCtx)
				checkpointQueryClient := checkpointTypes.NewQueryClient(clientCtx)

				chainManagerParams, err := chainManagerQueryClient.GetChainManagerParams(cmd.Context(), &chainmanagerTypes.QueryParamsRequest{})
				if err != nil {
					return fmt.Errorf("failed to fetch chain manager params: %w", err)
				}
				proposerResponse, err := stakeQueryClient.GetCurrentProposer(cmd.Context(), &stakeTypes.QueryCurrentProposerRequest{})
				if err != nil {
					return err
				}

				signerBytes, err := addressCodec.StringToBytes(proposerResponse.Validator.Signer)
				if err != nil {
					return fmt.Errorf("invalid validator signer address: %w", err)
				}

				if !bytes.Equal(signerBytes, helper.GetAddress()) {
					return fmt.Errorf("please wait for your turn to propose a checkpoint. Current proposer: %v", proposerResponse.Validator.Signer)
				}

				nextCheckpointResponse, err := checkpointQueryClient.GetNextCheckpoint(cmd.Context(), &checkpointTypes.QueryNextCheckpointRequest{})
				if err != nil {
					return err
				}

				msg := checkpointTypes.NewMsgCheckpointBlock(
					proposerResponse.Validator.Signer,
					nextCheckpointResponse.Checkpoint.StartBlock,
					nextCheckpointResponse.Checkpoint.EndBlock,
					nextCheckpointResponse.Checkpoint.RootHash,
					nextCheckpointResponse.Checkpoint.AccountRootHash,
					chainManagerParams.Params.ChainParams.BorChainId,
				)

				return cli.BroadcastMsg(clientCtx, proposerResponse.Validator.Signer, msg, logger)
			}

			// Bor ChainID
			borChainID := viper.GetString(FlagBorChainID)
			if borChainID == "" {
				return fmt.Errorf("bor chain ID cannot be empty")
			}

			proposerAddress := viper.GetString(FlagProposerAddress)
			if proposerAddress == "" {
				proposerAddress, err = helper.GetAddressString()
				if err != nil {
					return fmt.Errorf("invalid proposer address: %w", err)
				}
			}

			// Start Block
			startBlockStr := viper.GetString(FlagStartBlock)
			if startBlockStr == "" {
				return fmt.Errorf("start block cannot be empty")
			}

			startBlock, err := strconv.ParseUint(startBlockStr, 10, 64)
			if err != nil {
				return err
			}

			// End Block
			endBlockStr := viper.GetString(FlagEndBlock)
			if endBlockStr == "" {
				return fmt.Errorf("end block cannot be empty")
			}

			endBlock, err := strconv.ParseUint(endBlockStr, 10, 64)
			if err != nil {
				return err
			}

			// Root Hash
			rootHashStr := viper.GetString(FlagRootHash)
			if rootHashStr == "" {
				return fmt.Errorf("root hash cannot be empty")
			}

			// Account Root Hash
			accountRootHashStr := viper.GetString(FlagAccountRootHash)
			if accountRootHashStr == "" {
				return fmt.Errorf("account root hash cannot be empty")
			}

			msg := checkpointTypes.NewMsgCheckpointBlock(
				proposerAddress,
				startBlock,
				endBlock,
				common.FromHex(rootHashStr),
				common.FromHex(accountRootHashStr),
				borChainID,
			)

			return cli.BroadcastMsg(clientCtx, proposerAddress, msg, logger)
		},
	}

	cmd.Flags().StringP(FlagProposerAddress, "p", "", "--proposer=<proposer-address>")
	cmd.Flags().String(FlagStartBlock, "", "--start-block=<start-block-number>")
	cmd.Flags().String(FlagEndBlock, "", "--end-block=<end-block-number>")
	cmd.Flags().StringP(FlagRootHash, "r", "", "--root-hash=<root-hash>")
	cmd.Flags().String(FlagAccountRootHash, "", "--account-root=<account-root>")
	cmd.Flags().String(FlagBorChainID, "", "--bor-chain-id=<bor-chain-id>")
	cmd.Flags().Bool(FlagAutoConfigure, false, "--auto-configure=true/false")

	return cmd
}

// SendCheckpointAckCmd returns a CLI command handler for creating a MsgCpAck transaction.
func SendCheckpointAckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send-ack",
		Short: "Send an acknowledgment for a checkpoint in the buffer",
		RunE: func(cmd *cobra.Command, args []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			if viper.GetBool(FlagAutoConfigure) {
				// Auto-configure mode
				contractCaller, err := helper.NewContractCaller()
				if err != nil {
					return err
				}

				queryClient := chainmanagerTypes.NewQueryClient(clientCtx)
				chainManagerParams, err := queryClient.GetChainManagerParams(cmd.Context(), &chainmanagerTypes.QueryParamsRequest{})
				if err != nil {
					return fmt.Errorf("failed to fetch chain manager params: %w", err)
				}

				rootChainInstance, err := contractCaller.GetRootChainInstance(chainManagerParams.Params.ChainParams.RootChainAddress)
				if err != nil {
					return fmt.Errorf("failed to get root chain instance: %w", err)
				}

				checkpointQueryClient := checkpointTypes.NewQueryClient(clientCtx)
				checkpointParams, err := checkpointQueryClient.GetCheckpointParams(cmd.Context(), &checkpointTypes.QueryParamsRequest{})
				if err != nil {
					return fmt.Errorf("failed to fetch checkpoint params: %w", err)
				}

				blockNum, err := contractCaller.CurrentHeaderBlock(rootChainInstance, checkpointParams.Params.ChildChainBlockInterval)
				if err != nil {
					return fmt.Errorf("failed to get current header block number: %w", err)
				}

				block, err := rootChainInstance.HeaderBlocks(nil, big.NewInt(int64(blockNum*checkpointParams.Params.ChildChainBlockInterval)))
				if err != nil {
					return fmt.Errorf("failed to get header block: %w", err)
				}

				proposerAddress, err := helper.GetAddressString()
				if err != nil {
					return fmt.Errorf("failed to get proposer address: %w", err)
				}

				msg := checkpointTypes.NewMsgCpAck(
					proposerAddress,
					blockNum,
					block.Proposer.Hex(),
					block.Start.Uint64(),
					block.End.Uint64(),
					block.Root[:],
				)

				return cli.BroadcastMsg(clientCtx, proposerAddress, &msg, logger)
			}

			proposerAddress := viper.GetString(FlagProposerAddress)
			if proposerAddress == "" {
				proposerAddress, err = helper.GetAddressString()
				if err != nil {
					return fmt.Errorf("invalid proposer address: %w", err)
				}
			}

			headerBlockStr := viper.GetString(FlagHeaderNumber)
			if headerBlockStr == "" {
				return fmt.Errorf("header number cannot be empty")
			}

			headerBlock, err := strconv.ParseUint(headerBlockStr, 10, 64)
			if err != nil {
				return err
			}

			txHashStr := viper.GetString(FlagCheckpointTxHash)
			if txHashStr == "" {
				return fmt.Errorf("checkpoint transaction hash cannot be empty")
			}

			txHash := common.HexToHash(txHashStr)

			// Get header block details
			contractCaller, err := helper.NewContractCaller()
			if err != nil {
				return err
			}

			// Fetch params
			queryClient := chainmanagerTypes.NewQueryClient(clientCtx)
			chainManagerParams, err := queryClient.GetChainManagerParams(cmd.Context(), &chainmanagerTypes.QueryParamsRequest{})
			if err != nil {
				return err
			}

			receipt, err := contractCaller.GetConfirmedTxReceipt(txHash, chainManagerParams.Params.MainChainTxConfirmations)
			if err != nil || receipt == nil {
				return fmt.Errorf("transaction %s is not confirmed yet, please wait and try again later", txHash)
			}

			logIndex := viper.GetInt64(FlagCheckpointLogIndex)
			if logIndex < 0 {
				return fmt.Errorf("log index must be a non-negative integer")
			}

			decodedEvent, err := contractCaller.DecodeNewHeaderBlockEvent(
				chainManagerParams.Params.ChainParams.RootChainAddress,
				receipt,
				uint64(logIndex),
			)
			if err != nil {
				return fmt.Errorf("invalid transaction for header block. error: %w", err)
			}

			msg := checkpointTypes.NewMsgCpAck(
				proposerAddress,
				headerBlock,
				decodedEvent.Proposer.String(),
				decodedEvent.Start.Uint64(),
				decodedEvent.End.Uint64(),
				decodedEvent.Root[:],
			)

			return cli.BroadcastMsg(clientCtx, proposerAddress, &msg, logger)
		},
	}

	cmd.Flags().StringP(FlagProposerAddress, "p", "", "--proposer=<proposer-address>")
	cmd.Flags().String(FlagHeaderNumber, "", "--header=<header-index>")
	cmd.Flags().StringP(FlagCheckpointTxHash, "t", "", "--txhash=<checkpoint-txhash>")
	cmd.Flags().String(FlagCheckpointLogIndex, "", "--log-index=<log-index>")
	cmd.Flags().Bool(FlagAutoConfigure, false, "--auto-configure=true/false")

	return cmd
}
