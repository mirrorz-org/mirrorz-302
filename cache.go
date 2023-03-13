package main

import (
	"sync"
	"time"
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

	ttl    int64
	logger *Logger
}

func NewResolveCache(ttl int64, logger *Logger) *ResolveCache {
	return &ResolveCache{ttl: ttl, logger: logger}
}

func (c *ResolveCache) Load(key string) (Resolved, bool) {
	v, ok := c.Map.Load(key)
	if !ok {
		return Resolved{}, false
	}
	return v.(Resolved), true
}

func (c *ResolveCache) Store(key string, v Resolved) {
	c.Map.Store(key, v)
}

func (c *ResolveCache) GC(t time.Time) {
	cur := t.Unix()
	c.Map.Range(func(k interface{}, v interface{}) bool {
		r, ok := v.(Resolved)
		if !ok {
			c.Map.Delete(k)
			return true
		}
		if cur-r.start >= config.CacheTime &&
			cur-r.last >= config.CacheTime {
			c.Map.Delete(k)
			c.logger.Infof("Resolved GC %s %s\n", k, r.url)
		}
		return true
	})
}

func (s *MirrorZ302Server) ResolvedInit() {
	s.resolved.Map = sync.Map{}
}
