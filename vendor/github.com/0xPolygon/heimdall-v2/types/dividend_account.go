package types

import (
	"bytes"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/cbergoon/merkletree"
	addCodec "github.com/cosmos/cosmos-sdk/codec/address"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/crypto/sha3"
)

// GetAccountRootHash returns rootHash of Validator Account State Tree
func GetAccountRootHash(dividendAccounts []DividendAccount) ([]byte, error) {
	tree, err := GetAccountTree(dividendAccounts)
	if err != nil {
		return nil, err
	}

	return tree.Root.Hash, nil
}

// GetAccountTree returns rootHash of Validator Account State Tree
func GetAccountTree(dividendAccounts []DividendAccount) (*merkletree.MerkleTree, error) {
	// Sort the dividendAccounts by ID
	dividendAccounts, err := SortDividendAccountByAddress(dividendAccounts)
	if err != nil {
		return nil, err
	}

	list := make([]merkletree.Content, len(dividendAccounts))

	for i := 0; i < len(dividendAccounts); i++ {
		list[i] = dividendAccounts[i]
	}

	tree, err := merkletree.NewTreeWithHashStrategy(list, sha3.NewLegacyKeccak256)
	if err != nil {
		return nil, err
	}

	return tree, nil
}

// VerifyAccountProof returns proof of dividend Account
func VerifyAccountProof(dividendAccounts []DividendAccount, userAddr, proofToVerify string) (bool, error) {
	proof, _, err := GetAccountProof(dividendAccounts, userAddr)
	if err != nil {
		//nolint: nilerr
		return false, nil
	}

	// check proof bytes
	if bytes.Equal(common.FromHex(proofToVerify), proof) {
		return true, nil
	}

	return false, nil
}

// GetAccountProof returns proof of dividend Account
func GetAccountProof(dividendAccounts []DividendAccount, userAddr string) ([]byte, uint64, error) {
	// Sort the dividendAccounts by user address
	dividendAccounts, err := SortDividendAccountByAddress(dividendAccounts)
	if err != nil {
		return nil, 0, err
	}

	var (
		list    = make([]merkletree.Content, len(dividendAccounts))
		account DividendAccount
	)

	index := uint64(0)

	for i := 0; i < len(dividendAccounts); i++ {
		list[i] = dividendAccounts[i]

		if strings.EqualFold(dividendAccounts[i].User, userAddr) {
			account = dividendAccounts[i]
			index = uint64(i)
		}
	}

	tree, err := merkletree.NewTreeWithHashStrategy(list, sha3.NewLegacyKeccak256)
	if err != nil {
		return nil, 0, err
	}

	branchArray, _, err := tree.GetMerklePath(account)

	// concatenate branch array
	proof := AppendBytes32(branchArray...)

	return proof, index, err
}

// SortDividendAccountByAddress sorts DividendAccounts by address
func SortDividendAccountByAddress(dividendAccounts []DividendAccount) ([]DividendAccount, error) {
	var sortErr error

	sort.Slice(dividendAccounts, func(i, j int) bool {
		divAccBytesI, err := addCodec.NewHexCodec().StringToBytes(dividendAccounts[i].User)
		if err != nil {
			sortErr = err
		}
		divAccBytesJ, err := addCodec.NewHexCodec().StringToBytes(dividendAccounts[j].User)
		if err != nil {
			sortErr = err
		}

		return bytes.Compare(divAccBytesI, divAccBytesJ) < 0
	})

	return dividendAccounts, sortErr
}

func AppendBytes32(data ...[]byte) []byte {
	var result []byte

	for _, v := range data {
		paddedV, err := convertTo32(v)
		if err == nil {
			result = append(result, paddedV[:]...)
		}
	}

	return result
}

func convertTo32(input []byte) (output [32]byte, err error) {
	l := len(input)
	if l > 32 {
		return output, fmt.Errorf("input length %d exceeds 32 bytes", l)
	}
	if l == 0 {
		return output, nil
	}

	copy(output[32-l:], input[:])

	return
}

// CalculateHash hashes the values of a DividendAccount
func (da DividendAccount) CalculateHash() ([]byte, error) {
	fee, _ := big.NewInt(0).SetString(da.FeeAmount, 10)
	addressBytes, err := addCodec.NewHexCodec().StringToBytes(da.User)
	if err != nil {
		return nil, err
	}

	divAccountHash := crypto.Keccak256(AppendBytes32(
		addressBytes,
		fee.Bytes(),
	))

	return divAccountHash, nil
}

func (da DividendAccount) Equals(other merkletree.Content) (bool, error) {
	return da.User == other.(DividendAccount).User, nil
}
