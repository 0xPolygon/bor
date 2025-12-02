package consensus

type ErrInvalidVote struct {
	Reason string
}

func (e ErrInvalidVote) Error() string {
	return "invalid vote: " + e.Reason
}
