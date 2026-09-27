package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
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
		"https://ustc.example.com/repo/\tpriority:2",
		"",
	}, "\n"), apt.Body.String())
	assert.NotContains(t, apt.Body.String(), "countme")

	rpm := httptest.NewRecorder()
	s.ServeHTTP(rpm, mirrorlistRequest(http.MethodGet, "/api/rpm/mirrorlist/repo"))
	require.Equal(t, http.StatusOK, rpm.Code)
	assert.Equal(t, strings.Join([]string{
		"https://near.example.com/repo/",
		"https://ustc.example.com/repo/",
		"",
	}, "\n"), rpm.Body.String())
	assert.NotContains(t, rpm.Body.String(), "priority:")
	assert.Equal(t, 1, queries, "APT and RPM should share monitor data")

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

func TestMirrorlistBestEndpointPerSite(t *testing.T) {
	for _, test := range []struct {
		name, ip, label, endpoint string
		private                   bool
	}{
		{name: "higher score overrides configuration order", ip: "192.0.2.1", endpoint: "near"},
		{name: "tie follows configuration order", ip: "198.51.100.1", endpoint: "generic"},
		{name: "explicit preference", ip: "192.0.2.1", label: "tuna", endpoint: "generic"},
		{name: "avoided endpoint", ip: "192.0.2.1", label: "avoidtunanear", endpoint: "generic"},
		{name: "avoided site", ip: "192.0.2.1", label: "avoidtuna"},
		{name: "private endpoint inaccessible", ip: "198.51.100.1", label: "tunanear", endpoint: "generic", private: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(mirrorlistInfluxResponse))
			}))
			defer closeServer()
			patchRedirectSite(t, s, "tuna", map[string]any{"endpoints": []map[string]any{
				{"label": "tuna", "resolve": "generic.example.com", "public": true, "filter": []string{"V4", "V6", "SSL", "NOSSL"}},
				{"label": "tunanear", "resolve": "near.example.com", "public": !test.private, "filter": []string{"V4", "V6", "SSL", "NOSSL"}, "range": []string{"192.0.2.0/24"}},
			}})
			for _, format := range []string{"apt", "rpm"} {
				r := mirrorlistRequest(http.MethodGet, "/api/"+format+"/mirrorlist/repo")
				r.Header.Set("X-Real-IP", test.ip)
				if test.label != "" {
					r.Header.Set("X-Forwarded-Host", test.label+".mirrors.cernet.edu.cn")
				}
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				require.Equal(t, http.StatusOK, w.Code)
				urls := []string{"https://ustc.example.com/repo/"}
				if test.endpoint != "" {
					urls = append([]string{"https://" + test.endpoint + ".example.com/repo/"}, urls...)
				}
				for i := range urls {
					if format == "apt" {
						urls[i] += "\tpriority:" + strconv.Itoa(i+1)
					}
				}
				assert.Equal(t, strings.Join(urls, "\n")+"\n", w.Body.String(), format)
			}
		})
	}
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

func TestAPTMirrorlistOfficialIndex(t *testing.T) {
	for _, repo := range []struct{ cname, official string }{
		{"debian-security", "https://security.debian.org/debian-security/"},
		{"ubuntu", "https://security.ubuntu.com/ubuntu/"},
		{"ubuntu-ports", "https://ports.ubuntu.com/ubuntu-ports/"},
	} {
		t.Run(repo.cname, func(t *testing.T) {
			var queries atomic.Int32
			s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				queries.Add(1)
				_, _ = w.Write([]byte(strings.ReplaceAll(mirrorlistInfluxResponse, "repo", repo.cname)))
			}))
			defer closeServer()
			path := "/api/apt/mirrorlist/" + repo.cname
			plain := redirectGet(s, path)
			require.Equal(t, 200, plain.Code)
			want := repo.official + "\tpriority:0 type:index\n" + plain.Body.String() + repo.official + "\tpriority:3\n"
			actual := redirectGet(s, path+"?official_index=1")
			require.Equal(t, 200, actual.Code)
			assert.Equal(t, want, actual.Body.String())
			assert.Equal(t, strconv.Itoa(len(want)), actual.Header().Get("Content-Length"))
			assert.Equal(t, plain.Header().Get("Cache-Control"), actual.Header().Get("Cache-Control"))
			assert.Equal(t, plain.Header().Get("Vary"), actual.Header().Get("Vary"))
			head := httptest.NewRecorder()
			s.ServeHTTP(head, mirrorlistRequest(http.MethodHead, path+"?official_index=1"))
			assert.Equal(t, 200, head.Code)
			assert.Empty(t, head.Body.String())
			assert.Equal(t, actual.Header(), head.Header())
			for _, query := range []string{"", "?official_index=0", "?official_index=true", "?official_index="} {
				assert.Equal(t, plain.Body.String(), redirectGet(s, path+query).Body.String())
			}
			rpmPath := "/api/rpm/mirrorlist/" + repo.cname
			assert.Equal(t, redirectGet(s, rpmPath).Body.String(), redirectGet(s, rpmPath+"?official_index=1").Body.String())
			redirect := redirectGet(s, "/"+repo.cname+"/pool/package.deb")
			assert.Equal(t, 302, redirect.Code)
			assert.Equal(t, "https://near.example.com/"+repo.cname+"/pool/package.deb", redirect.Header().Get("Location"))
			assert.EqualValues(t, 1, queries.Load(), "all response formats share monitor data")
		})
	}
}

