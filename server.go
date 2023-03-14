package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/juju/loggo"
	"github.com/mirrorz-org/mirrorz-302/pkg/trace"
)

type MirrorZ302Server struct {
	resolveLogger Logger
	failLogger    Logger
	cacheGCLogger Logger

	resolved *ResolveCache
}

func NewMirrorZ302Server(config Config) *MirrorZ302Server {
	s := &MirrorZ302Server{}
	s.resolved = NewResolveCache(config.CacheTime)
	return s
}

func (s *MirrorZ302Server) InitLoggers() (err error) {
	if err = s.resolveLogger.Open("resolve.log", loggo.INFO); err != nil {
		return
	}
	if err = s.failLogger.Open("fail.log", loggo.INFO); err != nil {
		return
	}
	if err = s.cacheGCLogger.Open("gc.log", loggo.INFO); err != nil {
		return
	}
	return
}

// ServeHTTP implements the http.Handler interface.
func (s *MirrorZ302Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Remove leading '/'
	pathParts := strings.SplitN(r.URL.Path[1:], "/", 2)

	if r.URL.Path == "/" {
		labels := Host(r)
		scheme := Scheme(r)
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
