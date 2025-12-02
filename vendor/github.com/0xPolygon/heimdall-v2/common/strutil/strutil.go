package strutil

import "github.com/ethereum/go-ethereum/common"

func HashesToString(hashes [][]byte) string {
	hashesStr := ""
	for _, hash := range hashes {
		hashesStr += common.Bytes2Hex(hash) + " "
	}
	return hashesStr
}
