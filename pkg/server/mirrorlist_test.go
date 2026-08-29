package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mirrorz-org/mirrorz-302/pkg/influxdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/scoring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mirrorlistInfluxResponse = `{"results":[{"series":[
  {"name":"repo","tags":{"mirror":"TUNA","url":"/repo"},"columns":["time","value","disable"],"values":[["2026-08-27T00:00:00Z",-1,false]]},
  {"name":"repo","tags":{"mirror":"USTC","url":"/repo"},"columns":["time","value","disable"],"values":[["2026-08-27T00:00:00Z",-2,false]]}
]}]}`

func newMirrorlistTestServer(t *testing.T, cacheTime int, influxHandler http.Handler) (*Server, func()) {
	t.Helper()

	influx := httptest.NewServer(influxHandler)
	dir := t.TempDir()
	tuna := `{"abbrs":["TUNA"],"endpoints":[
  {"label":"tuna-near","resolve":"near.example.com","public":true,"filter":["V4","V6","SSL","NOSSL"],"range":["192.0.2.0/24"]},
  {"label":"tuna","resolve":"generic.example.com","public":true,"filter":["V4","V6","SSL","NOSSL"]}
]}`
	ustc := `{"abbrs":["USTC"],"endpoints":[
  {"label":"ustc","resolve":"ustc.example.com","public":true,"filter":["V4","V6","SSL","NOSSL"]}
]}`
	writeSiteConfig(t, dir, "tuna", tuna)
	writeSiteConfig(t, dir, "ustc", ustc)

	s := NewServer(Config{
		InfluxDB:          influxdb.Config{URL: influx.URL, Database: "mirrorz"},
		MirrorZDDirectory: dir,
		DomainLength:      5,
		CacheTime:         cacheTime,
	})
	require.NoError(t, s.LoadMirrorZD())
	return s, influx.Close
}

func mirrorlistRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, "https://mirrors.cernet.edu.cn"+path, nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "mirrors.cernet.edu.cn")
	r.Header.Set("X-Real-IP", "192.0.2.1")
	return r
}

func TestCandidateURLs(t *testing.T) {
	scores := scoring.Scores{
		{Resolve: "one.example.com", Repo: "/repo"},
		{Resolve: "one.example.com", Repo: "/repo/"},
		{Resolve: "ignored.example.com", Repo: "http://absolute.example.com/repository"},
	}

	assert.Equal(t, []string{
		"https://one.example.com/repo/",
		"http://absolute.example.com/repository/",
	}, candidateURLs(scores, "https"))
	assert.NotNil(t, candidateURLs(nil, "https"))
}

func TestCleanMirrorlistTail(t *testing.T) {
	for _, test := range []struct {
		input, expected string
		ok              bool
	}{
		{"", "", true},
		{"/", "", true},
		{"/9/BaseOS/x86_64/os", "9/BaseOS/x86_64/os", true},
		{"/9/BaseOS/x86_64/os/", "9/BaseOS/x86_64/os", true},
		{"/path with spaces/repo", "path%20with%20spaces/repo", true},
		{"9/BaseOS", "", false},
		{"/9//BaseOS", "", false},
		{"/9/./BaseOS", "", false},
		{"/9/../BaseOS", "", false},
		{`/9/BaseOS\x86_64`, "", false},
	} {
		actual, ok := cleanMirrorlistTail(test.input)
		assert.Equal(t, test.ok, ok, test.input)
		assert.Equal(t, test.expected, actual, test.input)
	}
}

func TestMirrorlistFormatsAndSharedCache(t *testing.T) {
	queries := 0
	s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		queries++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mirrorlistInfluxResponse))
	}))
	defer closeServer()

	apt := httptest.NewRecorder()
	s.ServeHTTP(apt, mirrorlistRequest(http.MethodGet, "/api/apt/mirrorlist/repo?countme=2"))
	require.Equal(t, http.StatusOK, apt.Code)
	assert.Equal(t, "text/plain; charset=utf-8", apt.Header().Get("Content-Type"))
	assert.Equal(t, "private, max-age=300", apt.Header().Get("Cache-Control"))
	assert.Equal(t, "X-Real-IP, X-Forwarded-Proto, X-Forwarded-Host", apt.Header().Get("Vary"))
	assert.Equal(t, strings.Join([]string{
		"https://near.example.com/repo/\tpriority:1",
		"https://generic.example.com/repo/\tpriority:2",
		"https://ustc.example.com/repo/\tpriority:3",
		"",
	}, "\n"), apt.Body.String())
	assert.NotContains(t, apt.Body.String(), "countme")

	rpm := httptest.NewRecorder()
	s.ServeHTTP(rpm, mirrorlistRequest(http.MethodGet, "/api/rpm/mirrorlist/repo"))
	require.Equal(t, http.StatusOK, rpm.Code)
	assert.Equal(t, strings.Join([]string{
		"https://near.example.com/repo/",
		"https://generic.example.com/repo/",
		"https://ustc.example.com/repo/",
		"",
	}, "\n"), rpm.Body.String())
	assert.NotContains(t, rpm.Body.String(), "priority:")
	assert.Equal(t, 1, queries, "APT and RPM should share the ordered candidate cache")

	redirect := httptest.NewRecorder()
	s.ServeHTTP(redirect, mirrorlistRequest(http.MethodGet, "/repo/file.rpm"))
	assert.Equal(t, http.StatusFound, redirect.Code)
	assert.Equal(t, "https://near.example.com/repo/file.rpm", redirect.Header().Get("Location"))
	assert.Equal(t, 1, queries, "regular redirects should reuse the candidate cache")

	head := httptest.NewRecorder()
	s.ServeHTTP(head, mirrorlistRequest(http.MethodHead, "/api/rpm/mirrorlist/repo"))
	assert.Equal(t, http.StatusOK, head.Code)
	assert.Empty(t, head.Body.String())
	assert.Equal(t, rpm.Header().Get("Content-Length"), head.Header().Get("Content-Length"))
}

