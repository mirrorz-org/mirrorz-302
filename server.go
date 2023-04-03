package main

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
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/mirrorz-org/mirrorz-302/pkg/trace"
)

type MirrorZ302Server struct {
	resolved *cacher.ResolveCache
	influx   *influxdb.Source
	meta     *requestmeta.Parser

	logDirectory string

	resolveLogger, failLogger loggo.Logger

	Homepage string
}

func NewMirrorZ302Server(config Config) *MirrorZ302Server {
	s := &MirrorZ302Server{
		resolved: cacher.NewResolveCache(config.CacheTime),
		influx:   influxdb.NewSourceFromConfig(config.InfluxDB),
		meta: &requestmeta.Parser{
			DomainLength: config.DomainLength,
		},

		resolveLogger: logging.GetLogger("resolve"),
		failLogger:    logging.GetLogger("fail"),

		Homepage: config.Homepage,
	}
	return s
}

var logContexts = []string{"resolve", "fail", "gc", "ipip", "parser"}

func (s *MirrorZ302Server) InitLoggers() error {
	defer runtime.GC() // trigger finalizers on released *os.File's
	dir := s.logDirectory
	for _, context := range logContexts {
		err := logging.SetContextFile(context, filepath.Join(dir, context+".log"))
		if err != nil {
			return err
		}
	}
	return nil
}

// ServeHTTP implements the http.Handler interface.
func (s *MirrorZ302Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Remove leading '/'
	pathParts := strings.SplitN(r.URL.Path[1:], "/", 2)

	if r.URL.Path == "/" {
		labels := s.meta.Labels(r)
		scheme := s.meta.Scheme(r)
		if len(labels) != 0 {
			resolve, ok := mirrorzd.ResolveLabel(labels[len(labels)-1])
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
