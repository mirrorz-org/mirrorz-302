package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mirrorz-org/mirrorz-302/pkg/influxdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/mirrorz-org/mirrorz-302/pkg/tracing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func patchRedirectSite(t *testing.T, s *Server, site string, update map[string]any) {
	t.Helper()
	path := filepath.Join(s.mirrorzdDir, site, "config.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var config map[string]any
	require.NoError(t, json.Unmarshal(data, &config))
	for key, value := range update {
		config[key] = value
	}
	data, err = json.Marshal(config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0600))
	require.NoError(t, s.LoadMirrorZD())
}

func redirectGet(s *Server, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, mirrorlistRequest(http.MethodGet, path))
	return w
}

func TestTwoStageRedirectAndSharedSourceCache(t *testing.T) {
	var queries atomic.Int32
	s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries.Add(1)
		assert.Contains(t, r.URL.Query().Get("params"), `"cname":"repo"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mirrorlistInfluxResponse))
	}))
	defer closeServer()
	patchRedirectSite(t, s, "tuna", map[string]any{
		"redirects": map[string]any{"repo": map[string]any{
			"rewrite": []map[string]string{{"match": `^/(.*)$`, "target": `/debian/${1}`}},
		}},
		"mirrorlist_paths": map[string]string{"repo": "/repo/debian"},
	})
	require.NoError(t, os.WriteFile(filepath.Join(s.mirrorzdDir, "redirects.json"), []byte(`{
		"redirects":[{"match":"^/alias(?:/debian)?(/.*)?$","target":"/repo${1}"}]
	}`), 0600))
	require.NoError(t, s.LoadMirrorZD())
	for _, test := range []struct{ input, target string }{
		{"/alias/debian/dists/bookworm/InRelease?x=%2F", "https://near.example.com/repo/debian/dists/bookworm/InRelease?x=%2F"},
		{"/repo/pool/a%2Fb%25.deb", "https://near.example.com/repo/debian/pool/a%2Fb%25.deb"},
		{"/repo/pool/a%3Fb%23c.deb", "https://near.example.com/repo/debian/pool/a%3Fb%23c.deb"},
		{"/repo/pool/a%20b.deb", "https://near.example.com/repo/debian/pool/a%20b.deb"},
		{"/alias", "https://near.example.com/repo/debian/"},
	} {
		w := redirectGet(s, test.input)
		require.Equal(t, 302, w.Code, w.Body.String())
		assert.Equal(t, test.target, w.Header().Get("Location"))
	}
	apt := redirectGet(s, "/api/apt/mirrorlist/alias")
	require.Equal(t, 200, apt.Code)
	assert.Contains(t, apt.Body.String(), "https://near.example.com/repo/debian/\tpriority:1")
	rpm := redirectGet(s, "/api/rpm/mirrorlist/alias/debian/9/BaseOS/x86_64/os")
	require.Equal(t, 200, rpm.Code)
	assert.Contains(t, rpm.Body.String(), "https://near.example.com/repo/debian/9/BaseOS/x86_64/os/")
	assert.EqualValues(t, 1, queries.Load(), "aliases, tails and lists share monitor data")
	trace := redirectGet(s, "/alias/debian/file?trace=1")
	assert.Contains(t, trace.Body.String(), "canonical path: /repo/file")
	assert.Contains(t, trace.Body.String(), "target: https://near.example.com/repo/debian/file")
}

func TestPathFiltersDoNotPoisonOtherPathsOrMirrorlists(t *testing.T) {
	var queries atomic.Int32
	s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		queries.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mirrorlistInfluxResponse))
	}))
	defer closeServer()
	patchRedirectSite(t, s, "tuna", map[string]any{"redirects": map[string]any{"repo": map[string]any{
		"blacklist": []string{`^/dists/(stretch|buster)(/|$)`},
		"whitelist": []string{`^/dists/`, `^/pool/`},
	}}})
	for _, test := range []struct{ path, host string }{
		{"/repo/dists/stretch/InRelease", "ustc.example.com"},
		{"/repo/dists/bookworm/InRelease", "near.example.com"},
		{"/repo/other/file", "ustc.example.com"},
		{"/repo/pool/file.deb", "near.example.com"},
	} {
		w := redirectGet(s, test.path)
		require.Equal(t, 302, w.Code)
		assert.Contains(t, w.Header().Get("Location"), test.host)
	}
	request := mirrorlistRequest("GET", "/repo/dists/stretch/InRelease")
	request.Header.Set("X-Forwarded-Host", "tunanear.mirrors.cernet.edu.cn")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request)
	assert.Contains(t, w.Header().Get("Location"), "ustc.example.com", "preferences cannot bypass missing data")
	for _, path := range []string{"/api/apt/mirrorlist/repo", "/api/rpm/mirrorlist/repo/dists/stretch"} {
		w := redirectGet(s, path)
		require.Equal(t, 200, w.Code)
		assert.Contains(t, w.Body.String(), "near.example.com", "consumers handle missing paths")
	}
	assert.EqualValues(t, 2, queries.Load(), "only the different preference key causes another query")
	trace := redirectGet(s, "/repo/dists/buster/InRelease?trace=1")
	assert.Contains(t, trace.Body.String(), "path blacklist[0]")
}

func TestPathFilterPrecedesDeltaCutoff(t *testing.T) {
	s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer closeServer()
	patchRedirectSite(t, s, "tuna", map[string]any{"redirects": map[string]any{"repo": map[string]any{"blacklist": []string{`^/old/`}}}})
	res := influxdb.Result{{Mirror: "TUNA", Path: "/repo", Value: -100000}, {Mirror: "USTC", Path: "/repo", Value: -1}}
	meta := requestmeta.RequestMeta{CName: "repo", Tail: "/old/file", Scheme: "https", IP: net.ParseIP("192.0.2.1")}
	eligible := s.eligibleForRequest(res, meta)
	require.Len(t, eligible, 1)
	assert.Equal(t, -1, calcDeltaCutoff(eligible))
	ctx := context.WithValue(context.Background(), tracing.Key, tracing.NewTracer(false))
	resolve, path := s.ResolveExist(ctx, res, "near.example.com", meta)
	assert.Empty(t, resolve)
	assert.Empty(t, path)
}

func TestRewriteMirrorlistNeedsDeclaredRoot(t *testing.T) {
	s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mirrorlistInfluxResponse))
	}))
	defer closeServer()
	patchRedirectSite(t, s, "tuna", map[string]any{"redirects": map[string]any{"repo": map[string]any{
		"rewrite":   []map[string]string{{"match": `^/old/(.*)$`, "target": `/new/${1}`}},
		"blacklist": []string{`^/missing/`},
	}}})
	assert.NotContains(t, redirectGet(s, "/api/apt/mirrorlist/repo").Body.String(), "near.example.com")
	patchRedirectSite(t, s, "tuna", map[string]any{"mirrorlist_paths": map[string]string{"repo": "/actual-root"}})
	assert.Contains(t, redirectGet(s, "/api/rpm/mirrorlist/repo/missing").Body.String(), "https://near.example.com/actual-root/missing/")
}

func TestRedirectReloadAndInvalidRequests(t *testing.T) {
	s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mirrorlistInfluxResponse))
	}))
	defer closeServer()
	global := filepath.Join(s.mirrorzdDir, "redirects.json")
	for _, target := range []string{"/repo/one", "/repo/two"} {
		data, _ := json.Marshal(map[string]any{"redirects": []map[string]string{{"match": "^/alias$", "target": target}}})
		require.NoError(t, os.WriteFile(global, data, 0600))
		require.NoError(t, s.LoadMirrorZD())
		assert.Equal(t, "https://near.example.com"+target, redirectGet(s, "/alias").Header().Get("Location"))
	}
	for _, invalid := range []string{`{"redirects":[{"match":"[","target":"/repo"}]}`, `{"rewrites":[]}`, `null`} {
		require.NoError(t, os.WriteFile(global, []byte(invalid), 0600))
		require.Error(t, s.LoadMirrorZD())
		assert.Equal(t, "https://near.example.com/repo/two", redirectGet(s, "/alias").Header().Get("Location"))
	}
	for _, path := range []string{"/repo/%2e%2e/file", "/repo/%5cfile", "/repo/%00file", "/repo%2Fother/file"} {
		assert.Equal(t, 400, redirectGet(s, path).Code, path)
	}
}

func TestConcurrentPathRulesAndReload(t *testing.T) {
	s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mirrorlistInfluxResponse))
	}))
	defer closeServer()
	patchRedirectSite(t, s, "tuna", map[string]any{"redirects": map[string]any{"repo": map[string]any{"blacklist": []string{`^/old/`}}}})
	var group sync.WaitGroup
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 20; j++ {
				w := redirectGet(s, "/repo/old/file")
				assert.Equal(t, 302, w.Code)
				assert.Contains(t, w.Header().Get("Location"), "ustc.example.com")
			}
		}()
	}
	for i := 0; i < 10; i++ {
		require.NoError(t, s.LoadMirrorZD())
	}
	group.Wait()
}

func TestRewritesKeepMonitorRootsAndEndpointBasePaths(t *testing.T) {
	for _, test := range []struct{ root, expected string }{
		{"/site-repo", "https://near.example.com/proxy/site-repo"},
		{"https://archive.example.org/site-repo", "https://archive.example.org/site-repo"},
	} {
		t.Run(test.root, func(t *testing.T) {
			s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(strings.ReplaceAll(mirrorlistInfluxResponse, `"url":"/repo"`, `"url":"`+test.root+`"`)))
			}))
			defer closeServer()
			patchRedirectSite(t, s, "tuna", map[string]any{
				"endpoints": []map[string]any{{"label": "tuna", "resolve": "near.example.com/proxy", "public": true, "filter": []string{"V4", "SSL"}, "range": []string{"192.0.2.0/24"}}},
				"redirects": map[string]any{"repo": map[string]any{"rewrite": []map[string]string{{"match": `^/(.*)$`, "target": `/debian/${1}`}}}},
			})
			w := redirectGet(s, "/repo/file")
			require.Equal(t, 302, w.Code)
			assert.Equal(t, test.expected+"/debian/file", w.Header().Get("Location"))
			meta := s.meta.Parse(mirrorlistRequest("GET", "/repo/file"))
			ctx := context.WithValue(context.Background(), tracing.Key, tracing.NewTracer(false))
			target, err := s.Resolve(ctx, meta)
			require.NoError(t, err)
			assert.Equal(t, test.expected+"/debian/file", target)
		})
	}
}

func TestAllPathsExcludedDoesNotCacheRepositoryFailure(t *testing.T) {
	var queries atomic.Int32
	s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		queries.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mirrorlistInfluxResponse))
	}))
	defer closeServer()
	for _, site := range []string{"tuna", "ustc"} {
		patchRedirectSite(t, s, site, map[string]any{"redirects": map[string]any{"repo": map[string]any{"blacklist": []string{`^/old/`}}}})
	}
	assert.Equal(t, 404, redirectGet(s, "/repo/old/file").Code)
	assert.Equal(t, 302, redirectGet(s, "/repo/new/file").Code)
	assert.EqualValues(t, 1, queries.Load())
}