func TestOfficialIndexFallbackWithoutMirrors(t *testing.T) {
	for _, scenario := range []string{"empty", "excluded", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch scenario {
				case "unavailable":
					http.Error(w, "unavailable", 503)
				case "excluded":
					_, _ = w.Write([]byte(mirrorlistInfluxResponse))
				default:
					_, _ = w.Write([]byte(`{"results":[{}]}`))
				}
			}))
			defer closeServer()
			if scenario == "excluded" {
				for _, site := range []string{"tuna", "ustc"} {
					patchRedirectSite(t, s, site, map[string]any{"blacklist": []string{"ubuntu"}})
				}
			}
			actual := redirectGet(s, "/api/apt/mirrorlist/ubuntu?official_index=1")
			require.Equal(t, 200, actual.Code)
			assert.Equal(t, "https://security.ubuntu.com/ubuntu/\tpriority:0 type:index\nhttps://security.ubuntu.com/ubuntu/\tpriority:1\n", actual.Body.String())
			plain := redirectGet(s, "/api/apt/mirrorlist/ubuntu")
			if scenario == "unavailable" {
				assert.Equal(t, 503, plain.Code)
			} else {
				assert.Equal(t, 404, plain.Code)
			}
		})
	}
}

func TestOfficialIndexUsesStaleMonitorData(t *testing.T) {
	var queries atomic.Int32
	s, closeServer := newMirrorlistTestServer(t, 0, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if queries.Add(1) > 1 {
			http.Error(w, "unavailable", 503)
			return
		}
		_, _ = w.Write([]byte(mirrorlistInfluxResponse))
	}))
	defer closeServer()
	first := redirectGet(s, "/api/apt/mirrorlist/ubuntu?official_index=1")
	require.Equal(t, 200, first.Code)
	assert.Contains(t, first.Body.String(), "near.example.com")
	second := redirectGet(s, "/api/apt/mirrorlist/ubuntu?official_index=1")
	assert.Equal(t, 200, second.Code)
	assert.Equal(t, first.Body.String(), second.Body.String())
	assert.EqualValues(t, 2, queries.Load())
}

func TestOfficialIndexNormalizationAndDeduplication(t *testing.T) {
	s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := strings.ReplaceAll(mirrorlistInfluxResponse, `"url":"/repo"`, `"url":"https://security.ubuntu.com/ubuntu"`)
		_, _ = w.Write([]byte(response))
	}))
	defer closeServer()
	require.NoError(t, os.WriteFile(filepath.Join(s.mirrorzdDir, "redirects.json"), []byte(`{
		"redirects":[{"match":"^/alias$","target":"/ubuntu"}]
	}`), 0600))
	require.NoError(t, s.LoadMirrorZD())
	patchRedirectSite(t, s, "ustc", map[string]any{"mirrorlist_paths": map[string]string{"ubuntu": "/custom-root"}})
	w := redirectGet(s, "/api/apt/mirrorlist/alias?official_index=1")
	require.Equal(t, 200, w.Code)
	assert.Equal(t, "https://security.ubuntu.com/ubuntu/\tpriority:0 type:index\nhttps://ustc.example.com/custom-root/\tpriority:1\nhttps://security.ubuntu.com/ubuntu/\tpriority:2\n", w.Body.String())
}

func TestOfficialIndexValidation(t *testing.T) {
	s, closeServer := newMirrorlistTestServer(t, 300, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		t.Error("invalid requests should not query monitor data")
	}))
	defer closeServer()
	for _, test := range []struct {
		path string
		code int
	}{
		{"debian", 400},
		{"ubuntu-old-releases", 400},
		{"missing", 400},
		{"", 404},
		{"ubuntu/extra", 404},
		{"ubuntu/%00", 400},
	} {
		w := redirectGet(s, "/api/apt/mirrorlist/"+test.path+"?official_index=1")
		assert.Equal(t, test.code, w.Code, test.path)
	}
}
