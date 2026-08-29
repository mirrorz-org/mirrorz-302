package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/juju/loggo"
	"github.com/mirrorz-org/mirrorz-302/pkg/caching"
	"github.com/mirrorz-org/mirrorz-302/pkg/influxdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/logging"
	"github.com/mirrorz-org/mirrorz-302/pkg/mirrorzdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/mirrorz-org/mirrorz-302/pkg/scoring"
	"github.com/mirrorz-org/mirrorz-302/pkg/tracing"
)

type Config struct {
	InfluxDB          influxdb.Config `json:"influxdb"`
	IPDBFile          string          `json:"ipdb-file"`
	HTTPBindAddress   string          `json:"http-bind-address"`
	MirrorZDDirectory string          `json:"mirrorz-d-directory"`
	Homepage          string          `json:"homepage"`
	DomainLength      int             `json:"domain-length"`
	CacheTime         int             `json:"cache-time"`
	MaxRepoStaleness  int             `json:"max-repo-staleness"`
	LogDirectory      string          `json:"log-directory"`
}

const DefaultMaxRepoStaleness = 48 * 60 * 60

type Server struct {
	// feature providers
	resolved *caching.ResolveCache
	mirrorzd *mirrorzdb.MirrorZDatabase
	influx   *influxdb.Source
	meta     *requestmeta.Parser

	// saved config
	logDir           string
	mirrorzdDir      string
	homepage         string
	cacheTime        int
	maxRepoStaleness int

	// http muxes
	handler, apiHandler http.Handler

	// loggers
	resolveLogger, failLogger, errorLogger loggo.Logger
}

const ApiPrefix = requestmeta.ApiPrefix

func NewServer(config Config) *Server {
	if config.MaxRepoStaleness <= 0 {
		config.MaxRepoStaleness = DefaultMaxRepoStaleness
	}
	s := &Server{
		resolved: caching.NewResolveCache(time.Duration(config.CacheTime) * time.Second),
		mirrorzd: mirrorzdb.NewMirrorZDatabase(),
		influx:   influxdb.NewSourceFromConfig(config.InfluxDB),
		meta: &requestmeta.Parser{
			DomainLength: config.DomainLength,
		},

		logDir:      config.LogDirectory,
		mirrorzdDir: config.MirrorZDDirectory,

		resolveLogger: logging.GetLogger("resolve"),
		failLogger:    logging.GetLogger("fail"),
		errorLogger:   logging.GetLogger("error"),

		homepage:         config.Homepage,
		cacheTime:        config.CacheTime,
		maxRepoStaleness: config.MaxRepoStaleness,
	}
	s.buildHandlers()
	return s
}

var logContexts = []string{"resolve", "fail", "gc", "ipip", "parser", "error"}

