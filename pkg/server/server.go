package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/juju/loggo"
	"github.com/mirrorz-org/mirrorz-302/pkg/cacher"
	"github.com/mirrorz-org/mirrorz-302/pkg/influxdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/logging"
	"github.com/mirrorz-org/mirrorz-302/pkg/mirrorzdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/mirrorz-org/mirrorz-302/pkg/trace"
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
	resolved *cacher.ResolveCache
	mirrorzd *mirrorzdb.MirrorZDatabase
	influx   *influxdb.Source
	meta     *requestmeta.Parser

	logDir      string
	mirrorzdDir string

	resolveLogger, failLogger, errorLogger loggo.Logger

	Homepage string
}

func NewServer(config Config) *Server {
	s := &Server{
		resolved: cacher.NewResolveCache(config.CacheTime),
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

		Homepage: config.Homepage,
	}
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

// ServeHTTP implements the http.Handler interface.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Remove leading '/'
	pathParts := strings.SplitN(r.URL.Path[1:], "/", 2)

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
		http.Redirect(w, r, fmt.Sprintf("%s://%s", scheme, s.Homepage), http.StatusFound)
		return
	}

	cname := pathParts[0]
	tail := ""
	if len(pathParts) == 2 {
		tail = "/" + pathParts[1]
	}

	_, traceEnabled := r.URL.Query()["trace"]
	tracer := trace.NewTracer(traceEnabled)
	ctx := context.WithValue(r.Context(), trace.Key, tracer)
	r = r.WithContext(ctx)

	url, err := s.Resolve(r, cname)

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
		http.Redirect(w, r, fmt.Sprintf("%s%s%s", url, tail, query), http.StatusFound)
	}
}
