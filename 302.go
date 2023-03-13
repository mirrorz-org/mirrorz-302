package main

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/influxdata/influxdb-client-go/v2/api"

	"encoding/json"

	"flag"

	"github.com/juju/loggo"

	"sort"

	"os"
	"os/signal"
	"syscall"
)

type Config struct {
	InfluxDBURL       string `json:"influxdb-url"`
	InfluxDBToken     string `json:"influxdb-token"`
	InfluxDBBucket    string `json:"influxdb-bucket"`
	InfluxDBOrg       string `json:"influxdb-org"`
	IPASNURL          string `json:"ipasn-url"`
	HTTPBindAddress   string `json:"http-bind-address"`
	MirrorZDDirectory string `json:"mirrorz-d-directory"`
	Homepage          string `json:"homepage"`
	DomainLength      int    `json:"domain-length"`
	CacheTime         int64  `json:"cache-time"`
	LogDirectory      string `json:"log-directory"`
}

var (
	logger = loggo.GetLogger("mirrorzd") // to stderr
	config Config
)

func LoadConfig(path string, debug bool) (config Config, err error) {
	if debug {
		loggo.ConfigureLoggers("mirrorzd=DEBUG")
	} else {
		loggo.ConfigureLoggers("mirrorzd=INFO")
	}

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
	if config.InfluxDBToken == "" {
		logger.Errorf("LoadConfig find no InfluxDBToken in file\n")
		return
	}
	if config.InfluxDBURL == "" {
		config.InfluxDBURL = "http://localhost:8086"
	}
	if config.InfluxDBBucket == "" {
		config.InfluxDBBucket = "mirrorz"
	}
	if config.InfluxDBOrg == "" {
		config.InfluxDBOrg = "mirrorz"
	}
	if config.IPASNURL == "" {
		config.IPASNURL = "http://localhost:8889"
	}
	if config.HTTPBindAddress == "" {
		config.HTTPBindAddress = "localhost:8888"
	}
	if config.MirrorZDDirectory == "" {
		config.MirrorZDDirectory = "mirrorz.d"
	}
	if config.Homepage == "" {
		config.Homepage = "mirrorz.org"
	}
	if config.DomainLength == 0 {
		// 4 for *.mirrors.edu.cn
		// 4 for *.m.mirrorz.org
		// 5 for *.mirrors.cngi.edu.cn
		// 5 for *.mirrors.cernet.edu.cn
		config.DomainLength = 5
	}
	if config.CacheTime == 0 {
		config.CacheTime = 300
	}
	// If you changed LogDirectory via SIGUSR1, you should issue SIGUSR2 manually
	if config.LogDirectory == "" {
		config.LogDirectory = "/var/log/mirrorzd"
	}
	logger.Debugf("LoadConfig InfluxDB URL: %s\n", config.InfluxDBURL)
	logger.Debugf("LoadConfig InfluxDB Org: %s\n", config.InfluxDBOrg)
	logger.Debugf("LoadConfig InfluxDB Bucket: %s\n", config.InfluxDBBucket)
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
	tracer := ctx.Value(TracerKey).(Tracer)
	traceFunc := tracer.Printf

	meta := ParseRequestMeta(r)
	traceFunc("Labels: %v\n", meta.Labels)
	traceFunc("IP: %v\n", meta.IP)
	traceFunc("ASN: %s\n", meta.ASN)
	traceFunc("Scheme: %s\n", meta.Scheme)

	logFunc := func(url string, score Score, char string) {
		if url != "" {
			// record detail in resolve log
			s.resolveLogger.Debugf(tracer.String())
			resolvedLog := fmt.Sprintf("%s: %s (%v, %s) %v %s\n",
				char, url, meta.IP, meta.ASN, meta.Labels,
				score.LogString())
			s.resolveLogger.Infof(resolvedLog)
			traceFunc(resolvedLog)
		} else {
			// record detail in fail log
			s.failLogger.Debugf(tracer.String())
			failLog := fmt.Sprintf("F: %s (%v, %s) %v\n", cname, meta.IP, meta.ASN, meta.Labels)
			s.failLogger.Infof(failLog)
			traceFunc(failLog)
		}
	}

	// check if already resolved / cached
	key := CacheKey(meta, cname)
	keyResolved, cacheHit := s.resolved.Load(key)

	// all valid, use cached result
	cur := time.Now().Unix()
	if cacheHit && cur-keyResolved.last < config.CacheTime &&
		cur-keyResolved.start < config.CacheTime {
		// update timestamp
		keyResolved.last = cur
		s.resolved.Store(key, keyResolved)
		logFunc(url, Score{}, "C") // C for cache
		return
	}

	res, err := QueryInflux(ctx, cname)
	if err != nil {
		logger.Errorf("Resolve query: %v\n", err)
		return
	}

	var resolve string
	var repo string

	if cacheHit && cur-keyResolved.last < config.CacheTime &&
		cur-keyResolved.start >= config.CacheTime {
		resolve, repo = ResolveExist(ctx, res, keyResolved.resolve)
	}

	var chosenScore Score
	if resolve == "" && repo == "" {
		// the above IF does not hold or resolveNotExist
		chosenScore = ResolveBest(ctx, res, meta)
		resolve = chosenScore.resolve
		repo = chosenScore.repo
	}

	if resolve == "" && repo == "" {
		url = ""
	} else if strings.HasPrefix(repo, "http://") || strings.HasPrefix(repo, "https://") {
		url = repo
	} else {
		url = fmt.Sprintf("%s://%s%s", meta.Scheme, resolve, repo)
	}
	s.resolved.Store(key, Resolved{
		start:   cur,
		last:    cur,
		url:     url,
		resolve: resolve,
	})
	logFunc(url, chosenScore, "R") // R for resolve
	return
}

