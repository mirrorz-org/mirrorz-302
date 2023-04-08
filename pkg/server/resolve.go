package server

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/mirrorz-org/mirrorz-302/pkg/caching"
	"github.com/mirrorz-org/mirrorz-302/pkg/influxdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/mirrorz-org/mirrorz-302/pkg/scoring"
	"github.com/mirrorz-org/mirrorz-302/pkg/tracing"
)

func (s *Server) queryInflux(ctx context.Context, cname string) (res influxdb.Result, ok bool) {
	res, err := s.influx.Query(ctx, cname)
	if res == nil {
		s.errorLogger.Errorf("Resolve query failed: %v\n", err)
		return res, false
	} else if err != nil {
		s.errorLogger.Warningf("Resolve query error: %v\n", err)
		// result available, continuing anyway
	}
	return res, true
}

func (s *Server) Resolve(ctx context.Context, meta requestmeta.RequestMeta) (url string, err error) {
	tracer := ctx.Value(tracing.Key).(tracing.Tracer)

	cname := meta.CName
	tracer.Printf("Labels: %v\n", meta.Labels)
	tracer.Printf("IP: %s\n", meta.IP)
	tracer.Printf("Scheme: %s\n", meta.Scheme)

	logFunc := func(url string, score scoring.Score, char string) {
		if url != "" {
			// record detail in resolve log
			s.resolveLogger.Debugf("%s", tracer.String())
			resolvedLog := fmt.Sprintf("%s: %s %s %s",
				char, url, meta,
				score)
			s.resolveLogger.Infof("%s\n", resolvedLog)
			tracer.Printf("%s\n", resolvedLog)
		} else {
			// record detail in fail log
			s.failLogger.Debugf("%s", tracer.String())
			failLog := fmt.Sprintf("F: %s", meta)
			s.failLogger.Infof("%s\n", failLog)
			tracer.Printf("%s\n", failLog)
		}
	}

	// check if already resolved / cached
	key := requestmeta.CacheKey(meta)
	keyResolved, cacheHit := s.resolved.Load(key)

	// all valid, use cached result
	if cacheHit && s.resolved.IsFresh(keyResolved) {
		// update timestamp
		s.resolved.Store(key, keyResolved)
		url = keyResolved.Url
		logFunc(url, scoring.Score{}, "C") // C for cache
		return
	}

	res, ok := s.queryInflux(ctx, cname)
	if !ok {
		return
	}

	var resolve, repo string

	if cacheHit && s.resolved.IsStale(keyResolved) {
		resolve, repo = s.ResolveExist(ctx, res, keyResolved.Resolve)
	}

	var chosenScore scoring.Score
	if resolve == "" && repo == "" {
		// ResolveExist failed
		scores := s.resolveBest(ctx, res, meta)
		if len(scores) > 0 {
			chosenScore = scores[0]
			resolve = chosenScore.Resolve
			repo = chosenScore.Repo
		}
	}

	if resolve == "" && repo == "" {
		url = ""
	} else if strings.HasPrefix(repo, "http://") || strings.HasPrefix(repo, "https://") {
		url = repo
	} else {
		url = fmt.Sprintf("%s://%s%s", meta.Scheme, resolve, repo)
	}
	s.resolved.Store(key, caching.Resolved{
		Url:     url,
		Resolve: resolve,
	})
	logFunc(url, chosenScore, "R") // R for resolve
	return
}

func calcDeltaCutoff(res influxdb.Result) int {
	var sum, squareSum, n int
	for _, item := range res {
		if item.Value >= 0 {
			continue
		}
		sum += item.Value
		squareSum += item.Value * item.Value
		n++
	}
	if n == 0 {
		return 0
	}
	mean := float64(sum) / float64(n)
	stdev := math.Sqrt(float64(squareSum)/float64(n) - mean*mean)
	return int(math.Round(mean - 2*stdev))
}

// ResolveBest tries to find the best mirror for the given request
func (s *Server) ResolveBest(ctx context.Context, meta requestmeta.RequestMeta) (scores scoring.Scores) {
	res, ok := s.queryInflux(ctx, meta.CName)
	if !ok {
		return
	}
	return s.resolveBest(ctx, res, meta)
}

func (s *Server) resolveBest(ctx context.Context, res influxdb.Result, meta requestmeta.RequestMeta) (scores scoring.Scores) {
	tracer := ctx.Value(tracing.Key).(tracing.Tracer)
	deltaCutoff := calcDeltaCutoff(res)

	for _, item := range res {
		abbr := item.Mirror
		tracer.Printf("abbr: %s\n", abbr)
		endpoints, ok := s.mirrorzd.Lookup(abbr)
		if !ok {
			continue
		}
		var scoresEndpoints scoring.Scores
		for _, endpoint := range endpoints {
			tracer.Printf("  endpoint: %s %s\n", endpoint.Resolve, endpoint.Label)
			if reason, ok := endpoint.Match(meta); !ok {
				tracer.Printf("    %s\n", reason)
				continue
			}
			score := scoring.Eval(endpoint, meta)
			score.Abbr = abbr
			score.Delta = item.Value
			score.Repo = item.Path
			tracer.Printf("    score: %s\n", score)

			if score.Delta < deltaCutoff {
				tracer.Printf("    outdated\n")
				continue
			}
			if !endpoint.Public && score.Mask == 0 && score.ISP == 0 {
				tracer.Printf("    private endpoint\n")
				continue
			}
			scoresEndpoints = append(scoresEndpoints, score)
		}

		if len(scoresEndpoints) == 0 {
			tracer.Printf("  no score available\n")
			continue
		}

		for i, score := range scoresEndpoints {
			tracer.Printf("  score %d: %s\n", i, score)
			scores = append(scores, score)
		}
	}
	if len(scores) == 0 {
		tracer.Printf("no score available\n")
		return
	}

	scores.Sort()
	for i, score := range scores {
		tracer.Printf("score %d: %s\n", i, score)
	}
	return
}

// ResolveExist refreshes a stale cached result
func (s *Server) ResolveExist(ctx context.Context, res influxdb.Result, oldResolve string) (resolve string, repo string) {
	tracer := ctx.Value(tracing.Key).(tracing.Tracer)

outerLoop:
	for _, item := range res {
		abbr := item.Mirror
		tracer.Printf("abbr: %s\n", abbr)
		endpoints, ok := s.mirrorzd.Lookup(abbr)
		if !ok {
			continue
		}
		for _, endpoint := range endpoints {
			tracer.Printf("  endpoint: %s %s\n", endpoint.Resolve, endpoint.Label)

			if oldResolve == endpoint.Resolve {
				resolve = endpoint.Resolve
				repo = item.Path
				tracer.Printf("exist\n")
				break outerLoop
			}
		}
	}
	return
}

func (s *Server) CachePurge() {
	s.resolved.Clear()
}

func (s *Server) StartResolvedTicker() {
	s.resolved.StartGCTicker()
}
