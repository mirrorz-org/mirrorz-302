package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/juju/loggo"
	"github.com/mirrorz-org/mirrorz-302/pkg/cacher"
	"github.com/mirrorz-org/mirrorz-302/pkg/influxdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/mirrorzdb"
	"github.com/mirrorz-org/mirrorz-302/pkg/requestmeta"
	"github.com/mirrorz-org/mirrorz-302/pkg/scoring"
	"github.com/mirrorz-org/mirrorz-302/pkg/trace"
)

type Config struct {
	InfluxDB          influxdb.Config `json:"influxdb"`
	IPASNURL          string          `json:"ipasn-url"`
	HTTPBindAddress   string          `json:"http-bind-address"`
	MirrorZDDirectory string          `json:"mirrorz-d-directory"`
	Homepage          string          `json:"homepage"`
	DomainLength      int             `json:"domain-length"`
	CacheTime         int64           `json:"cache-time"`
	LogDirectory      string          `json:"log-directory"`
}

var (
	logger   = loggo.GetLogger("<root>")
	mirrorzd = mirrorzdb.NewMirrorZDatabase()
)

func LoadConfig(path string) (config Config, err error) {
	file, err := os.ReadFile(path)
	if err != nil {
		logger.Errorf("LoadConfig ReadFile failed: %v\n", err)
		return
	}
	err = json.Unmarshal(file, &config)
	if err != nil {
		logger.Errorf("LoadConfig json Unmarshal failed: %v\n", err)
		return
	}
	logger.Debugf("LoadConfig InfluxDB URL: %s\n", config.InfluxDB.URL)
	logger.Debugf("LoadConfig InfluxDB Org: %s\n", config.InfluxDB.Org)
	logger.Debugf("LoadConfig InfluxDB Bucket: %s\n", config.InfluxDB.Bucket)
	logger.Debugf("LoadConfig IPASN URL: %s\n", config.IPASNURL)
	logger.Debugf("LoadConfig HTTP Bind Address: %s\n", config.HTTPBindAddress)
	logger.Debugf("LoadConfig MirrorZ D Directory: %s\n", config.MirrorZDDirectory)
	logger.Debugf("LoadConfig Homepage: %s\n", config.Homepage)
	logger.Debugf("LoadConfig Domain Length: %d\n", config.DomainLength)
	logger.Debugf("LoadConfig Cache Time: %d\n", config.CacheTime)
	logger.Debugf("LoadConfig Log Directory: %s\n", config.LogDirectory)
	return
}

func (s *MirrorZ302Server) Resolve(r *http.Request, cname string) (url string, err error) {
	ctx := r.Context()
	tracer := ctx.Value(trace.Key).(trace.Tracer)
	traceFunc := tracer.Printf

	meta := s.meta.Parse(r)
	traceFunc("Labels: %v\n", meta.Labels)
	traceFunc("IP: %v\n", meta.IP)
	traceFunc("ASN: %s\n", meta.ASN)
	traceFunc("Scheme: %s\n", meta.Scheme)

	logFunc := func(url string, score scoring.Score, char string) {
		if url != "" {
			// record detail in resolve log
			s.resolveLogger.Debugf("%s", tracer.String())
			resolvedLog := fmt.Sprintf("%s: %s (%v, %s) %v %s\n",
				char, url, meta.IP, meta.ASN, meta.Labels,
				score.LogString())
			s.resolveLogger.Infof("%s", resolvedLog)
			traceFunc("%s", resolvedLog)
		} else {
			// record detail in fail log
			s.failLogger.Debugf("%s", tracer.String())
			failLog := fmt.Sprintf("F: %s (%v, %s) %v\n", cname, meta.IP, meta.ASN, meta.Labels)
			s.failLogger.Infof("%s", failLog)
			traceFunc("%s", failLog)
		}
	}

	// check if already resolved / cached
	key := requestmeta.CacheKey(meta, cname)
	keyResolved, cacheHit := s.resolved.Load(key)

	// all valid, use cached result
	if cacheHit && s.resolved.IsFresh(keyResolved) {
		// update timestamp
		s.resolved.Store(key, keyResolved)
		url = keyResolved.Url
		logFunc(url, scoring.Score{}, "C") // C for cache
		return
	}

	res, err := s.influx.Query(ctx, cname)
	if err != nil {
		logger.Errorf("Resolve query: %v\n", err)
		return
	}

	var resolve, repo string

	if cacheHit && s.resolved.IsStale(keyResolved) {
		resolve, repo = ResolveExist(ctx, res, keyResolved.Resolve)
	}

	var chosenScore scoring.Score
	if resolve == "" && repo == "" {
		// ResolveExist failed
		chosenScore = ResolveBest(ctx, res, meta)
		resolve = chosenScore.Resolve
		repo = chosenScore.Repo
	}

	if resolve == "" && repo == "" {
		url = ""
	} else if strings.HasPrefix(repo, "http://") || strings.HasPrefix(repo, "https://") {
		url = repo
	} else {
		url = fmt.Sprintf("%s://%s%s", meta.Scheme, resolve, repo)
	}
	s.resolved.Store(key, cacher.Resolved{
		Url:     url,
		Resolve: resolve,
	})
	logFunc(url, chosenScore, "R") // R for resolve
	return
}

