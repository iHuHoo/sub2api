package service

import (
	"context"
	"errors"
	"time"
)

var (
	ErrSupplierUsageDayClosed     = errors.New("supplier usage day is closed")
	ErrSupplierUsageDayOutOfOrder = errors.New("supplier usage day is out of order")
)

type SupplierUsageLateDay struct {
	Day                          time.Time
	CostDifference               string
	RequestDifference            int64
	InputTokenDifference         int64
	OutputTokenDifference        int64
	CacheCreationTokenDifference int64
	CacheReadTokenDifference     int64
}

type SupplierUsageSnapshotRepository interface {
	RefreshDay(context.Context, time.Time, *time.Time) error
	CloseDay(context.Context, time.Time) error
	FirstOpenDay(context.Context) (time.Time, bool, error)
	EarliestUsageDay(context.Context) (time.Time, bool, error)
	// FindLateUsage returns positive per-account increases summed by day in [from, to).
	FindLateUsage(context.Context, time.Time, time.Time) ([]SupplierUsageLateDay, error)
}