func TestRPMMirrorlistAppendsExpandedRepositoryPath(t *testing.T) {
	queries := 0
	s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		queries++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mirrorlistInfluxResponse))
	}))
	defer closeServer()

	baseOS := httptest.NewRecorder()
	s.ServeHTTP(baseOS, mirrorlistRequest(http.MethodGet,
		"/api/rpm/mirrorlist/repo/9/BaseOS/x86_64/os/"))
	require.Equal(t, http.StatusOK, baseOS.Code)
	assert.Contains(t, baseOS.Body.String(), "https://near.example.com/repo/9/BaseOS/x86_64/os/\n")
	assert.Contains(t, baseOS.Body.String(), "https://ustc.example.com/repo/9/BaseOS/x86_64/os/\n")

	appStream := httptest.NewRecorder()
	s.ServeHTTP(appStream, mirrorlistRequest(http.MethodGet,
		"/api/rpm/mirrorlist/repo/9/AppStream/x86_64/os"))
	require.Equal(t, http.StatusOK, appStream.Code)
	assert.Contains(t, appStream.Body.String(), "https://near.example.com/repo/9/AppStream/x86_64/os/\n")
	assert.Equal(t, 1, queries, "repository tails should not create separate candidate cache entries")
}

func TestMirrorlistRequestValidation(t *testing.T) {
	s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mirrorlistInfluxResponse))
	}))
	defer closeServer()

	post := httptest.NewRecorder()
	s.ServeHTTP(post, mirrorlistRequest(http.MethodPost, "/api/rpm/mirrorlist/repo"))
	assert.Equal(t, http.StatusMethodNotAllowed, post.Code)
	assert.Equal(t, "GET, HEAD", post.Header().Get("Allow"))

	for _, path := range []string{
		"/api/apt/mirrorlist/",
		"/api/apt/mirrorlist/repo/extra",
	} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, mirrorlistRequest(http.MethodGet, path))
		assert.Equal(t, http.StatusNotFound, w.Code, path)
	}
}

func TestMirrorlistNotFound(t *testing.T) {
	s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{}]}`))
	}))
	defer closeServer()

	w := httptest.NewRecorder()
	s.ServeHTTP(w, mirrorlistRequest(http.MethodGet, "/api/rpm/mirrorlist/missing"))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestMirrorlistUsesStaleCacheOnInfluxFailure(t *testing.T) {
	queries := 0
	s, closeServer := newMirrorlistTestServer(t, 0, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		queries++
		if queries > 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(mirrorlistInfluxResponse))
	}))
	defer closeServer()

	first := httptest.NewRecorder()
	s.ServeHTTP(first, mirrorlistRequest(http.MethodGet, "/api/rpm/mirrorlist/repo"))
	require.Equal(t, http.StatusOK, first.Code)

	second := httptest.NewRecorder()
	s.ServeHTTP(second, mirrorlistRequest(http.MethodGet, "/api/rpm/mirrorlist/repo"))
	assert.Equal(t, http.StatusOK, second.Code)
	assert.Equal(t, first.Body.String(), second.Body.String())
	assert.Equal(t, 2, queries)
}

func TestMirrorlistReturnsServiceUnavailableWithoutCache(t *testing.T) {
	s, closeServer := newMirrorlistTestServer(t, int(time.Minute/time.Second), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer closeServer()

	w := httptest.NewRecorder()
	s.ServeHTTP(w, mirrorlistRequest(http.MethodGet, "/api/apt/mirrorlist/repo"))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}
