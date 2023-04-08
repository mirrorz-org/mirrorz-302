package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"

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
	LogDirectory      string          `json:"log-directory"`
}

type Server struct {
	// feature providers
	resolved *caching.ResolveCache
	mirrorzd *mirrorzdb.MirrorZDatabase
	influx   *influxdb.Source
	meta     *requestmeta.Parser

	// saved config
	logDir      string
	mirrorzdDir string
	homepage    string

	// http muxes
	handler, apiHandler http.Handler

	// loggers
	resolveLogger, failLogger, errorLogger loggo.Logger
}

const ApiPrefix = requestmeta.ApiPrefix

func NewServer(config Config) *Server {
	s := &Server{
		resolved: caching.NewResolveCache(config.CacheTime),
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

		homepage: config.Homepage,
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
	apiMux.HandleFunc(ApiPrefix+"scoring/", s.handleScoringAPI)
	s.apiHandler = apiMux

	mainMux := http.NewServeMux()
	mainMux.HandleFunc("/", s.handleRedirect)
	mainMux.Handle(ApiPrefix, apiMux)
	s.handler = mainMux
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
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	resp := new(ScoringAPIResponse)
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.errorLogger.Errorf("Error encoding response: %v", err)
	}
}
