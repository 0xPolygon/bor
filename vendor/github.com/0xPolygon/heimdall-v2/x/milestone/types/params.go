package types

import (
	"fmt"
)

// ValidateBasic checks that the milestone proposition's parameters have valid values.
func (p Params) ValidateBasic() error {
	if p.MaxMilestonePropositionLength == 0 {
		return fmt.Errorf("max milestone proposition length should not be zero")
	}
	if p.FfMilestoneThreshold == 0 {
		return fmt.Errorf("ff milestone threshold should not be zero")
	}
	if p.FfMilestoneBlockInterval == 0 {
		return fmt.Errorf("ff milestone block interval should not be zero")
	}
	if p.FfMilestoneBlockInterval >= p.FfMilestoneThreshold {
		return fmt.Errorf("ff milestone block interval should be less than ff milestone threshold")
	}
	if p.FfMilestoneThreshold%p.FfMilestoneBlockInterval != 0 {
		return fmt.Errorf("ff milestone threshold should be divisible by ff milestone block interval")
	}
	return nil
}
