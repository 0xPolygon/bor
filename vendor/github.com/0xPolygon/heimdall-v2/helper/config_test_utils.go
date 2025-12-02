package helper

import (
	"github.com/cometbft/cometbft/crypto/secp256k1"
	serverconfig "github.com/cosmos/cosmos-sdk/server/config"
)

// InitTestHeimdallConfig initializes test config for the unit tests
func InitTestHeimdallConfig(chain string) {
	customAppConf := CustomAppConfig{
		Config: *serverconfig.DefaultConfig(),
		Custom: GetDefaultHeimdallConfig(),
	}

	switch chain {
	case MumbaiChain:
		customAppConf.Custom.Chain = MumbaiChain
	case AmoyChain:
		customAppConf.Custom.Chain = AmoyChain
	case MainChain:
		customAppConf.Custom.Chain = MainChain
	}

	SetTestConfig(customAppConf)

	privKeyObject = secp256k1.GenPrivKey()
	pubKeyObject = privKeyObject.PubKey().(secp256k1.PubKey)
}

// SetTestConfig sets test configuration
func SetTestConfig(_conf CustomAppConfig) {
	conf = _conf
}

// SetTestPrivPubKey sets test the private and public keys for testing
func SetTestPrivPubKey(privKey secp256k1.PrivKey) {
	privKeyObject = privKey
	privKeyObject.PubKey()
	pubKey, ok := privKeyObject.PubKey().(secp256k1.PubKey)
	if !ok {
		panic("pub key is not of type secp256k1.PrivKey")
	}
	pubKeyObject = pubKey
}

// SetTestInitialHeight sets test the initial height for testing
func SetTestInitialHeight(height int64) {
	initialHeight = height
}
