package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	InfluxDB               influxdb.Config `json:"influxdb"`
	IPDBFile               string          `json:"ipdb-file"`
	HTTPBindAddress        string          `json:"http-bind-address"`
	MirrorZConfigDirectory string          `json:"mirrorz-config-directory"`
	Homepage               string          `json:"homepage"`
	DomainLength           int             `json:"domain-length"`
	CacheTime              int             `json:"cache-time"`
	MaxRepoStaleness       int             `json:"max-repo-staleness"`
	LogDirectory           string          `json:"log-directory"`
}

const DefaultMaxRepoStaleness = 48 * 60 * 60

type Server struct {
	// Keep configuration reloads and cache invalidation atomic with resolution.
	configMu sync.RWMutex
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
		mirrorzdDir: config.MirrorZConfigDirectory,

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
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if err := s.mirrorzd.Load(s.mirrorzdDir); err != nil {
		return err
	}
	s.CachePurge()
	return nil
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
	apiMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "404 page not found")
	})
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
		writeError(w, r, http.StatusMethodNotAllowed, fmt.Sprintf("Method %s is not supported", r.Method))
		return
	}

	meta := s.meta.Parse(r)
	ctx := context.WithValue(r.Context(), tracing.Key, tracing.NewTracer(false))
	officialIndex := apt && r.URL.Query().Get("official_index") == "1"
	list, err := s.resolveMirrorlist(ctx, meta, apt, officialIndex)
	if err != nil {
		switch {
		case errors.Is(err, ErrOfficialIndexUnsupported):
			writeError(w, r, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrInvalidPath):
			writeError(w, r, http.StatusBadRequest, "Invalid repository path")
		case errors.Is(err, ErrMirrorlistNotFound):
			writeError(w, r, http.StatusNotFound, "404 page not found")
		default:
			writeError(w, r, http.StatusServiceUnavailable, "Mirror information is temporarily unavailable")
		}
		return
	}
	if len(list.URLs) == 0 {
		writeError(w, r, http.StatusNotFound, "No mirror sites are available for this repository")
		return
	}

	var body strings.Builder
	if list.OfficialURL != "" {
		fmt.Fprintf(&body, "%s\tpriority:0 type:index\n", list.OfficialURL)
	}
	for i, url := range list.URLs {
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

func (s *Server) redirectRoot(w http.ResponseWriter, r *http.Request) {
	host := s.homepage
	labels := s.meta.Labels(r)
	if len(labels) != 0 {
		if resolved, ok := s.mirrorzd.ResolveLabel(labels[len(labels)-1]); ok {
			host = resolved
		}
	}
	http.Redirect(w, r, fmt.Sprintf("%s://%s", s.meta.Scheme(r), host), http.StatusFound)
}

type redirectQuery struct {
	nth          int
	rawQuery     string
	traceEnabled bool
}

func parseRedirectQuery(u *url.URL) (redirectQuery, error) {
	query := redirectQuery{rawQuery: u.RawQuery}
	parts := strings.Split(u.RawQuery, "&")
	nthIndex := -1
	for i, part := range parts {
		params, err := url.ParseQuery(part)
		if err != nil {
			continue
		}
		query.traceEnabled = query.traceEnabled || params.Has("trace")
		if nthIndex == -1 && params.Has("nth") {
			n, err := strconv.ParseUint(params.Get("nth"), 10, strconv.IntSize-1)
			if err != nil {
				return redirectQuery{}, err
			}
			query.nth, nthIndex = int(n), i
		}
	}
	if nthIndex != -1 {
		// Remove the consumed pair without re-encoding the rest.
		query.rawQuery = strings.Join(slices.Delete(parts, nthIndex, nthIndex+1), "&")
	}
	return query, nil
}

// handleRedirect handles a regular mirrorz-302 request.
func (s *Server) handleRedirect(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		s.redirectRoot(w, r)
		return
	}

	query, err := parseRedirectQuery(r.URL)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid nth query")
		return
	}
	tracer := tracing.NewTracer(query.traceEnabled)
	ctx := context.WithValue(r.Context(), tracing.Key, tracer)
	meta := s.meta.Parse(r)
	url, err := s.Resolve(ctx, meta, query.nth)
	if errors.Is(err, ErrNthOutOfRange) {
		if query.nth != 0 {
			writeError(w, r, http.StatusUnprocessableEntity, ErrNthOutOfRange.Error())
			return
		}
		// No candidate at index zero: use the normal empty-target response.
		err = nil
	}
	if err != nil {
		tracer.Printf("error: %v\n", err)
	}

	if query.traceEnabled {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		tracer.WriteTo(w)
	} else if errors.Is(err, ErrInvalidPath) {
		writeError(w, r, http.StatusBadRequest, "Invalid repository path")
	} else if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, "Mirror information is temporarily unavailable")
	} else if url == "" {
		writeError(w, r, http.StatusNotFound, "No mirror sites are available for this repository")
	} else {
		if query.rawQuery != "" {
			url += "?" + query.rawQuery
		}
		http.Redirect(w, r, url, http.StatusFound)
	}
}

type ScoringAPIResponse struct {
	Scores scoring.Scores `json:"scores"`
}

func (s *Server) handleScoringAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		writeError(w, r, http.StatusMethodNotAllowed, fmt.Sprintf("Method %s is not supported", r.Method))
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

func acceptsJSON(values []string) bool {
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(item))
			if err != nil || mediaType != "application/json" {
				continue
			}
			if quality, ok := params["q"]; ok {
				q, err := strconv.ParseFloat(quality, 64)
				if err != nil || !(q > 0 && q <= 1) {
					continue
				}
			}
			return true
		}
	}
	return false
}

func writeError(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.Header().Add("Vary", "Accept")
	if !acceptsJSON(r.Header.Values("Accept")) {
		http.Error(w, message, status)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error string `json:"error"`
	}{Error: message})
}
