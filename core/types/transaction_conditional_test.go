package types

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
)

func TestKnownAccounts(t *testing.T) {
	t.Parallel()

	requestRaw := []byte(`{"0xadd1add1add1add1add1add1add1add1add1add1": "0x000000000000000000000000313aadca1750caadc7bcb26ff08175c95dcf8e38", "0xadd2add2add2add2add2add2add2add2add2add2": {"0x0000000000000000000000000000000000000000000000000000000000000aaa": "0x0000000000000000000000000000000000000000000000000000000000000bbb", "0x0000000000000000000000000000000000000000000000000000000000000ccc": "0x0000000000000000000000000000000000000000000000000000000000000ddd"}}`)

	accs := &KnownAccounts{}

	err := json.Unmarshal(requestRaw, accs)
	require.NoError(t, err)

	expected := &KnownAccounts{
		common.HexToAddress("0xadd1add1add1add1add1add1add1add1add1add1"): SingleFromHex("0x000000000000000000000000313aadca1750caadc7bcb26ff08175c95dcf8e38"),
		common.HexToAddress("0xadd2add2add2add2add2add2add2add2add2add2"): FromMap(map[string]string{
			"0x0000000000000000000000000000000000000000000000000000000000000aaa": "0x0000000000000000000000000000000000000000000000000000000000000bbb",
			"0x0000000000000000000000000000000000000000000000000000000000000ccc": "0x0000000000000000000000000000000000000000000000000000000000000ddd",
		}),
	}

	require.Equal(t, expected, accs)
}

func TestKnownAccountsLength(t *testing.T) {
	t.Parallel()

	accs := KnownAccounts{}
	require.NoError(t, json.Unmarshal([]byte(`{
		"0xadd1add1add1add1add1add1add1add1add1add1": "0x000000000000000000000000313aadca1750caadc7bcb26ff08175c95dcf8e38",
		"0xadd2add2add2add2add2add2add2add2add2add2": {"0x0000000000000000000000000000000000000000000000000000000000000aaa": "0x0000000000000000000000000000000000000000000000000000000000000bbb", "0x0000000000000000000000000000000000000000000000000000000000000ccc": "0x0000000000000000000000000000000000000000000000000000000000000ddd"},
		"0xadd3add3add3add3add3add3add3add3add3add3": {},
		"0xadd4add4add4add4add4add4add4add4add4add4": null
	}`), &accs))
	// single 1 + storage 2 + empty map 1 + null 1
	require.Equal(t, 5, accs.Length())

	// 1000 empty entries used to count as 0 and pass the limit.
	empty := KnownAccounts{}
	for i := range 1000 {
		empty[common.BigToAddress(big.NewInt(int64(i)))] = &Value{Storage: map[common.Hash]common.Hash{}}
	}
	require.Error(t, empty.ValidateLength())
	require.NoError(t, KnownAccounts(nil).ValidateLength())
}