func (s *Server) InitLoggers() error {
	defer runtime.GC() // trigger finalizers on released *os.File's
	for _, context := range logContexts {
		err := logging.SetContextFile(context, filepath.Join(s.logDir, context+".log"))
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) LoadMirrorZD() error {
	return s.mirrorzd.Load(s.mirrorzdDir)
}

func (s *Server) buildHandlers() {
	apiMux := http.NewServeMux()
	prefix := ApiPrefix + "scoring"
	apiMux.Handle(prefix, http.StripPrefix(prefix, http.HandlerFunc(s.handleScoringAPI)))
	apiMux.Handle(prefix+"/", http.StripPrefix(prefix, http.HandlerFunc(s.handleScoringAPI)))
	aptMirrorlistPrefix := ApiPrefix + "apt/mirrorlist/"
	apiMux.Handle(aptMirrorlistPrefix, http.StripPrefix(aptMirrorlistPrefix,
		http.HandlerFunc(s.handleAPTMirrorlist)))
	rpmMirrorlistPrefix := ApiPrefix + "rpm/mirrorlist/"
	apiMux.Handle(rpmMirrorlistPrefix, http.StripPrefix(rpmMirrorlistPrefix,
		http.HandlerFunc(s.handleRPMMirrorlist)))
	s.apiHandler = apiMux

	mainMux := http.NewServeMux()
	mainMux.HandleFunc("/", s.handleRedirect)
	mainMux.Handle(ApiPrefix, apiMux)
	s.handler = mainMux
}

func (s *Server) handleAPTMirrorlist(w http.ResponseWriter, r *http.Request) {
	s.handleMirrorlist(w, r, true)
}

func (s *Server) handleRPMMirrorlist(w http.ResponseWriter, r *http.Request) {
	s.handleMirrorlist(w, r, false)
}

func (s *Server) handleMirrorlist(w http.ResponseWriter, r *http.Request, apt bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, fmt.Sprintf("Method %s is not supported", r.Method), http.StatusMethodNotAllowed)
		return
	}

	meta := s.meta.Parse(r)
	if meta.CName == "" || (apt && meta.Tail != "") {
		http.NotFound(w, r)
		return
	}
	tail := ""
	if !apt {
		var ok bool
		tail, ok = cleanMirrorlistTail(meta.Tail)
		if !ok {
			http.Error(w, "Invalid repository path", http.StatusBadRequest)
			return
		}
	}

	ctx := context.WithValue(r.Context(), tracing.Key, tracing.NewTracer(false))
	urls, err := s.resolveCandidates(ctx, meta)
	if err != nil {
		http.Error(w, "Mirror information is temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	urls = appendMirrorlistTail(urls, tail)
	if len(urls) == 0 {
		http.NotFound(w, r)
		return
	}

	var body strings.Builder
	for i, url := range urls {
		if apt {
			fmt.Fprintf(&body, "%s\tpriority:%d\n", url, i+1)
		} else {
			fmt.Fprintln(&body, url)
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", fmt.Sprintf("private, max-age=%d", max(s.cacheTime, 0)))
	w.Header().Set("Vary", "X-Real-IP, X-Forwarded-Proto, X-Forwarded-Host")
	w.Header().Set("Content-Length", strconv.Itoa(body.Len()))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		if _, err := w.Write([]byte(body.String())); err != nil {
			s.errorLogger.Errorf("Error writing mirrorlist response: %v", err)
		}
	}
}

func cleanMirrorlistTail(tail string) (string, bool) {
	if tail == "" || tail == "/" {
		return "", true
	}
	if !strings.HasPrefix(tail, "/") {
		return "", false
	}

	raw := strings.TrimPrefix(tail, "/")
	raw = strings.TrimSuffix(raw, "/")
	if raw == "" {
		return "", true
	}

	parts := strings.Split(raw, "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Contains(part, `\`) ||
			strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return "", false
		}
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/"), true
}

func appendMirrorlistTail(urls []string, tail string) []string {
	if tail == "" {
		return urls
	}

	withTail := make([]string, len(urls))
	for i, base := range urls {
		withTail[i] = strings.TrimSuffix(base, "/") + "/" + tail + "/"
	}
	return withTail
}

// ServeHTTP implements the http.Handler interface.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// handleRedirect handles a regular mirrorz-302 request.
func (s *Server) handleRedirect(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		labels := s.meta.Labels(r)
		scheme := s.meta.Scheme(r)
		if len(labels) != 0 {
			resolve, ok := s.mirrorzd.ResolveLabel(labels[len(labels)-1])
			if ok {
				http.Redirect(w, r, fmt.Sprintf("%s://%s", scheme, resolve), http.StatusFound)
				return
			}
		}
		http.Redirect(w, r, fmt.Sprintf("%s://%s", scheme, s.homepage), http.StatusFound)
		return
	}

	_, traceEnabled := r.URL.Query()["trace"]
	tracer := tracing.NewTracer(traceEnabled)
	ctx := context.WithValue(r.Context(), tracing.Key, tracer)
	meta := s.meta.Parse(r)
	url, err := s.Resolve(ctx, meta)

	if traceEnabled {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		tracer.WriteTo(w)
	} else if url == "" || err != nil {
		http.NotFound(w, r)
	} else {
		query := ""
		if r.URL.RawQuery != "" {
			query = "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, fmt.Sprintf("%s%s%s", url, meta.Tail, query), http.StatusFound)
	}
}

type ScoringAPIResponse struct {
	Scores scoring.Scores `json:"scores"`
}

func (s *Server) handleScoringAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, fmt.Sprintf("Method %s is not supported", r.Method), http.StatusMethodNotAllowed)
		return
	}
	meta := s.meta.Parse(r)

	ctx := context.WithValue(r.Context(), tracing.Key, tracing.NewTracer(false))
	scores := s.ResolveBest(ctx, meta)
	resp := &ScoringAPIResponse{Scores: scores}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.errorLogger.Errorf("Error encoding response: %v", err)
	}
}
