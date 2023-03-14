package main

import (
	"sync"
	"time"

	"github.com/mirrorz-org/mirrorz-302/pkg/logging"
)

// IP, label to start, last timestamp, url
type Resolved struct {
	start   int64 // starting timestamp, namely still check db after some time
	last    int64 // last update timestamp
	url     string
	resolve string // only used in resolveExist
}

type ResolveCache struct {
	sync.Map // map[string]Resolved

	ttl int64
}

func NewResolveCache(ttl int64) *ResolveCache {
	return &ResolveCache{ttl: ttl}
}

func (c *ResolveCache) Load(key string) (Resolved, bool) {
	v, ok := c.Map.Load(key)
	if !ok {
		return Resolved{}, false
	}
	return v.(Resolved), true
}

func (c *ResolveCache) Store(key string, value Resolved) {
	c.Map.Store(key, value)
}

func (c *ResolveCache) GC(t time.Time, logger *logging.Logger) {
	cur := t.Unix()
	logger.Infof("Resolved GC start at %s\n", t)
	c.Map.Range(func(k interface{}, v interface{}) bool {
		r, ok := v.(Resolved)
		if !ok {
			c.Map.Delete(k)
			return true
		}
		if cur-r.start >= c.ttl && cur-r.last >= c.ttl {
			c.Map.Delete(k)
			logger.Infof("Resolved GC %s: %s\n", k, r.url)
		}
		return true
	})
	logger.Infof("Resolved GC done at %s\n\n", time.Now())
}

func (c *ResolveCache) Clear() {
	c.Map.Range(func(k interface{}, v interface{}) bool {
		c.Map.Delete(k)
		return true
	})
}
