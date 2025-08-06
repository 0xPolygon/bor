package bor

import (
	"context"

	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/checkpoint"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/milestone"

	"github.com/0xPolygon/heimdall-v2/x/bor/types"
)

//go:generate mockgen -source=IHeimdallClient.go -destination=../../tests/bor/mocks/MockIHeimdallClient.go -package=mocks
type IHeimdallClient interface {
	StateSyncEventsWithTime(ctx context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error)
	StateSyncEventById(ctx context.Context, ID uint64) (*clerk.EventRecordWithTime, error)
	StateSyncEventsList(ctx context.Context, fromId uint64) ([]*clerk.EventRecordWithTime, error)
	StateFetchLimit() uint64
	GetSpan(ctx context.Context, spanID uint64) (*types.Span, error)
	GetLatestSpan(ctx context.Context) (*types.Span, error)
	FetchCheckpoint(ctx context.Context, number int64) (*checkpoint.Checkpoint, error)
	FetchCheckpointCount(ctx context.Context) (int64, error)
	FetchMilestone(ctx context.Context) (*milestone.Milestone, error)
	FetchMilestoneCount(ctx context.Context) (int64, error)
	Close()
}
