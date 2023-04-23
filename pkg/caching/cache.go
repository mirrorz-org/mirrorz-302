package caching

import (
	"sync"
	"time"

	"github.com/mirrorz-org/mirrorz-302/pkg/logging"
)

var cacheGCLogger = logging.GetLogger("gc")

type Status int

const (
	StatusNone Status = iota
	StatusFresh
	StatusStale
	StatusExpired
)

// IP, label to start, last timestamp, url
type Resolved struct {
	start time.Time // time of last write
	last  time.Time // time of last read

	Url     string
	Resolve string // only used in resolveExist
}

type ResolveCache struct {
	sync.Map // map[string]Resolved

	ttl time.Duration
}

func NewResolveCache(ttl time.Duration) *ResolveCache {
	return &ResolveCache{ttl: ttl}
}

func (c *ResolveCache) Load(key string) (Resolved, Status) {
	cur := time.Now()
	v, ok := c.Map.Load(key)
	if !ok {
		return Resolved{}, StatusNone
	}
	r := v.(Resolved)
	if cur.Sub(r.last) >= c.ttl {
		return r, StatusExpired
	}
	if cur.Sub(r.start) >= c.ttl {
		return r, StatusStale
	}
	return r, StatusFresh
}

func (c *ResolveCache) Store(key string, value Resolved) {
	cur := time.Now()
	if value.start.IsZero() {
		value.start = cur
	}
	value.last = cur
	c.Map.Store(key, value)
}

func (c *ResolveCache) Delete(key string) {
	c.Map.Delete(key)
}

func (c *ResolveCache) Touch(key string) {
	r, status := c.Load(key)
	if status != StatusFresh && status != StatusStale {
		return
	}
	c.Store(key, r)
}

func (c *ResolveCache) GC(cur time.Time) {
	cacheGCLogger.Infof("Resolved GC start at %s\n", cur)
	c.Map.Range(func(k, v any) bool {
		r, ok := v.(Resolved)
		if !ok {
			c.Map.Delete(k)
			return true
		}
		if cur.Sub(r.start) >= c.ttl && cur.Sub(r.last) >= c.ttl {
			c.Map.Delete(k)
			cacheGCLogger.Infof("Resolved GC %s: %s\n", k, r.Url)
		}
		return true
	})
	cacheGCLogger.Infof("Resolved GC done at %s\n\n", time.Now())
}

func (c *ResolveCache) GCTicker(ch <-chan time.Time) {
	for t := range ch {
		c.GC(t)
	}
}

func (c *ResolveCache) StartGCTicker() {
	ticker := time.NewTicker(time.Second * time.Duration(c.ttl))
	go c.GCTicker(ticker.C)
}

func (c *ResolveCache) Clear() {
	c.Map.Range(func(k, v any) bool {
		c.Map.Delete(k)
		return true
	})
}
