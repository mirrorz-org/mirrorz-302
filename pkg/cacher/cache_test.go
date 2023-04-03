package cacher

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestResolveCache(t *testing.T) {
	as := assert.New(t)
	c := NewResolveCache(10000000000)

	now := time.Now().Unix()
	r := Resolved{start: now, last: now}
	as.True(c.IsFresh(r))
	as.False(c.IsStale(r))

	r.start -= 15000000000
	as.False(c.IsFresh(r))
	as.True(c.IsStale(r))

	r.last -= 15000000000
	as.False(c.IsFresh(r))
	as.False(c.IsStale(r))
}
