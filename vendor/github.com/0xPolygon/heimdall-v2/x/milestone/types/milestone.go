package types

import "sort"

// SortMilestones sorts the array of milestones on the basis for timestamps
func SortMilestones(milestones []Milestone) []Milestone {
	sort.Slice(milestones, func(i, j int) bool {
		return milestones[i].Timestamp < milestones[j].Timestamp
	})
	return milestones
}
