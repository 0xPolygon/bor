package util

type CometBFTUnconfirmedTxs struct {
	Result struct {
		Total string   `json:"total"`
		Txs   []string `json:"txs"`
	} `json:"result"`
}
