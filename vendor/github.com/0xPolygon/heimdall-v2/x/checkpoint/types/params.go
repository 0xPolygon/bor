package types

import "fmt"

// ValidateBasic checks that the checkpoint parameters have valid values
func (p Params) ValidateBasic() error {
	if p.MaxCheckpointLength == 0 {
		return fmt.Errorf("max checkpoint length should be non-zero")
	}

	if p.AvgCheckpointLength == 0 {
		return fmt.Errorf("value of avg checkpoint length should be non-zero")
	}

	if p.MaxCheckpointLength < p.AvgCheckpointLength {
		return fmt.Errorf("avg checkpoint length should not be greater than max checkpoint length")
	}

	if p.ChildChainBlockInterval == 0 {
		return fmt.Errorf("child chain block interval should be greater than zero")
	}

	return nil
}
