package types

import (
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
)

const (
	// ModuleName is the name of the staking module
	ModuleName = "stake"

	// StoreKey is the string store representation
	StoreKey = ModuleName

	// RouterKey is the msg router key for the stake module
	RouterKey = ModuleName

	// DefaultLogIndexUnit represents the default unit for txHash+logIndex
	DefaultLogIndexUnit = 100000
)

var (
	ValidatorsKey                   = []byte{0x21} // prefix for each key to a validator
	ValidatorSetKey                 = []byte{0x22} // prefix for each key for the validator map
	CurrentValidatorSetKey          = []byte{0x23} // key to store the current validator set
	StakeSequenceKey                = []byte{0x24} // prefix for each key for the staking sequence map
	SignerKey                       = []byte{0x25} // prefix for signer address for signer map
	LastBlockTxsKey                 = []byte{0x26} // key to store last block's txs
	PreviousBlockValidatorSetKey    = []byte{0x27} // key to store the previous block's validator set
	PenultimateBlockValidatorSetKey = []byte{0x28} // key to store the validator set from 2 blocks ago
)

type PubKey [65]byte

// EmptyPubKey represents an empty pub key
var EmptyPubKey = PubKey{}

var Secp256k1Type = secp256k1.GenPrivKey().PubKey().Type()
