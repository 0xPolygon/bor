package params

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// Stablecoins on Polygon PoS mainnet that the payment lane can list.
var (
	MainnetUSDC  = common.HexToAddress("0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359") // native USDC (Circle)
	MainnetUSDCe = common.HexToAddress("0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174") // bridged USDC.e
	MainnetUSDT0 = common.HexToAddress("0xc2132D05D31c914a87C6611C10748AEb04B58e8F") // USDT0
	MainnetDAI   = common.HexToAddress("0x8f3Cf7ad23Cd3CaDbD9735AFf958023239c6A063") // bridged DAI
	MainnetPUSD  = common.HexToAddress("0xC011a7E12a19f7B1f670d46F03B03f3342E82DFB") // Polymarket USD (collateral)

	AmoyUSDC = common.HexToAddress("0x41E94Eb019C0762f9Bfcf9Fb1E58725BfB0e7582") // Circle testnet USDC
)

// MainnetPaymentLaneAssets is the proposed mainnet list. pUSD is deliberately
// not in it: it is exchange collateral, and listing it routes most of its
// discount through exchange payouts.
var MainnetPaymentLaneAssets = []common.Address{MainnetUSDC, MainnetUSDCe, MainnetUSDT0, MainnetDAI}

// paymentLaneOneCent is one cent of each known stablecoin, in token units:
// most direct stablecoin transfers on mainnet move a single base unit
// (dust, not payments), and the lane should not subsidise them.
var paymentLaneOneCent = map[common.Address]*big.Int{
	MainnetUSDC:  big.NewInt(10_000),
	MainnetUSDCe: big.NewInt(10_000),
	MainnetUSDT0: big.NewInt(10_000),
	MainnetDAI:   big.NewInt(10_000_000_000_000_000),
	MainnetPUSD:  big.NewInt(10_000),
	AmoyUSDC:     big.NewInt(10_000),
}

// DefaultPaymentLaneConfig returns the proposed parameters for assets: 80%
// off the base fee of payment gas and a one-cent minimum for the stablecoins
// it knows. The tip is charged in full: a cut tip still buys the nominal
// priority, since producers order by the nominal tip. The discount is full
// while the parent is at or below 40% of its gas limit and fades linearly to
// none at 70%, the producers' high EIP-1559 target (dynamic target gas).
// Starting the fade at 40% gives nearly every payment the same full discount,
// so wallets can predict the price; discounted payments that fill blocks past
// it to push the base fee up pay more of it the fuller they make them, and in
// full at 70%. The fade must not end above that target, or the lane would
// discount gas that raises the base fee past the setpoint. Allowance pulls
// (transferFrom by a spender) count, so exchange settlement and merchant or
// subscription pulls are priced like the payer sending the funds directly.
func DefaultPaymentLaneConfig(assets []common.Address) *PaymentLaneConfig {
	cfg := &PaymentLaneConfig{
		Assets:               append([]common.Address(nil), assets...),
		BaseFeeDiscountBps:   8_000,
		ActiveGasPercentage:  40,
		RampEndGasPercentage: 70,
		CountAllowancePulls:  true,
	}

	for _, asset := range assets {
		if cent, ok := paymentLaneOneCent[asset]; ok {
			if cfg.MinAmounts == nil {
				cfg.MinAmounts = make(map[common.Address]*big.Int)
			}

			cfg.MinAmounts[asset] = new(big.Int).Set(cent)
		}
	}

	return cfg
}
