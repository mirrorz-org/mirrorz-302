package caching

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestResolveCache(t *testing.T) {
	as := assert.New(t)
	c := NewResolveCache(10 * time.Second)

	now := time.Now()
	r := Resolved{start: now, last: now}
	c.Store("a", r)
	r2, status := c.Load("a")
	as.Equal(r.start, r2.start)
	as.Equal(StatusFresh, status)

	r.start = now.Add(-11 * time.Second)
	c.Store("a", r)
	r2, status = c.Load("a")
	as.Equal(r.start, r2.start)
	as.Equal(StatusStale, status)
}

func TestStartGCTickerLongTTL(t *testing.T) {
	// 600s used to overflow the ticker interval computation and panic.
	c := NewResolveCache(600 * time.Second)
	c.StartGCTicker()
	c.StopGCTicker()
}

func TestGCTickerRemovesExpiredEntries(t *testing.T) {
	as := assert.New(t)
	c := NewResolveCache(10 * time.Millisecond)
	c.StartGCTicker()
	defer c.StopGCTicker()

	c.Store("a", Resolved{})
	as.Eventually(func() bool {
		_, status := c.Load("a")
		return status == StatusNone
	}, time.Second, 10*time.Millisecond)
}
