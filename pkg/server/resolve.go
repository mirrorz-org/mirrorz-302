package server

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/mirrorz-org/mirrorz-302/pkg/caching"
	"github.com/mirrorz-org/mirrorz-302/pkg/influxdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/mirrorz-org/mirrorz-302/pkg/scoring"
	"github.com/mirrorz-org/mirrorz-302/pkg/tracing"
)

func (s *Server) Resolve(ctx context.Context, meta requestmeta.RequestMeta) (url string, err error) {
	tracer := ctx.Value(tracing.Key).(tracing.Tracer)
	traceFunc := tracer.Printf

	cname := meta.CName
	traceFunc("Labels: %v\n", meta.Labels)
	traceFunc("IP: %s\n", meta.IP)
	traceFunc("Scheme: %s\n", meta.Scheme)

	logFunc := func(url string, score scoring.Score, char string) {
		if url != "" {
			// record detail in resolve log
			s.resolveLogger.Debugf("%s", tracer.String())
			resolvedLog := fmt.Sprintf("%s: %s %s %s",
				char, url, meta,
				score)
			s.resolveLogger.Infof("%s\n", resolvedLog)
			traceFunc("%s\n", resolvedLog)
		} else {
			// record detail in fail log
			s.failLogger.Debugf("%s", tracer.String())
			failLog := fmt.Sprintf("F: %s", meta)
			s.failLogger.Infof("%s\n", failLog)
			traceFunc("%s\n", failLog)
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

	res, err := s.influx.Query(ctx, cname)
	if res == nil {
		s.errorLogger.Errorf("Resolve query failed: %v\n", err)
		return
	} else if err != nil {
		s.errorLogger.Warningf("Resolve query error: %v\n", err)
		// result available, continuing anyway
	}

	var resolve, repo string

	if cacheHit && s.resolved.IsStale(keyResolved) {
		resolve, repo = s.ResolveExist(ctx, res, keyResolved.Resolve)
	}

	var chosenScore scoring.Score
	if resolve == "" && repo == "" {
		// ResolveExist failed
		chosenScore = s.ResolveBest(ctx, res, meta)
		resolve = chosenScore.Resolve
		repo = chosenScore.Repo
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
	mean := float64(sum) / float64(n)
	variance := float64(squareSum)/float64(n) - mean*mean
	return int(math.Sqrt(mean - 2*variance))
}

// ResolveBest tries to find the best mirror for the given request
func (s *Server) ResolveBest(ctx context.Context, res influxdb.Result, meta requestmeta.RequestMeta) (chosenScore scoring.Score) {
	tracer := ctx.Value(tracing.Key).(tracing.Tracer)
	traceFunc := tracer.Printf

	var scores scoring.Scores
	deltaCutoff := calcDeltaCutoff(res)

	for _, item := range res {
		abbr := item.Mirror
		traceFunc("abbr: %s\n", abbr)
		endpoints, ok := s.mirrorzd.Lookup(abbr)
		if !ok {
			continue
		}
		var scoresEndpoints scoring.Scores
		for _, endpoint := range endpoints {
			traceFunc("  endpoint: %s %s\n", endpoint.Resolve, endpoint.Label)
			if reason, ok := endpoint.Match(meta); !ok {
				traceFunc("    %s\n", reason)
				continue
			}
			score := scoring.Eval(endpoint, meta)
			score.Delta = item.Value
			score.Repo = item.Path
			traceFunc("    score: %s\n", score)

			if score.Delta < deltaCutoff {
				traceFunc("    not up-to-date enough\n")
				continue
			}
			if !endpoint.Public && score.Mask == 0 && score.ISP == 0 {
				traceFunc("    not hit private\n")
				continue
			}
			scoresEndpoints = append(scoresEndpoints, score)
		}

		if len(scoresEndpoints) == 0 {
			traceFunc("  no score found\n")
			continue
		}

		// Find the not-dominated scores, or the first one
		optimalScores := scoresEndpoints.OptimalsExceptDelta() // Delta all the same
		if len(optimalScores) > 0 && len(optimalScores) != len(scoresEndpoints) {
			for index, score := range optimalScores {
				traceFunc("  optimal scores: %d %s\n", index, score)
				scores = append(scores, score)
			}
		} else {
			traceFunc("  first score: %s\n", scoresEndpoints[0])
			scores = append(scores, scoresEndpoints[0])
		}
	}
	if len(scores) == 0 {
		return
	}

	for index, score := range scores {
		traceFunc("scores: %d %s\n", index, score)
	}
	optimalScores := scores.Optimals()
	if len(optimalScores) == 0 {
		s.errorLogger.Warningf("Resolve optimal scores empty, algorithm implemented wrong\n")
		chosenScore = scores[0]
		return
	}

	allDelta := scores.AllDelta()
	allEqualExceptDelta := optimalScores.AllEqualExceptDelta()
	if allEqualExceptDelta || allDelta {
		var candidateScores scoring.Scores
		if allDelta {
			// Note: allDelta == true implies allEqualExceptDelta == true
			candidateScores = scores
		} else {
			candidateScores = optimalScores
		}
		// randomly choose one mirror from the optimal half
		// when len(optimalScores) == 1, randomHalf always succeeds
		sort.Sort(candidateScores)
		chosenScore = candidateScores.RandomHalf()
		for index, score := range candidateScores {
			traceFunc("sorted delta scores: %d %s\n", index, score)
		}
	} else {
		sort.Sort(optimalScores)
		chosenScore = optimalScores[0]
		// randomly choose one mirror not dominated by others
		//chosenScore = optimalScores.Random()
		for index, score := range optimalScores {
			traceFunc("optimal scores: %d %s\n", index, score)
		}
	}
	return
}

// ResolveExist refreshes a stale cached result
func (s *Server) ResolveExist(ctx context.Context, res influxdb.Result, oldResolve string) (resolve string, repo string) {
	tracer := ctx.Value(tracing.Key).(tracing.Tracer)
	traceFunc := tracer.Printf

outerLoop:
	for _, item := range res {
		abbr := item.Mirror
		traceFunc("abbr: %s\n", abbr)
		endpoints, ok := s.mirrorzd.Lookup(abbr)
		if !ok {
			continue
		}
		for _, endpoint := range endpoints {
			traceFunc("  endpoint: %s %s\n", endpoint.Resolve, endpoint.Label)

			if oldResolve == endpoint.Resolve {
				resolve = endpoint.Resolve
				repo = item.Path
				traceFunc("exist\n")
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
