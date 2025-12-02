package types

import cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

// IsVoteTypeValid returns true if t is a valid vote type.
func IsVoteTypeValid(t cmtproto.SignedMsgType) bool {
	switch t {
	case cmtproto.PrevoteType, cmtproto.PrecommitType:
		return true
	default:
		return false
	}
}

var signedMsgTypeToShortName = map[cmtproto.SignedMsgType]string{
	cmtproto.UnknownType:   "unknown",
	cmtproto.PrevoteType:   "prevote",
	cmtproto.PrecommitType: "precommit",
	cmtproto.ProposalType:  "proposal",
}

// Returns a short lowercase descriptor for a signed message type.
func SignedMsgTypeToShortString(t cmtproto.SignedMsgType) string {
	if shortName, ok := signedMsgTypeToShortName[t]; ok {
		return shortName
	}
	return "unknown"
}