// ResolveBest tries to find the best mirror for the given request
func ResolveBest(ctx context.Context, res influxdb.Result, meta requestmeta.RequestMeta) (chosenScore scoring.Score) {
	tracer := ctx.Value(trace.Key).(trace.Tracer)
	traceFunc := tracer.Printf

	var scores scoring.Scores

	for res.Next() {
		record := res.Record()
		abbr := record.ValueByKey("mirror").(string)
		traceFunc("abbr: %s\n", abbr)
		endpoints, ok := mirrorzd.Lookup(abbr)
		if !ok {
			continue
		}
		var scoresEndpoints scoring.Scores
		for _, endpoint := range endpoints {
			traceFunc("  endpoint: %s %s\n", endpoint.Resolve, endpoint.Label)
			if reason, ok := endpoint.Match(meta); !ok {
				traceFunc("    %s\n", reason)
				continue
			}
			score := endpoint.Score(meta)
			score.Delta = int(record.Value().(int64))
			score.Repo = record.ValueByKey("path").(string)
			traceFunc("    score: %v\n", score)

			//if score.Delta < -60*60*24*3 { // 3 days
			//    traceFunc("    not up-to-date enough\n")
			//    continue
			//}
			if !endpoint.Public && score.Mask == 0 && score.AS == 0 {
				traceFunc("    not hit private\n")
				continue
			}
			scoresEndpoints = append(scoresEndpoints, score)
		}

		if len(scoresEndpoints) == 0 {
			traceFunc("  no score found\n")
			continue
		}

		// Find the not-dominated scores, or the first one
		optimalScores := scoresEndpoints.OptimalsExceptDelta() // Delta all the same
		if len(optimalScores) > 0 && len(optimalScores) != len(scoresEndpoints) {
			for index, score := range optimalScores {
				traceFunc("  optimal scores: %d %v\n", index, score)
				scores = append(scores, score)
			}
		} else {
			traceFunc("  first score: %v\n", scoresEndpoints[0])
			scores = append(scores, scoresEndpoints[0])
		}
	}
	if err := res.Err(); err != nil {
		logger.Errorf("Resolve query parsing error: %v\n", err)
		return
	}
	if len(scores) == 0 {
		return
	}

	for index, score := range scores {
		traceFunc("scores: %d %v\n", index, score)
	}
	optimalScores := scores.Optimals()
	if len(optimalScores) == 0 {
		logger.Warningf("Resolve optimal scores empty, algorithm implemented wrong\n")
		chosenScore = scores[0]
		return
	}

	allDelta := scores.AllDelta()
	allEqualExceptDelta := optimalScores.AllEqualExceptDelta()
	if allEqualExceptDelta || allDelta {
		var candidateScores scoring.Scores
		if allDelta {
			// Note: allDelta == true implies allEqualExceptDelta == true
			candidateScores = scores
		} else {
			candidateScores = optimalScores
		}
		// randomly choose one mirror from the optimal half
		// when len(optimalScores) == 1, randomHalf always succeeds
		sort.Sort(candidateScores)
		chosenScore = candidateScores.RandomHalf()
		for index, score := range candidateScores {
			traceFunc("sorted delta scores: %d %v\n", index, score)
		}
	} else {
		sort.Sort(optimalScores)
		chosenScore = optimalScores[0]
		// randomly choose one mirror not dominated by others
		//chosenScore = optimalScores.Random()
		for index, score := range optimalScores {
			traceFunc("optimal scores: %d %v\n", index, score)
		}
	}
	return
}

