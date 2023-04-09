package main

import (
	"encoding/json"
	"flag"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/juju/loggo"
	"github.com/mirrorz-org/mirrorz-302/pkg/geo"
	"github.com/mirrorz-org/mirrorz-302/pkg/server"
)

var logger = loggo.GetLogger("<root>")

func LoadConfig(path string) (config server.Config, err error) {
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
	logger.Debugf("LoadConfig IPDB File: %s\n", config.IPDBFile)
	logger.Debugf("LoadConfig HTTP Bind Address: %s\n", config.HTTPBindAddress)
	logger.Debugf("LoadConfig MirrorZ D Directory: %s\n", config.MirrorZDDirectory)
	logger.Debugf("LoadConfig Homepage: %s\n", config.Homepage)
	logger.Debugf("LoadConfig Domain Length: %d\n", config.DomainLength)
	logger.Debugf("LoadConfig Cache Time: %d\n", config.CacheTime)
	logger.Debugf("LoadConfig Log Directory: %s\n", config.LogDirectory)
	return
}

func main() {
	//lint:ignore SA1019 - we don't care
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

	if config.IPDBFile != "" {
		geo.LoadIPDB(config.IPDBFile)
	}

	s := server.NewServer(config)
	if err := s.LoadMirrorZD(); err != nil {
		logger.Errorf("Cannot load mirrorz.d.json: %v\n", err)
		os.Exit(1)
	}

	// Logfile (or its directory) must be unprivilegd
	err = s.InitLoggers()
	if err != nil {
		logger.Errorf("Cannot open log file: %v\n", err)
		os.Exit(1)
	}

	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2, syscall.SIGWINCH)
	go func() {
		for sig := range signalChannel {
			switch sig {
			case syscall.SIGHUP:
				logger.Infof("Got A HUP Signal! Now Reloading mirrorz.d.json....\n")
				s.LoadMirrorZD()
			case syscall.SIGUSR1:
				logger.Infof("Got A USR1 Signal! Now Reloading config.json....\n")
				LoadConfig(*configPtr)
			case syscall.SIGUSR2:
				logger.Infof("Got A USR2 Signal! Now Reopen log file....\n")
				err := s.InitLoggers()
				if err != nil {
					logger.Errorf("Error reopening log file: %v\n", err)
				}
			case syscall.SIGWINCH:
				logger.Infof("Got A WINCH Signal! Now Flush Resolved....\n")
				s.CachePurge()
			}
		}
	}()

	s.StartResolvedTicker()

	logger.Infof("Starting HTTP server on %s\n", config.HTTPBindAddress)
	logger.Errorf("HTTP Server error: %v\n", http.ListenAndServe(config.HTTPBindAddress, s))
}
