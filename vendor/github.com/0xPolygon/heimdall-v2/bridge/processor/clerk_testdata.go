package processor

import "github.com/0xPolygon/heimdall-v2/bridge/util"

const (
	dummyCometBFTNode      = "http://localhost:26657"
	dummyHeimdallServerUrl = "https://dummy-heimdall-api-testnet.polygon.technology"

	chainManagerParamsUrl      = dummyHeimdallServerUrl + util.ChainManagerParamsURL
	chainManagerParamsResponse = `
	{
	"height": "0",
	"result": {
		"chain_params": {
			"bor_chain_id": "80001",
			"heimdall_chain_id": "heimdall-80001",
			"pol_token_address": "0x499d11E0b6eAC7c0593d8Fb292DCBbF815Fb29Ae",
			"staking_manager_address": "0x4864d89DCE4e24b2eDF64735E014a7E4154bfA7A",
			"slash_manager_address": "0x93D8f8A1A88498b258ceb69dD82311962374269C",
			"root_chain_address": "0x2890bA17EfE978480615e330ecB65333b880928e",
			"staking_info_address": "0x318EeD65F064904Bc6E0e3842940c5972BC8E38f",
			"state_sender_address": "0xEAa852323826C71cd7920C3b4c007184234c3945",
			"state_receiver_address": "0x0000000000000000000000000000000000001001",
			"validator_set_address": "0x0000000000000000000000000000000000001000"
			},
		"main_chain_tx_confirmations": "6",
		"bor_chain_tx_confirmations": "10"
		}
	}`

	getAccountUrl      = dummyHeimdallServerUrl + "/auth/accounts/9FB29AAC15B9A4B7F17C3385939B007540F4D791"
	getAccountUrl2     = dummyHeimdallServerUrl + "/auth/accounts/3F17F1962B36E491B30A40B2405849E597BA5FB5"
	getAccountResponse = `
	{
	"type": "auth/Account",
	"value": {
		"address": "0x5973918275c01f50555d44e92c9d9b353cadad54",
		"coins": [{
			"denom": "pol",
			"amount": "10000000000000000000"
		}],
		"public_key": null,
		"account_number": "0",
		"sequence_number": "0",
		"name": "",
		"permissions": []
	}
	}`

	isOldTxUrl      = dummyHeimdallServerUrl + "/clerk/is-old-tx?logindex=0&txhash=0x6d428739815d7c84cf89db055158861b089e0fd649676a0243a2a2d204c1d854"
	isOldTxResponse = `
	{
		"result": false
	}`

	checkpointCountUrl      = dummyHeimdallServerUrl + util.CountCheckpointURL
	checkpointCountResponse = `
	{
  		"result": {
    		"result": 74834
  		}
	}`

	unconfirmedTxsUrl      = dummyCometBFTNode + "/unconfirmed_txs"
	unconfirmedTxsResponse = `
	{
		"result": {
			"total": "",
			"txs": []
		}
	}`

	getUnconfirmedTxnCountUrl      = dummyCometBFTNode + "/num_unconfirmed_txs"
	getUnconfirmedTxnCountResponse = `
	{
		"result": {
			"total": "0",
			"txs": []
		}
	}`

	getValidatorSetUrl      = dummyHeimdallServerUrl + "/stake/validators-set"
	getValidatorSetResponse = `
	{
	"result": {
		"validators": [{
				"ID": 23,
				"startEpoch": 72797,
				"endEpoch": 0,
				"nonce": 52,
				"power": 2386,
				"pubKey": "0x04b0a83d83b01c11ec491e18d264468d5fec83b3d89a3dc274c1090c6941318884aa6fe8018db897c588651df0e6e5773a0a55e7f6147e39a57f565e6196c1a0bb",
				"signer": "0x0288a9ddca69a4784b3ecab3d8403ddfaaca8ba4",
				"last_updated": "701494100012",
				"jailed": false,
				"accum": -60050137
			},
			{
				"ID": 16,
				"startEpoch": 60315,
				"endEpoch": 0,
				"nonce": 925,
				"power": 1175,
				"pubKey": "0x046e3874eef1f03eee0a1933489f4a1e349257be057fe9f180b2e21c29aa05a69d483ca73afe63b3b61c506fede81a30e2c16d75b8d42d91a7d2ec5454041d1ede",
				"signer": "0x0651e9a1b5805fb67ac8cf82dfa4319e5be4d82c",
				"last_updated": "702097300006",
				"jailed": false,
				"accum": 46469323
			},
			{
				"ID": 19,
				"startEpoch": 65978,
				"endEpoch": 0,
				"nonce": 798,
				"power": 1529,
				"pubKey": "0x0451048d2384c1b3b5f2ba9db1c1cd813aaf10e69bb0a4c7147b164b1b7e241bfe16f17e99cdf9b3cf476164fb670f81a40b26ea06d7d59ff254b544b6ca7851ee",
				"signer": "0x12d8184f0747e33e68ab2d470dc6e870f242ea7a",
				"last_updated": "701529500051",
				"jailed": false,
				"accum": -169957608
			},
			{
				"ID": 9,
				"startEpoch": 17380,
				"endEpoch": 0,
				"nonce": 65065,
				"power": 54016735,
				"pubKey": "0x045b89dc4610f6bc13b15dc628fb8094a8e1cb23c9e72644ec41417688665f9a123047f434356cd56974acf4a637cb95c8779a36982fd28ff758baf6e0b69bbf52",
				"signer": "0x3a22c8bc68e98b0faf40f349dd2b2890fae01484",
				"last_updated": "703823500014",
				"jailed": false,
				"accum": 91342318
			},
			{
				"ID": 22,
				"startEpoch": 72568,
				"endEpoch": 0,
				"nonce": 1,
				"power": 100,
				"pubKey": "0x040053708297eb4aad4b20d7dc906b880692526c3ff3416eb845ba4024f2b2a18080e30724aab820d5e715976f826325bcdd825935aab6c3613d756944538926db",
				"signer": "0x5082f249cdb2f2c1ee035e4f423c46ea2dab3ab1",
				"last_updated": "667330300051",
				"jailed": false,
				"accum": -10683298
			},
			{
				"ID": 21,
				"startEpoch": 71794,
				"endEpoch": 0,
				"nonce": 106,
				"power": 1204,
				"pubKey": "0x041a98df71f1cddbc00530f39c6364a8fc250850e514c1f5ab6d4df47f9974842936b9805adc441d5526148613a2c3bc189a68957a633851dbf2730da29f05241f",
				"signer": "0x518d0f73e34b46b435b485283ef6255fe8436ed5",
				"last_updated": "706193800008",
				"jailed": false,
				"accum": 39839283
			},
			{
				"ID": 20,
				"startEpoch": 65980,
				"endEpoch": 0,
				"nonce": 845,
				"power": 734,
				"pubKey": "0x045ef9aabe6b3b4b9c57c319299f7c7bf483baf6f0381eb4f2031b30c2984166cd18f407faac255d8c86be734c566ec0666f9c6eaff52fc660414adece69607aec",
				"signer": "0x5a1715e478859da38e8749d4c55fef5b7a65387a",
				"last_updated": "701480800023",
				"jailed": false,
				"accum": 44109244
			},
			{
				"ID": 18,
				"startEpoch": 65872,
				"endEpoch": 0,
				"nonce": 643,
				"power": 14951,
				"pubKey": "0x049b61f7033294a17c2657fbf55ead9c0c84f42c573c90eeea4f256ae1cd4f0113e71a280458ccd3680761ca27548ccd9b36d7704fb413ce1e208e34e820721fff",
				"signer": "0x6fd70512f0e9e30e75e104f00402a49ac9eb277a",
				"last_updated": "699680700008",
				"jailed": false,
				"accum": 66266480
			},
			{
				"ID": 10,
				"startEpoch": 29689,
				"endEpoch": 0,
				"nonce": 237,
				"power": 37896,
				"pubKey": "0x041f2c0ff8f11c0584bad20b3d275a025f567deda7b8ec97600509398cceba1f3649fc8b424b4754032980770a4c495706d5191d051e6423d5b8e63cd7792aa3d5",
				"signer": "0x92da9f8f3ee16a276896fc7b2550b2151aae0332",
				"last_updated": "699239100021",
				"jailed": false,
				"accum": 50174785
			},
			{
				"ID": 2,
				"startEpoch": 0,
				"endEpoch": 0,
				"nonce": 78958,
				"power": 55387659,
				"pubKey": "0x04888a737a003f4e522ccf23bd9980fdbe7ef2b54365249deba0f9acd45279d66355b1864173b2cf9e75a1cbfb45e65a1a72b9ea76e47aa4bd50d79772ef301769",
				"signer": "0xbe188d6641e8b680743a4815dfa0f6208038960f",
				"last_updated": "696958900017",
				"jailed": false,
				"accum": 48512227
			},
			{
				"ID": 1,
				"startEpoch": 0,
				"endEpoch": 0,
				"nonce": 158281,
				"power": 56349433,
				"pubKey": "0x040bec8102c221c7cfff3e250bb6cc01c3b9a3964fb1bf4d53e91905320eef09595acb09ee0950e7374ec19488ff2523f186f6b1a9164c78dba8602e4e3c4eb013",
				"signer": "0xc26880a0af2ea0c7e8130e6ec47af756465452e8",
				"last_updated": "706221600090",
				"jailed": false,
				"accum": 65277101
			},
			{
				"ID": 3,
				"startEpoch": 0,
				"endEpoch": 0,
				"nonce": 65654,
				"power": 46071442,
				"pubKey": "0x04f3f18a027c929380417d2bd7d2a489cb662d4977e9daff335bc51f23c1c5f5f468aa19c6c8e937a745462ef2550bce42e4f38608dffb5a06e7b9d27d964cffee",
				"signer": "0xc275dc8be39f50d12f66b6a63629c39da5bae5bd",
				"last_updated": "701533000056",
				"jailed": false,
				"accum": 51535588
			},
			{
				"ID": 14,
				"startEpoch": 42535,
				"endEpoch": 0,
				"nonce": 84,
				"power": 7113,
				"pubKey": "0x046e58afa78fade1229ce3bebe3ed5435d895cfdc399323d4f20752935ff04dc514e8f3320a8d5434a13acc9209b9657ebbdf154ae715830135997f6c2ae028258",
				"signer": "0xc443279a66280fa9bb2916999c5c2d2facab0579",
				"last_updated": "705224200008",
				"jailed": false,
				"accum": -154620784
			},
			{
				"ID": 11,
				"startEpoch": 35313,
				"endEpoch": 0,
				"nonce": 169,
				"power": 1274,
				"pubKey": "0x04161cf579b40ea1a68f166da216c50e88f1323213cd22a8ffa6acabc45893a80250b5aafa6dea6e4a0289ebabe8b2996ae806098b7d88d2eee8634ec73fe2edfd",
				"signer": "0xc4acf8fbe2829cb0c209dff15a98b3dc13f12b1f",
				"last_updated": "695091100099",
				"jailed": false,
				"accum": 54747128
			},
			{
				"ID": 4,
				"startEpoch": 0,
				"endEpoch": 0,
				"nonce": 158405,
				"power": 45333182,
				"pubKey": "0x04dcd2883416e7b8663caafbfc885e757b0ea809657df8d6f322f01a0c5a11fd033bf13d3e0d5e88feff92ba415d32d626e3f7d9dd7b5ec7c2fef8ded83d660ac2",
				"signer": "0xf903ba9e006193c1527bfbe65fe2123704ea3f99",
				"last_updated": "706173600012",
				"jailed": false,
				"accum": -162961648
			}
		],
		"proposer": {
			"ID": 4,
			"startEpoch": 0,
			"endEpoch": 0,
			"nonce": 158405,
			"power": 45333182,
			"pubKey": "0x04dcd2883416e7b8663caafbfc885e757b0ea809657df8d6f322f01a0c5a11fd033bf13d3e0d5e88feff92ba415d32d626e3f7d9dd7b5ec7c2fef8ded83d660ac2",
			"signer": "0xf903ba9e006193c1527bfbe65fe2123704ea3f99",
			"last_updated": "706173600012",
			"jailed": false,
			"accum": -162961648
		}
	}
}`
)
