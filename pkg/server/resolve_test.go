package server

import (
	"context"
	"net"
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
  "extension": "D",
  "site": {"abbr": "` + abbr + `"},
  "endpoints": [{
    "label": "` + abbr + `",
    "resolve": "` + abbr + `.example.com",
    "public": true,
    "filter": ["V4", "SSL"],
    "range": []
  }]
}`
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	writeMirror("a.json", "A")
	writeMirror("b.json", "B")

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
  "extension": "D",
  "site": {"abbr": "A"},
  "endpoints": [{
    "label": "a",
    "resolve": "a.example.com",
    "public": true,
    "filter": ["V4", "SSL"],
    "range": []
  }]
}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.json"), []byte(content), 0o600))
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
	writeSite := func(name, abbr, status string) {
		content := `{
  "extension": "D",
  "site": {"abbr": "` + abbr + `"},
  "endpoints": [{
    "label": "` + abbr + `",
    "resolve": "` + abbr + `.example.com",
    "public": true,
    "filter": ["V4", "SSL"],
    "range": []
  }],
  "mirrors": [{"cname": "repo", "url": "/repo", "status": "` + status + `"}]
}`
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	writeSite("unknown.json", "UNKNOWN", "U")
	writeSite("normal.json", "NORMAL", "S")

	db := mirrorzdb.NewMirrorZDatabase()
	require.NoError(t, db.Load(dir))
	s := &Server{mirrorzd: db, maxRepoStaleness: DefaultMaxRepoStaleness}
	meta := requestmeta.RequestMeta{
		CName: "repo", IP: net.ParseIP("192.0.2.1"), Scheme: "https",
	}
	ctx := context.WithValue(context.Background(), tracing.Key, tracing.NewTracer(false))
	res := influxdb.Result{
		{Mirror: "UNKNOWN", Value: -1, Path: "/repo"},
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
  "extension": "D",
  "site": {"abbr": "A"},
  "endpoints": [{
    "label": "a", "resolve": "a.example.com", "public": true,
    "filter": ["V4", "SSL"], "range": []
  }],
  "mirrors": [{"cname": "repo", "url": "/repo", "status": "U"}]
}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.json"), []byte(content), 0o600))
	db := mirrorzdb.NewMirrorZDatabase()
	require.NoError(t, db.Load(dir))
	s := &Server{mirrorzd: db, maxRepoStaleness: DefaultMaxRepoStaleness}
	meta := requestmeta.RequestMeta{
		CName: "repo", IP: net.ParseIP("192.0.2.1"), Scheme: "https",
	}
	ctx := context.WithValue(context.Background(), tracing.Key, tracing.NewTracer(false))

	resolve, repo := s.ResolveExist(ctx,
		influxdb.Result{{Mirror: "A", Value: -1, Path: "/repo"}},
		"a.example.com", meta)
	assert.Empty(t, resolve)
	assert.Empty(t, repo)
}