// ResolveExist returns the site and repo if the previous resolved mirror is still valid
func ResolveExist(ctx context.Context, res influxdb.Result, oldResolve string) (resolve string, repo string) {
	tracer := ctx.Value(trace.Key).(trace.Tracer)
	traceFunc := tracer.Printf

outerLoop:
	for res.Next() {
		record := res.Record()
		abbr := record.ValueByKey("mirror").(string)
		traceFunc("abbr: %s\n", abbr)
		endpoints, ok := mirrorzd.Lookup(abbr)
		if !ok {
			continue
		}
		for _, endpoint := range endpoints {
			traceFunc("  endpoint: %s %s\n", endpoint.Resolve, endpoint.Label)

			if oldResolve == endpoint.Resolve {
				resolve = endpoint.Resolve
				repo = record.ValueByKey("path").(string)
				traceFunc("exist\n")
				break outerLoop
			}
		}
	}
	return
}

func (s *MirrorZ302Server) CachePurge() {
	s.resolved.Clear()
}

func (s *MirrorZ302Server) StartResolvedTicker() {
	s.resolved.StartGCTicker(s.cacheGCLogger)
}

func main() {
	//lint:ignore SA1019 we don't care
	rand.Seed(time.Now().Unix())

	configPtr := flag.String("config", "config.json", "path to config file")
	debugPtr := flag.Bool("debug", false, "debug mode")
	flag.Parse()

	if *debugPtr {
		loggo.ConfigureLoggers("<root>=DEBUG")
	} else {
		loggo.ConfigureLoggers("<root>=INFO")
	}

	config, err := LoadConfig(*configPtr)
	if err != nil {
		logger.Errorf("Cannot open config file: %v\n", err)
		os.Exit(1)
	}

	mirrorzd.Load(config.MirrorZDDirectory)

	server := NewMirrorZ302Server(config)

	// Logfile (or its directory) must be unprivilegd
	err = server.InitLoggers()
	if err != nil {
		logger.Errorf("Cannot open log file: %v\n", err)
		os.Exit(1)
	}

	//defer server.influx.Close()

	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGWINCH)
	go func() {
		for sig := range signalChannel {
			switch sig {
			case syscall.SIGHUP:
				logger.Infof("Got A HUP Signal! Now Reloading mirrorz.d.json....\n")
				mirrorzd.Load(config.MirrorZDDirectory)
			case syscall.SIGUSR1:
				logger.Infof("Got A USR1 Signal! Now Reloading config.json....\n")
				LoadConfig(*configPtr)
			case syscall.SIGUSR2:
				logger.Infof("Got A USR2 Signal! Now Reopen log file....\n")
				err := server.InitLoggers()
				if err != nil {
					logger.Errorf("Can not open log file\n")
				}
			case syscall.SIGWINCH:
				logger.Infof("Got A WINCH Signal! Now Flush Resolved....\n")
				server.CachePurge()
			}
		}
	}()

	server.StartResolvedTicker()

	http.Handle("/", server)
	logger.Errorf("HTTP Server error: %v\n", http.ListenAndServe(config.HTTPBindAddress, nil))
}
