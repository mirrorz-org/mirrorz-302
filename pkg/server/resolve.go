package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mirrorz-org/mirrorz-302/pkg/caching"
	"github.com/mirrorz-org/mirrorz-302/pkg/influxdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/mirrorzdb"
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

// ErrInvalidPath identifies malformed requests or invalid global rewrite output.
var ErrInvalidPath = errors.New("invalid repository path")
var errMirrorlistNotFound = errors.New("invalid mirrorlist repository")
var errOfficialIndexUnsupported = errors.New("official_index is only supported for debian-security, ubuntu and ubuntu-ports")

// normalizeMeta must run under configMu, together with site rule evaluation.
func (s *Server) normalizeMeta(ctx context.Context, meta requestmeta.RequestMeta) (requestmeta.RequestMeta, error) {
	input := "/" + url.PathEscape(meta.CName) + meta.Tail
	canonical, err := s.mirrorzd.Normalize(input)
	if err != nil {
		return meta, fmt.Errorf("%w: %v", ErrInvalidPath, err)
	}
	parts := strings.SplitN(strings.TrimPrefix(canonical, "/"), "/", 2)
	cname, err := url.PathUnescape(parts[0])
	if err != nil || !mirrorzdb.ValidCName(cname) {
		return meta, fmt.Errorf("%w: invalid cname", ErrInvalidPath)
	}
	tracer := ctx.Value(tracing.Key).(tracing.Tracer)
	tracer.Printf("Request path: %s; canonical path: %s\n", input, canonical)
	meta.CName, meta.Tail = cname, ""
	if len(parts) == 2 {
		meta.Tail = "/" + parts[1]
	}
	return meta, nil
}

// monitorResults shares an unfiltered snapshot between redirects and lists.
// Only mirrorlists retain the existing stale-data fallback on query failure.
func (s *Server) monitorResults(ctx context.Context, meta requestmeta.RequestMeta) (influxdb.Result, error) {
	key := requestmeta.CacheKey(meta)
	cached, status := s.resolved.Load(key)
	tracer := ctx.Value(tracing.Key).(tracing.Tracer)
	if status == caching.StatusFresh && cached.Source != nil && !tracer.Enabled() {
		s.resolved.Touch(key)
		return cached.Source, nil
	}
	res, ok := s.queryInflux(ctx, meta.CName)
	if !ok {
		if meta.Mirrorlist && len(cached.Source) > 0 {
			s.resolved.Touch(key)
			return cached.Source, nil
		}
		return nil, fmt.Errorf("queryInflux failed")
	}
	s.resolved.Store(key, caching.Resolved{Source: res})
	return res, nil
}

// Resolve returns the complete target URL, excluding its query string.
// Path-dependent selection uses cached monitor data, not cached redirect URLs.
func (s *Server) Resolve(ctx context.Context, meta requestmeta.RequestMeta) (string, error) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	meta.Mirrorlist = false
	meta, err := s.normalizeMeta(ctx, meta)
	if err != nil {
		return "", err
	}
	res, err := s.monitorResults(ctx, meta)
	if err != nil {
		return "", err
	}
	tracer := ctx.Value(tracing.Key).(tracing.Tracer)
	tracer.Printf("Labels: %v\nIP: %s\nScheme: %s\n", meta.Labels, meta.IP, meta.Scheme)
	for _, score := range s.resolveBest(ctx, res, meta, 0) {
		endpoints, _ := s.mirrorzd.Lookup(score.Abbr)
		for _, endpoint := range endpoints {
			if endpoint.Label != score.Label {
				continue
			}
			tail, rule, err := endpoint.Redirects[meta.CName].ApplyWithRule(meta.Tail)
			if err != nil {
				continue
			}
			target := appendRepositoryPath(repositoryURL(score, meta.Scheme), tail)
			if rule != "" {
				tracer.Printf("Site rewrite on %s: %s\n", endpoint.Label, rule)
			}
			tracer.Printf("Path on %s: %s -> %s; target: %s\n", endpoint.Label, meta.Tail, tail, target)
			s.resolveLogger.Debugf("%s", tracer.String())
			s.resolveLogger.Infof("R: %s %s %s\n", target, meta, score)
			return target, nil
		}
	}
	s.failLogger.Debugf("%s", tracer.String())
	s.failLogger.Infof("F: %s\n", meta)
	return "", nil
}

func appendRepositoryPath(root, tail string) string {
	if tail == "" {
		return root
	}
	return strings.TrimRight(root, "/") + tail
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

// resolveMirrorlist normalizes its repository path using the same snapshot as
// site configuration. Path blacklists and whitelists intentionally do not apply.
func (s *Server) resolveMirrorlist(ctx context.Context, meta requestmeta.RequestMeta, apt, officialIndex bool) (list mirrorlist, err error) {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if meta.CName == "" {
		return list, errMirrorlistNotFound
	}
	meta.Mirrorlist = true
	meta, err = s.normalizeMeta(ctx, meta)
	if err != nil {
		return list, err
	}
	if apt && meta.Tail != "" {
		return list, errMirrorlistNotFound
	}
	decodedTail, err := url.PathUnescape(meta.Tail)
	if err != nil {
		return list, ErrInvalidPath
	}
	tail, ok := cleanMirrorlistTail(decodedTail)
	if !ok {
		return list, ErrInvalidPath
	}
	if apt && officialIndex {
		switch meta.CName {
		case "debian-security":
			list.OfficialURL = "https://security.debian.org/debian-security/"
		case "ubuntu":
			list.OfficialURL = "https://security.ubuntu.com/ubuntu/"
		case "ubuntu-ports":
			list.OfficialURL = "https://ports.ubuntu.com/ubuntu-ports/"
		default:
			return list, errOfficialIndexUnsupported
		}
	}
	res, err := s.monitorResults(ctx, meta)
	if err != nil {
		if list.OfficialURL == "" {
			return list, err
		}
		// Official repositories remain usable without monitor data.
		list.URLs = []string{list.OfficialURL}
		return list, nil
	}
	scores := s.resolveBest(ctx, res, meta, 0)
	candidates := make(scoring.Scores, 0, len(scores))
	for _, score := range scores {
		endpoints, _ := s.mirrorzd.Lookup(score.Abbr)
		for _, endpoint := range endpoints {
			if endpoint.Label != score.Label {
				continue
			}
			path, declared := endpoint.MirrorlistPaths[meta.CName]
			if declared {
				score.Repo = path
			}
			candidates = append(candidates, score)
			break
		}
	}
	list.URLs = appendMirrorlistTail(candidateURLs(candidates, meta.Scheme), tail)
	if list.OfficialURL != "" {
		mirrors := make([]string, 0, len(list.URLs)+1)
		for _, candidate := range list.URLs {
			if candidate != list.OfficialURL {
				mirrors = append(mirrors, candidate)
			}
		}
		list.URLs = append(mirrors, list.OfficialURL)
	}
	return list, nil
}

type mirrorlist struct {
	URLs        []string
	OfficialURL string
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
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if meta.CName == "" {
		return s.resolveBestAll(ctx, meta)
	}
	var err error
	meta, err = s.normalizeMeta(ctx, meta)
	if err != nil {
		return nil
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
