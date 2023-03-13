package main

import (
	"fmt"
	"math/rand"
	"net"
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

func (s *MirrorZ302Server) Resolve(r *http.Request, cname string, trace bool) (url string, traceStr string, err error) {
	traceBuilder := new(strings.Builder)
	defer func() {
		traceStr = traceBuilder.String()
	}()
	traceFunc := func(s string) {
		if trace {
			traceBuilder.WriteString(s)
		}
	}

	labels := Host(r)
	remoteIP := IP(r)
	asn := ASN(remoteIP)
	scheme := Scheme(r)
	traceFunc(fmt.Sprintf("labels: %v\n", labels))
	traceFunc(fmt.Sprintf("IP: %v\n", remoteIP))
	traceFunc(fmt.Sprintf("ASN: %s\n", asn))
	traceFunc(fmt.Sprintf("Scheme: %s\n", scheme))

	logFunc := func(url string, score Score, char string) {
		if url != "" {
			// record detail in resolve log
			s.resolveLogger.Debugf(traceStr)
			scoreLog := fmt.Sprintf("%d %d %d %d", score.pos, score.mask, score.as, score.delta)
			resolvedLog := fmt.Sprintf("%s: %s (%v, %s) %v %s\n", char, url, remoteIP, asn, labels, scoreLog)
			s.resolveLogger.Infof(resolvedLog)
			traceFunc(resolvedLog)
		} else {
			// record detail in fail log
			s.failLogger.Debugf(traceStr)
			failLog := fmt.Sprintf("F: %s (%v, %s) %v\n", cname, remoteIP, asn, labels)
			s.failLogger.Infof(failLog)
			traceFunc(failLog)
		}
	}

	// check if already resolved / cached
	key := strings.Join([]string{
		remoteIP.String(),
		cname,
		asn,
		scheme,
		strings.Join(labels, "-"),
	}, "+")
	keyResolved, ok := s.resolved.Load(key)

	// all valid, use cached result
	cur := time.Now().Unix()
	if ok && cur-keyResolved.last < config.CacheTime &&
		cur-keyResolved.start < config.CacheTime {
		url = keyResolved.url
		// update timestamp
		s.resolved.Store(key, Resolved{
			start:   keyResolved.start,
			last:    cur,
			url:     url,
			resolve: keyResolved.resolve,
		})
		logFunc(url, Score{}, "C") // C for cache
		return
	}

	res, err := QueryInflux(r.Context(), cname)
	if err != nil {
		logger.Errorf("Resolve query: %v\n", err)
		return
	}

	var resolve string
	var repo string

	if ok && cur-keyResolved.last < config.CacheTime &&
		cur-keyResolved.start >= config.CacheTime {
		resolve, repo = ResolveExist(res, traceBuilder, trace, keyResolved.resolve)
	}

	var chosenScore Score
	if resolve == "" && repo == "" {
		// the above IF does not hold or resolveNotExist
		chosenScore = ResolveBest(res, traceBuilder, trace, labels, remoteIP, asn, scheme)
		resolve = chosenScore.resolve
		repo = chosenScore.repo
	}

	if resolve == "" && repo == "" {
		url = ""
	} else if strings.HasPrefix(repo, "http://") || strings.HasPrefix(repo, "https://") {
		url = repo
	} else {
		url = fmt.Sprintf("%s://%s%s", scheme, resolve, repo)
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

func ResolveBest(res *api.QueryTableResult, traceBuilder *strings.Builder, trace bool,
	labels []string, remoteIP net.IP, asn string, scheme string) (chosenScore Score) {
	traceFunc := func(s string) {
		if trace {
			traceBuilder.WriteString(s)
		}
	}

	var scores Scores
	remoteIPv4 := remoteIP.To4() != nil

	for res.Next() {
		record := res.Record()
		abbr := record.ValueByKey("mirror").(string)
		traceFunc(fmt.Sprintf("abbr: %s\n", abbr))
		endpoints, ok := LookupMirrorZD(abbr)
		if !ok {
			continue
		}
		var scoresEndpoints Scores
		for _, endpoint := range endpoints {
			traceFunc(fmt.Sprintf("  endpoint: %s %s\n", endpoint.Resolve, endpoint.Label))
			if remoteIPv4 && !endpoint.Filter.V4 {
				traceFunc(fmt.Sprintf("    not v4 endpoint\n"))
				continue
			}
			if !remoteIPv4 && !endpoint.Filter.V6 {
				traceFunc(fmt.Sprintf("    not v6 endpoint\n"))
				continue
			}
			if scheme == "http" && !endpoint.Filter.NOSSL {
				traceFunc(fmt.Sprintf("    not nossl endpoint\n"))
				continue
			}
			if scheme == "https" && !endpoint.Filter.SSL {
				traceFunc(fmt.Sprintf("    not ssl endpoint\n"))
				continue
			}
			if (len(labels) != 0 && labels[len(labels)-1] == "4") && !endpoint.Filter.V4Only {
				traceFunc(fmt.Sprintf("    label v4only but endpoint not v4only\n"))
				continue
			}
			if (len(labels) != 0 && labels[len(labels)-1] == "6") && !endpoint.Filter.V6Only {
				traceFunc(fmt.Sprintf("    label v6only but endpoint not v6only\n"))
				continue
			}
			score := Score{pos: 0, as: 0, mask: 0, delta: 0}
			score.delta = int(record.Value().(int64))
			for index, label := range labels {
				if label == endpoint.Label {
					score.pos = index + 1
				}
			}
			for _, endpointASN := range endpoint.RangeASN {
				if endpointASN == asn {
					score.as = 1
				}
			}
			for _, ipnet := range endpoint.RangeCIDR {
				if remoteIP != nil && ipnet.Contains(remoteIP) {
					mask, _ := ipnet.Mask.Size()
					if mask > score.mask {
						score.mask = mask
					}
				}
			}

			score.resolve = endpoint.Resolve
			score.repo = record.ValueByKey("path").(string)
			traceFunc(fmt.Sprintf("    score: %v\n", score))

			//if score.delta < -60*60*24*3 { // 3 days
			//    traceFunc(fmt.Sprintf("    not up-to-date enough\n"))
			//    continue
			//}
			if !endpoint.Public && score.mask == 0 && score.as == 0 {
				traceFunc(fmt.Sprintf("    not hit private\n"))
				continue
			}
			scoresEndpoints = append(scoresEndpoints, score)
		}

		// Find the not-dominated scores, or the first one
		if len(scoresEndpoints) > 0 {
			optimalScores := scoresEndpoints.OptimalsExceptDelta() // Delta all the same
			if len(optimalScores) > 0 && len(optimalScores) != len(scoresEndpoints) {
				for index, score := range optimalScores {
					traceFunc(fmt.Sprintf("  optimal scores: %d %v\n", index, score))
					scores = append(scores, score)
				}
			} else {
				traceFunc(fmt.Sprintf("  first score: %v\n", scoresEndpoints[0]))
				scores = append(scores, scoresEndpoints[0])
			}
		} else {
			traceFunc(fmt.Sprintf("  no score found\n"))
		}
	}
	if err := res.Err(); err != nil {
		logger.Errorf("Resolve query parsing error: %v\n", err)
		return
	}

	if len(scores) > 0 {
		for index, score := range scores {
			traceFunc(fmt.Sprintf("scores: %d %v\n", index, score))
		}
		optimalScores := scores.Optimals()
		if len(optimalScores) == 0 {
			logger.Warningf("Resolve optimal scores empty, algorithm implemented error")
			chosenScore = scores[0]
		} else {
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
				// when len(optimalScores) == 1, randomHalf always success
				sort.Sort(candidateScores)
				chosenScore = candidateScores.RandomHalf()
				for index, score := range candidateScores {
					traceFunc(fmt.Sprintf("sorted delta scores: %d %v\n", index, score))
				}
			} else {
				sort.Sort(optimalScores)
				chosenScore = optimalScores[0]
				// randomly choose one mirror not dominated by others
				//chosenScore = optimalScores.Random()
				for index, score := range optimalScores {
					traceFunc(fmt.Sprintf("optimal scores: %d %v\n", index, score))
				}
			}
		}
	}
	return
}

func ResolveExist(res *api.QueryTableResult, traceBuilder *strings.Builder, trace bool,
	oldResolve string) (resolve string, repo string) {
	traceFunc := func(s string) {
		if trace {
			traceBuilder.WriteString(s)
		}
	}

	found := false

	for res.Next() {
		record := res.Record()
		abbr := record.ValueByKey("mirror").(string)
		traceFunc(fmt.Sprintf("abbr: %s\n", abbr))
		endpoints, ok := LookupMirrorZD(abbr)
		if !ok {
			continue
		}
		for _, endpoint := range endpoints {
			traceFunc(fmt.Sprintf("  endpoint: %s %s\n", endpoint.Resolve, endpoint.Label))

			if oldResolve == endpoint.Resolve {
				resolve = endpoint.Resolve
				repo = record.ValueByKey("path").(string)
				found = true
				traceFunc("exist\n")
			}
			if found {
				break
			}
		}
		if found {
			break
		}
	}
	return
}

func (s *MirrorZ302Server) ResolvedTicker(c <-chan time.Time) {
	for t := range c {
		s.cacheGCLogger.Infof("Resolved GC starts\n")
		s.resolved.GC(t)
		s.cacheGCLogger.Infof("Resolved GC finished\n")
	}
}
func (s *MirrorZ302Server) StartResolvedTicker() {
	s.ResolvedInit()
	// GC on resolved
	ticker := time.NewTicker(time.Second * time.Duration(config.CacheTime))
	go s.ResolvedTicker(ticker.C)
}

func main() {
	rand.Seed(time.Now().Unix())

	configPtr := flag.String("config", "config.json", "path to config file")
	debugPtr := flag.Bool("debug", false, "debug mode")
	flag.Parse()

	server := NewMirrorZ302Server()

	config, err := LoadConfig(*configPtr, *debugPtr)
	if err != nil {
		logger.Errorf("Can not open config file: %v\n", err)
		os.Exit(1)
	}

	// Logfile (or its directory) must be unprivilegd
	err = server.InitLoggers()
	if err != nil {
		logger.Errorf("Can not open log file: %v\n", err)
		os.Exit(1)
	}

	OpenInfluxDB()

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
				server.ResolvedInit()
			}
		}
	}()

	server.StartResolvedTicker()

	http.Handle("/", server)
	logger.Errorf("HTTP Server error: %v\n", http.ListenAndServe(config.HTTPBindAddress, nil))

	CloseInfluxDB()
}
