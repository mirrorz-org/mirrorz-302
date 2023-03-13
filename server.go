package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/juju/loggo"
)

type MirrorZ302Server struct {
	resolveLogger Logger
	failLogger    Logger
	cacheGCLogger Logger

	resolved *ResolveCache
}

func NewMirrorZ302Server() *MirrorZ302Server {
	s := &MirrorZ302Server{}
	s.resolved = NewResolveCache(0, &s.cacheGCLogger)
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
	// [1:] for no heading `/`
	pathArr := strings.SplitN(r.URL.Path[1:], "/", 2)

	cname := ""
	tail := ""
	if r.URL.Path == "/" {
		labels := Host(r)
		scheme := Scheme(r)
		if len(labels) != 0 {
			resolve, ok := LabelToResolve.Load(labels[len(labels)-1])
			if ok {
				http.Redirect(w, r, fmt.Sprintf("%s://%s", scheme, resolve), http.StatusFound)
				return
			}
		}
		http.Redirect(w, r, fmt.Sprintf("%s://%s", scheme, config.Homepage), http.StatusFound)
		return
	}
	cname = pathArr[0]
	if len(pathArr) == 2 {
		tail = "/" + pathArr[1]
	}

	_, trace := r.URL.Query()["trace"]

	url, traceStr, err := s.Resolve(r, cname, trace)

	if trace {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%s", traceStr)
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