func ResolveBest(ctx context.Context, res *api.QueryTableResult,
	meta RequestMeta) (chosenScore Score) {
	tracer := ctx.Value(TracerKey).(Tracer)
	traceFunc := tracer.Printf

	var scores Scores

	for res.Next() {
		record := res.Record()
		abbr := record.ValueByKey("mirror").(string)
		traceFunc("abbr: %s\n", abbr)
		endpoints, ok := LookupMirrorZD(abbr)
		if !ok {
			continue
		}
		var scoresEndpoints Scores
		for _, endpoint := range endpoints {
			traceFunc("  endpoint: %s %s\n", endpoint.Resolve, endpoint.Label)
			if reason, ok := endpoint.Match(meta); !ok {
				traceFunc("    %s\n", reason)
				continue
			}
			score := endpoint.Score(meta)
			score.delta = int(record.Value().(int64))
			score.repo = record.ValueByKey("path").(string)
			traceFunc("    score: %v\n", score)

			//if score.delta < -60*60*24*3 { // 3 days
			//    traceFunc("    not up-to-date enough\n")
			//    continue
			//}
			if !endpoint.Public && score.mask == 0 && score.as == 0 {
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
		var candidateScores Scores
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

func ResolveExist(ctx context.Context, res *api.QueryTableResult,
	oldResolve string) (resolve string, repo string) {
	tracer := ctx.Value(TracerKey).(Tracer)
	traceFunc := tracer.Printf

outerLoop:
	for res.Next() {
		record := res.Record()
		abbr := record.ValueByKey("mirror").(string)
		traceFunc("abbr: %s\n", abbr)
		endpoints, ok := LookupMirrorZD(abbr)
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

func (s *MirrorZ302Server) resolvedTicker(c <-chan time.Time) {
	for t := range c {
		s.resolved.GC(t, &s.cacheGCLogger)
	}
}

func (s *MirrorZ302Server) StartResolvedTicker() {
	// GC on resolved
	ticker := time.NewTicker(time.Second * time.Duration(config.CacheTime))
	go s.resolvedTicker(ticker.C)
}

func main() {
	rand.Seed(time.Now().Unix())

	configPtr := flag.String("config", "config.json", "path to config file")
	debugPtr := flag.Bool("debug", false, "debug mode")
	flag.Parse()

	config, err := LoadConfig(*configPtr, *debugPtr)
	if err != nil {
		logger.Errorf("Can not open config file: %v\n", err)
		os.Exit(1)
	}

	server := NewMirrorZ302Server(config)

	// Logfile (or its directory) must be unprivilegd
	err = server.InitLoggers()
	if err != nil {
		logger.Errorf("Can not open log file: %v\n", err)
		os.Exit(1)
	}

	OpenInfluxDB()
	defer CloseInfluxDB()

	LoadMirrorZD(config.MirrorZDDirectory)

	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGWINCH)
	go func() {
		for sig := range signalChannel {
			switch sig {
			case syscall.SIGHUP:
				logger.Infof("Got A HUP Signal! Now Reloading mirrorz.d.json....\n")
				LoadMirrorZD(config.MirrorZDDirectory)
			case syscall.SIGUSR1:
				logger.Infof("Got A USR1 Signal! Now Reloading config.json....\n")
				LoadConfig(*configPtr, *debugPtr)
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
