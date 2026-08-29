package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mirrorz-org/mirrorz-302/pkg/influxdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/mirrorzdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/mirrorz-org/mirrorz-302/pkg/tracing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeSiteConfig(t *testing.T, dir, name, content string) {
	t.Helper()
	siteDir := filepath.Join(dir, name)
	require.NoError(t, os.Mkdir(siteDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(siteDir, "config.json"), []byte(content), 0o600))
}

func TestExcludeOfflineMirrors(t *testing.T) {
	newest := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	res := influxdb.Result{
		{Mirror: "five-minutes-old", Time: newest.Add(-5 * time.Minute)},
		{Mirror: "newest", Time: newest},
		{Mirror: "four-minutes-old", Time: newest.Add(-4 * time.Minute)},
		{Mirror: "much-older", Time: newest.Add(-30 * time.Minute)},
	}

	filtered := excludeOfflineMirrors(res)

	require.Len(t, filtered, 2)
	assert.Equal(t, "newest", filtered[0].Mirror)
	assert.Equal(t, "four-minutes-old", filtered[1].Mirror)
}

func TestExcludeOfflineMirrorsUsesRelativeTime(t *testing.T) {
	// Even if the monitor itself has been down for a long time, mirrors with
	// mutually close timestamps remain available.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	res := influxdb.Result{
		{Mirror: "older", Time: old},
		{Mirror: "newer", Time: old.Add(time.Minute)},
	}

	filtered := excludeOfflineMirrors(res)

	require.Len(t, filtered, 2)
	assert.Equal(t, "newer", filtered[0].Mirror)
	assert.Equal(t, "older", filtered[1].Mirror)
}

