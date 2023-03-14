package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/juju/loggo"
	"github.com/mirrorz-org/mirrorz-302/pkg/logging"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/mirrorz-org/mirrorz-302/pkg/trace"
)

type MirrorZ302Server struct {
	resolveLogger *logging.Logger
	failLogger    *logging.Logger
	cacheGCLogger *logging.Logger

	resolved *ResolveCache
	meta     *requestmeta.Parser
}

func NewMirrorZ302Server(config Config) *MirrorZ302Server {
	s := &MirrorZ302Server{
		resolveLogger: logging.NewLogger(
			filepath.Join(config.LogDirectory, "resolve.log"),
			loggo.INFO,
		),
		failLogger: logging.NewLogger(
			filepath.Join(config.LogDirectory, "fail.log"),
			loggo.INFO,
		),
		cacheGCLogger: logging.NewLogger(
			filepath.Join(config.LogDirectory, "gc.log"),
			loggo.INFO,
		),

		resolved: NewResolveCache(config.CacheTime),
	}

	s.meta = &requestmeta.Parser{
		IPASNURL:     config.IPASNURL,
		DomainLength: config.DomainLength,
		Logger:       s.resolveLogger,
	}
	return s
}

func (s *MirrorZ302Server) InitLoggers() (err error) {
	if err = s.resolveLogger.Open(); err != nil {
		return
	}
	if err = s.failLogger.Open(); err != nil {
		return
	}
	if err = s.cacheGCLogger.Open(); err != nil {
		return
	}
	return
}

// ServeHTTP implements the http.Handler interface.
func (s *MirrorZ302Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Remove leading '/'
	pathParts := strings.SplitN(r.URL.Path[1:], "/", 2)

	if r.URL.Path == "/" {
		labels := s.meta.Host(r)
		scheme := s.meta.Scheme(r)
		if len(labels) != 0 {
			resolve, ok := mirrorzd.ResolveLabel(labels[len(labels)-1])
			if ok {
				http.Redirect(w, r, fmt.Sprintf("%s://%s", scheme, resolve), http.StatusFound)
				return
			}
		}
		http.Redirect(w, r, fmt.Sprintf("%s://%s", scheme, config.Homepage), http.StatusFound)
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
