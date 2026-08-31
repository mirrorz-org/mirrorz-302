package server

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mirrorz-org/mirrorz-302/pkg/caching"
	"github.com/mirrorz-org/mirrorz-302/pkg/influxdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/mirrorz-org/mirrorz-302/pkg/scoring"
	"github.com/mirrorz-org/mirrorz-302/pkg/tracing"
)

const mirrorOfflineThreshold = 5 * time.Minute

// excludeOfflineMirrors sorts monitor results from newest to oldest and drops
// mirrors whose latest data trails the newest result by five minutes or more.
// Using a relative watermark means a monitor-wide outage does not exclude every
// mirror merely because all collected data is old.
func excludeOfflineMirrors(res influxdb.Result) influxdb.Result {
	sort.SliceStable(res, func(i, j int) bool {
		return res[i].Time.After(res[j].Time)
	})
	if len(res) == 0 {
		return res
	}

	newest := res[0].Time
	for i, item := range res {
		if newest.Sub(item.Time) >= mirrorOfflineThreshold {
			return res[:i]
		}
	}
	return res
}

func (s *Server) queryInflux(ctx context.Context, cname string) (res influxdb.Result, ok bool) {
	res, err := s.influx.Query(ctx, cname)
	if res == nil {
		s.errorLogger.Errorf("Resolve query failed: %v\n", err)
		return res, false
	} else if err != nil {
		s.errorLogger.Warningf("Resolve query error: %v\n", err)
		// result available, continuing anyway
	}
	return excludeOfflineMirrors(res), true
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
	keyResolved, cacheStatus := s.resolved.Load(key)

	// all valid, use cached result
	if cacheStatus == caching.StatusFresh && !tracer.Enabled() {
		// update timestamp
		s.resolved.Store(key, keyResolved)
		url = keyResolved.Url
		logFunc(url, scoring.Score{}, "C") // C for cache
		return
	}

	res, ok := s.queryInflux(ctx, cname)
	if !ok {
		return "", fmt.Errorf("queryInflux failed")
	}

	var resolve, repo string

	if cacheStatus == caching.StatusStale {
		resolve, repo = s.ResolveExist(ctx, res, keyResolved.Resolve, meta)
	}

	var chosenScore scoring.Score
	if resolve == "" && repo == "" {
		// ResolveExist failed
		scores := s.resolveBest(ctx, res, meta, 0)
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

func repositoryURL(score scoring.Score, scheme string) string {
	if strings.HasPrefix(score.Repo, "http://") || strings.HasPrefix(score.Repo, "https://") {
		return score.Repo
	}
	return fmt.Sprintf("%s://%s%s", scheme, score.Resolve, score.Repo)
}

func candidateURL(score scoring.Score, scheme string) string {
	url := repositoryURL(score, scheme)
	if !strings.HasSuffix(url, "/") {
		url += "/"
	}
	return url
}

func candidateURLs(scores scoring.Scores, scheme string) []string {
	if len(scores) == 0 {
		return []string{}
	}

	urls := make([]string, 0, len(scores))
	seen := make(map[string]struct{}, len(scores))
	for _, score := range scores {
		url := candidateURL(score, scheme)
		if _, ok := seen[url]; ok {
			continue
		}
		seen[url] = struct{}{}
		urls = append(urls, url)
	}
	return urls
}

// resolveCandidates returns all eligible repository roots in scoring order.
// A cached stale list is retained as a fallback if the monitor database is
// temporarily unavailable.
func (s *Server) resolveCandidates(ctx context.Context, meta requestmeta.RequestMeta) ([]string, error) {
	key := requestmeta.CacheKey(meta)
	cached, cacheStatus := s.resolved.Load(key)
	if cacheStatus == caching.StatusFresh && cached.Candidates != nil {
		s.resolved.Touch(key)
		return cached.Candidates, nil
	}

	res, ok := s.queryInflux(ctx, meta.CName)
	if !ok {
		if len(cached.Candidates) > 0 {
			s.resolved.Touch(key)
			return cached.Candidates, nil
		}
		return nil, fmt.Errorf("queryInflux failed")
	}

	scores := s.resolveBest(ctx, res, meta, 0)
	urls := candidateURLs(scores, meta.Scheme)
	resolved := caching.Resolved{Candidates: urls}
	if len(scores) > 0 {
		resolved.Url = repositoryURL(scores[0], meta.Scheme)
		resolved.Resolve = scores[0].Resolve
	}
	s.resolved.Store(key, resolved)
	return urls, nil
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

func (s *Server) eligibleForRequest(res influxdb.Result, meta requestmeta.RequestMeta) influxdb.Result {
	eligible := make(influxdb.Result, 0, len(res))
	for _, item := range res {
		endpoints, ok := s.mirrorzd.Lookup(item.Mirror)
		if !ok {
			continue
		}
		for _, endpoint := range endpoints {
			if _, ok := endpoint.Match(meta); ok {
				eligible = append(eligible, item)
				break
			}
		}
	}
	return eligible
}

func (s *Server) outdatedReason(delta, dynamicCutoff int) string {
	if delta < -s.maxRepoStaleness {
		return fmt.Sprintf("absolute (delta=%d, limit=%d)", delta, -s.maxRepoStaleness)
	}
	if delta < dynamicCutoff {
		return fmt.Sprintf("dynamic (delta=%d, cutoff=%d)", delta, dynamicCutoff)
	}
	return ""
}

// ResolveBest tries to find the best mirror for the given request
func (s *Server) ResolveBest(ctx context.Context, meta requestmeta.RequestMeta) (scores scoring.Scores) {
	if meta.CName == "" {
		return s.resolveBestAll(ctx, meta)
	}
	res, ok := s.queryInflux(ctx, meta.CName)
	if !ok {
		return
	}
	return s.resolveBest(ctx, res, meta, 0)
}

func (s *Server) resolveBestAll(ctx context.Context, meta requestmeta.RequestMeta) (scores scoring.Scores) {
	res := make(influxdb.Result, 0)
	for _, abbr := range s.mirrorzd.Abbrs() {
		res = append(res, influxdb.Item{Mirror: abbr})
	}
	return s.resolveBest(ctx, res, meta, 1)
}

// Resolves the best mirror for the given request.
func (s *Server) resolveBest(ctx context.Context, res influxdb.Result, meta requestmeta.RequestMeta, mode int) (scores scoring.Scores) {
	tracer := ctx.Value(tracing.Key).(tracing.Tracer)
	deltaCutoff := calcDeltaCutoff(s.eligibleForRequest(res, meta))
	tracer.Printf("outdated thresholds: dynamic=%d absolute=%d\n", deltaCutoff, -s.maxRepoStaleness)

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
			if reason := s.outdatedReason(item.Value, deltaCutoff); reason != "" {
				tracer.Printf("    error: outdated: %s\n", reason)
				continue
			}
			if reason, ok := endpoint.Match(meta); !ok {
				tracer.Printf("    error: %s\n", reason)
				continue
			}
			score := scoring.Eval(endpoint, meta)
			score.Abbr, score.Delta, score.Repo =
				abbr, item.Value, item.Path
			// mirrorz-monitor encodes the U (unknown) main status as value 0.
			score.Unknown = item.Value == 0
			tracer.Printf("    score: %s\n", score)
			scoresEndpoints = append(scoresEndpoints, score)
		}

		if len(scoresEndpoints) == 0 {
			tracer.Printf("  no score available\n")
			continue
		}

		scoresEndpoints.Sort()
		for i, score := range scoresEndpoints {
			tracer.Printf("  score %d: %s\n", i, score)
			// when mode == 1, keep only the best score per endpoint
			if mode != 1 || i == 0 {
				scores = append(scores, score)
			}
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
func (s *Server) ResolveExist(ctx context.Context, res influxdb.Result, oldResolve string, meta requestmeta.RequestMeta) (resolve string, repo string) {
	tracer := ctx.Value(tracing.Key).(tracing.Tracer)
	deltaCutoff := calcDeltaCutoff(s.eligibleForRequest(res, meta))

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
				// Unknown repositories are fallback candidates. Re-score instead of
				// retaining one from a stale cache when a normal candidate may exist.
				if item.Value == 0 {
					tracer.Printf("  error: unknown repository is fallback only\n")
					continue
				}
				if reason := s.outdatedReason(item.Value, deltaCutoff); reason != "" {
					tracer.Printf("  error: outdated: %s\n", reason)
					continue
				}
				if reason, ok := endpoint.Match(meta); !ok {
					tracer.Printf("  error: %s\n", reason)
					continue
				}
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
