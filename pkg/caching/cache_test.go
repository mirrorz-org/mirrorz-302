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