func TestCalcDeltaCutoff(t *testing.T) {
	as := assert.New(t)
	data := []int{-11, -1, -1, -1, -1, -1, -1, -1, -1, -1,
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	payload := make(influxdb.Result, len(data))
	for i, d := range data {
		payload[i].Value = d
	}
	// avg = -2, std = 3, zero and positive values are ignored
	as.Equal(-8, calcDeltaCutoff(payload))
}
func TestOutdatedReasonAbsoluteLimit(t *testing.T) {
	s := &Server{maxRepoStaleness: DefaultMaxRepoStaleness}

	assert.Empty(t, s.outdatedReason(-DefaultMaxRepoStaleness, -2_000_000))
	assert.Contains(t, s.outdatedReason(-DefaultMaxRepoStaleness-1, -2_000_000), "absolute")
	assert.Contains(t, s.outdatedReason(-100, -99), "dynamic")
	assert.Empty(t, s.outdatedReason(0, -1))
}

func TestNewServerDefaultsMaxRepoStaleness(t *testing.T) {
	s := NewServer(Config{})
	defer s.influx.Close()

	assert.Equal(t, DefaultMaxRepoStaleness, s.maxRepoStaleness)

	s = NewServer(Config{MaxRepoStaleness: 3600})
	defer s.influx.Close()
	assert.Equal(t, 3600, s.maxRepoStaleness)
}

func TestDeltaCutoffExcludesMirrorsWithoutMatchingEndpoint(t *testing.T) {
	dir := t.TempDir()
	writeMirror := func(name, abbr string) {
		content := `{
  "abbrs": ["` + abbr + `"],
  "endpoints": [{
    "label": "` + abbr + `",
    "resolve": "` + abbr + `.example.com",
    "public": true,
    "filter": ["V4", "SSL"],
    "range": []
  }]
}`
		writeSiteConfig(t, dir, name, content)
	}
	writeMirror("a", "A")
	writeMirror("b", "B")

	db := mirrorzdb.NewMirrorZDatabase()
	require.NoError(t, db.Load(dir))
	s := &Server{mirrorzd: db, maxRepoStaleness: DefaultMaxRepoStaleness}
	meta := requestmeta.RequestMeta{
		IP:     net.ParseIP("192.0.2.1"),
		Scheme: "https",
	}
	res := influxdb.Result{
		{Mirror: "A", Value: -10},
		{Mirror: "B", Value: -10},
		{Mirror: "MISSING", Value: -10_000_000},
	}

	eligible := s.eligibleForRequest(res, meta)
	require.Len(t, eligible, 2)
	assert.Equal(t, -10, calcDeltaCutoff(eligible))
}

func TestResolveExistRejectsAbsolutelyOutdatedMirror(t *testing.T) {
	dir := t.TempDir()
	content := `{
  "abbrs": ["A"],
  "endpoints": [{
    "label": "a",
    "resolve": "a.example.com",
    "public": true,
    "filter": ["V4", "SSL"],
    "range": []
  }]
}`
	writeSiteConfig(t, dir, "a", content)
	db := mirrorzdb.NewMirrorZDatabase()
	require.NoError(t, db.Load(dir))
	s := &Server{mirrorzd: db, maxRepoStaleness: DefaultMaxRepoStaleness}
	meta := requestmeta.RequestMeta{IP: net.ParseIP("192.0.2.1"), Scheme: "https"}
	ctx := context.WithValue(context.Background(), tracing.Key, tracing.NewTracer(false))
	res := influxdb.Result{{Mirror: "A", Value: -DefaultMaxRepoStaleness - 1, Path: "/ubuntu"}}

	resolve, repo := s.ResolveExist(ctx, res, "a.example.com", meta)
	assert.Empty(t, resolve)
	assert.Empty(t, repo)
}

func TestResolveBestUsesUnknownOnlyAsFallback(t *testing.T) {
	dir := t.TempDir()
	writeSite := func(name, abbr string) {
		content := `{
  "abbrs": ["` + abbr + `"],
  "endpoints": [{
    "label": "` + abbr + `",
    "resolve": "` + abbr + `.example.com",
    "public": true,
    "filter": ["V4", "SSL"],
    "range": []
  }]
}`
		writeSiteConfig(t, dir, name, content)
	}
	writeSite("unknown", "UNKNOWN")
	writeSite("normal", "NORMAL")

	db := mirrorzdb.NewMirrorZDatabase()
	require.NoError(t, db.Load(dir))
	s := &Server{mirrorzd: db, maxRepoStaleness: DefaultMaxRepoStaleness}
	meta := requestmeta.RequestMeta{
		CName: "repo", IP: net.ParseIP("192.0.2.1"), Scheme: "https",
	}
	ctx := context.WithValue(context.Background(), tracing.Key, tracing.NewTracer(false))
	res := influxdb.Result{
		{Mirror: "UNKNOWN", Value: 0, Path: "/repo"},
		{Mirror: "NORMAL", Value: -10, Path: "/repo"},
	}

	scores := s.resolveBest(ctx, res, meta, 0)
	require.Len(t, scores, 2)
	assert.Equal(t, "NORMAL", scores[0].Abbr)
	assert.False(t, scores[0].Unknown)
	assert.Equal(t, "UNKNOWN", scores[1].Abbr)
	assert.True(t, scores[1].Unknown)

	fallback := s.resolveBest(ctx, res[:1], meta, 0)
	require.Len(t, fallback, 1)
	assert.Equal(t, "UNKNOWN", fallback[0].Abbr)
}

func TestResolveExistDoesNotRetainUnknown(t *testing.T) {
	dir := t.TempDir()
	content := `{
  "abbrs": ["A"],
  "endpoints": [{
    "label": "a", "resolve": "a.example.com", "public": true,
    "filter": ["V4", "SSL"], "range": []
  }]
}`
	writeSiteConfig(t, dir, "a", content)
	db := mirrorzdb.NewMirrorZDatabase()
	require.NoError(t, db.Load(dir))
	s := &Server{mirrorzd: db, maxRepoStaleness: DefaultMaxRepoStaleness}
	meta := requestmeta.RequestMeta{
		CName: "repo", IP: net.ParseIP("192.0.2.1"), Scheme: "https",
	}
	ctx := context.WithValue(context.Background(), tracing.Key, tracing.NewTracer(false))

	resolve, repo := s.ResolveExist(ctx,
		influxdb.Result{{Mirror: "A", Value: 0, Path: "/repo"}},
		"a.example.com", meta)
	assert.Empty(t, resolve)
	assert.Empty(t, repo)
}

func TestResolveRepoFromInfluxWithStaticSiteConfig(t *testing.T) {
	influx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/query", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"series":[{"name":"repo","tags":{"mirror":"TUNA.NANO","url":"/archlinux"},"columns":["time","value","disable"],"values":[["2026-08-25T00:00:00Z",-1,false]]}]}]}`))
	}))
	defer influx.Close()

	dir := t.TempDir()
	config := `{"abbrs":["TUNA.NANO","TUNA.NEO"],"endpoints":[{"label":"tuna","resolve":"mirrors.tuna.tsinghua.edu.cn","public":true,"filter":["V4","V6","SSL","NOSSL"]}]}`
	writeSiteConfig(t, dir, "tuna", config)

	s := NewServer(Config{
		InfluxDB:          influxdb.Config{URL: influx.URL, Database: "mirrorz"},
		MirrorZDDirectory: dir,
		DomainLength:      5,
		CacheTime:         300,
	})
	require.NoError(t, s.LoadMirrorZD())

	r := httptest.NewRequest(http.MethodGet, "https://mirrors.cernet.edu.cn/archlinux/iso/latest/", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "mirrors.cernet.edu.cn")
	r.Header.Set("X-Real-IP", "192.0.2.1")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)

	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "https://mirrors.tuna.tsinghua.edu.cn/archlinux/iso/latest/", w.Header().Get("Location"))
}

func TestScoringAPIIncludesEveryConfiguredAbbr(t *testing.T) {
	dir := t.TempDir()
	config := `{"abbrs":["TUNA.NANO","TUNA.NEO"],"endpoints":[{"label":"tuna","resolve":"mirrors.tuna.tsinghua.edu.cn","public":true,"filter":["V4","V6","SSL","NOSSL"]}]}`
	writeSiteConfig(t, dir, "tuna", config)

	s := NewServer(Config{MirrorZDDirectory: dir, DomainLength: 5})
	require.NoError(t, s.LoadMirrorZD())
	r := httptest.NewRequest(http.MethodGet, "https://mirrors.cernet.edu.cn/api/scoring", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "mirrors.cernet.edu.cn")
	r.Header.Set("X-Real-IP", "192.0.2.1")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	var response ScoringAPIResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Len(t, response.Scores, 2)
	assert.ElementsMatch(t, []string{"TUNA.NANO", "TUNA.NEO"}, []string{response.Scores[0].Abbr, response.Scores[1].Abbr})
}
