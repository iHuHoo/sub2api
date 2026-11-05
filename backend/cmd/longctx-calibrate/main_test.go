//go:build calibration

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

type fpStub struct {
	service.IdentityCache
	err error
}

func (s fpStub) GetFingerprint(context.Context, int64) (*service.Fingerprint, error) {
	return nil, s.err
}

func TestMissAsNil(t *testing.T) {
	if fp, err := (missAsNil{fpStub{err: redis.Nil}}).GetFingerprint(context.Background(), 71); fp != nil || err != nil {
		t.Fatalf("redis.Nil must be a miss, got %v %v", fp, err)
	}
	boom := errors.New("read failed")
	if _, err := (missAsNil{fpStub{err: boom}}).GetFingerprint(context.Background(), 71); !errors.Is(err, boom) {
		t.Fatalf("other errors must stay errors, got %v", err)
	}
}
