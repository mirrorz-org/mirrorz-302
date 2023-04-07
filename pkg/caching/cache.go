package caching

import (
	"sync"
	"time"

	"github.com/mirrorz-org/mirrorz-302/pkg/logging"
)

var cacheGCLogger = logging.GetLogger("gc")

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

func NewResolveCache(ttlSeconds int) *ResolveCache {
	return &ResolveCache{ttl: time.Duration(ttlSeconds) * time.Second}
}

func (c *ResolveCache) Load(key string) (Resolved, bool) {
	v, ok := c.Map.Load(key)
	if !ok {
		return Resolved{}, false
	}
	return v.(Resolved), true
}

func (c *ResolveCache) IsFresh(v Resolved) bool {
	cur := time.Now()
	return cur.Sub(v.last) < c.ttl && cur.Sub(v.start) < c.ttl
}

func (c *ResolveCache) IsStale(v Resolved) bool {
	cur := time.Now()
	return cur.Sub(v.last) < c.ttl && cur.Sub(v.start) >= c.ttl
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
	r, ok := c.Load(key)
	if !ok {
		return
	}
	c.Store(key, r)
}

func (c *ResolveCache) GC(cur time.Time) {
	cacheGCLogger.Infof("Resolved GC start at %s\n", cur)
	c.Map.Range(func(k interface{}, v interface{}) bool {
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
	c.Map.Range(func(k interface{}, v interface{}) bool {
		c.Map.Delete(k)
		return true
	})
}
