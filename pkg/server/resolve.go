package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/mirrorz-org/mirrorz-302/pkg/cacher"
	"github.com/mirrorz-org/mirrorz-302/pkg/influxdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/mirrorz-org/mirrorz-302/pkg/scoring"
	"github.com/mirrorz-org/mirrorz-302/pkg/trace"
)

func (s *Server) Resolve(r *http.Request, cname string) (url string, err error) {
	ctx := r.Context()
	tracer := ctx.Value(trace.Key).(trace.Tracer)
	traceFunc := tracer.Printf

	meta := s.meta.Parse(r)
	traceFunc("Labels: %v\n", meta.Labels)
	traceFunc("IP: %v\n", meta.IP)
	traceFunc("Scheme: %s\n", meta.Scheme)

	logFunc := func(url string, score scoring.Score, char string) {
		if url != "" {
			// record detail in resolve log
			s.resolveLogger.Debugf("%s", tracer.String())
			resolvedLog := fmt.Sprintf("%s: %s %s %s",
				char, url, meta,
				score.LogString())
			s.resolveLogger.Infof("%s\n", resolvedLog)
			traceFunc("%s\n", resolvedLog)
		} else {
			// record detail in fail log
			s.failLogger.Debugf("%s", tracer.String())
			failLog := fmt.Sprintf("F: %s %s", cname, meta)
			s.failLogger.Infof("%s\n", failLog)
			traceFunc("%s\n", failLog)
		}
	}

	// check if already resolved / cached
	key := requestmeta.CacheKey(meta, cname)
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
	if err != nil {
		s.errorLogger.Errorf("Resolve query: %v\n", err)
		return
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
	s.resolved.Store(key, cacher.Resolved{
		Url:     url,
		Resolve: resolve,
	})
	logFunc(url, chosenScore, "R") // R for resolve
	return
}

// ResolveBest tries to find the best mirror for the given request
func (s *Server) ResolveBest(ctx context.Context, res influxdb.Result, meta requestmeta.RequestMeta) (chosenScore scoring.Score) {
	tracer := ctx.Value(trace.Key).(trace.Tracer)
	traceFunc := tracer.Printf

	var scores scoring.Scores

	for res.Next() {
		record := res.Record()
		abbr := record.ValueByKey("mirror").(string)
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
			score.Delta = int(record.Value().(int64))
			score.Repo = record.ValueByKey("path").(string)
			traceFunc("    score: %v\n", score)

			//if score.Delta < -60*60*24*3 { // 3 days
			//    traceFunc("    not up-to-date enough\n")
			//    continue
			//}
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
				traceFunc("  optimal scores: %d %v\n", index, score)
				scores = append(scores, score)
			}
		} else {
			traceFunc("  first score: %v\n", scoresEndpoints[0])
			scores = append(scores, scoresEndpoints[0])
		}
	}
	if err := res.Err(); err != nil {
		s.errorLogger.Errorf("Resolve query parsing error: %v\n", err)
		return
	}
	if len(scores) == 0 {
		return
	}

	for index, score := range scores {
		traceFunc("scores: %d %v\n", index, score)
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
			traceFunc("sorted delta scores: %d %v\n", index, score)
		}
	} else {
		sort.Sort(optimalScores)
		chosenScore = optimalScores[0]
		// randomly choose one mirror not dominated by others
		//chosenScore = optimalScores.Random()
		for index, score := range optimalScores {
			traceFunc("optimal scores: %d %v\n", index, score)
		}
	}
	return
}

// ResolveExist refreshes a stale cached result
func (s *Server) ResolveExist(ctx context.Context, res influxdb.Result, oldResolve string) (resolve string, repo string) {
	tracer := ctx.Value(trace.Key).(trace.Tracer)
	traceFunc := tracer.Printf

outerLoop:
	for res.Next() {
		record := res.Record()
		abbr := record.ValueByKey("mirror").(string)
		traceFunc("abbr: %s\n", abbr)
		endpoints, ok := s.mirrorzd.Lookup(abbr)
		if !ok {
			continue
		}
		for _, endpoint := range endpoints {
			traceFunc("  endpoint: %s %s\n", endpoint.Resolve, endpoint.Label)

			if oldResolve == endpoint.Resolve {
				resolve = endpoint.Resolve
				repo = record.ValueByKey("path").(string)
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
