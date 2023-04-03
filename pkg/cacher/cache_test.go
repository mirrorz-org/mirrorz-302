package cacher

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestResolveCache(t *testing.T) {
	as := assert.New(t)
	c := NewResolveCache(10)

	now := time.Now()
	r := Resolved{start: now, last: now}
	as.True(c.IsFresh(r))
	as.False(c.IsStale(r))

	r.start = r.start.Add(-15 * time.Second)
	as.False(c.IsFresh(r))
	as.True(c.IsStale(r))

	r.last = r.last.Add(-15 * time.Second)
	as.False(c.IsFresh(r))
	as.False(c.IsStale(r))
}
