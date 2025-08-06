package bor

import (
	"context"

	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/milestone"
)

//go:generate mockgen -source=IHeimdallWSClient.go -destination=../../tests/bor/mocks/MockIHeimdallWSClient.go -package=mocks . IHeimdallWSClient
type IHeimdallWSClient interface {
	SubscribeMilestoneEvents(ctx context.Context) <-chan *milestone.Milestone
	Unsubscribe(ctx context.Context) error
	Close() error
}
