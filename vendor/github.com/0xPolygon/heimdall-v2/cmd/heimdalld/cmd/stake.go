package heimdalld

import (
	"errors"
	"math/big"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/ethereum/go-ethereum/common"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	bridge "github.com/0xPolygon/heimdall-v2/bridge/util"
	"github.com/0xPolygon/heimdall-v2/helper"
	stakingcli "github.com/0xPolygon/heimdall-v2/x/stake/client/cli"
)

// StakeCmd stakes for a validator
func StakeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stake",
		Short: "Stake pol tokens for your account",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			helper.InitHeimdallConfig("")

			validatorStr := viper.GetString(stakingcli.FlagValidatorAddress)
			stakeAmountStr := viper.GetString(stakingcli.FlagAmount)
			feeAmountStr := viper.GetString(stakingcli.FlagFeeAmount)
			acceptDelegation := viper.GetBool(stakingcli.FlagAcceptDelegation)

			// validator str
			if validatorStr == "" {
				return errors.New("validator address is required")
			}

			// stake amount
			stakeAmount, ok := big.NewInt(0).SetString(stakeAmountStr, 10)
			if !ok {
				return errors.New("invalid stake amount")
			}

			// fee amount
			feeAmount, ok := big.NewInt(0).SetString(feeAmountStr, 10)
			if !ok {
				return errors.New("invalid fee amount")
			}

			// contract caller
			contractCaller, err := helper.NewContractCaller()
			if err != nil {
				return err
			}

			params, err := bridge.GetChainmanagerParams(clientCtx.Codec)
			if err != nil {
				return err
			}

			stakingManagerAddress := params.ChainParams.StakingManagerAddress
			stakeManagerInstance, err := contractCaller.GetStakeManagerInstance(stakingManagerAddress)
			if err != nil {
				return err
			}

			return contractCaller.StakeFor(
				common.HexToAddress(validatorStr),
				stakeAmount,
				feeAmount,
				acceptDelegation,
				common.HexToAddress(stakingManagerAddress),
				stakeManagerInstance,
			)
		},
	}

	cmd.Flags().String(stakingcli.FlagValidatorAddress, "", "--validator=<validator address here>")
	cmd.Flags().String(stakingcli.FlagAmount, "10000000000000000000", "--staked-amount=<stake amount>, if left blank it will be assigned as 10 pol tokens")
	cmd.Flags().String(stakingcli.FlagFeeAmount, "5000000000000000000", "--fee-amount=<heimdall fee amount>, if left blank will be assigned as 5 pol tokens")
	cmd.Flags().Bool(stakingcli.FlagAcceptDelegation, true, "--accept-delegation=<accept delegation>, if left blank will be assigned as true")

	return cmd
}

// ApproveCmd approves tokens for a validator
func ApproveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approve",
		Short: "Approve the tokens to stake",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			clientCtx, err := client.GetClientTxContext(cmd)
			if err != nil {
				return err
			}

			helper.InitHeimdallConfig("")

			stakeAmountStr := viper.GetString(stakingcli.FlagAmount)
			feeAmountStr := viper.GetString(stakingcli.FlagFeeAmount)

			// stake amount
			stakeAmount, ok := big.NewInt(0).SetString(stakeAmountStr, 10)
			if !ok {
				return errors.New("invalid stake amount")
			}

			// fee amount
			feeAmount, ok := big.NewInt(0).SetString(feeAmountStr, 10)
			if !ok {
				return errors.New("invalid fee amount")
			}

			contractCaller, err := helper.NewContractCaller()
			if err != nil {
				return err
			}

			params, err := bridge.GetChainmanagerParams(clientCtx.Codec)
			if err != nil {
				return err
			}

			stakingManagerAddress := params.ChainParams.StakingManagerAddress
			polTokenAddress := params.ChainParams.PolTokenAddress

			polTokenInstance, err := contractCaller.GetTokenInstance(polTokenAddress)
			if err != nil {
				return err
			}

			return contractCaller.ApproveTokens(stakeAmount.Add(stakeAmount, feeAmount), common.HexToAddress(stakingManagerAddress), common.HexToAddress(polTokenAddress), polTokenInstance)
		},
	}

	cmd.Flags().String(stakingcli.FlagAmount, "10000000000000000000", "--staked-amount=<stake amount>, if left blank will be assigned as 10 pol tokens")
	cmd.Flags().String(stakingcli.FlagFeeAmount, "5000000000000000000", "--fee-amount=<heimdall fee amount>, if left blank will be assigned as 5 pol tokens")

	return cmd
}
