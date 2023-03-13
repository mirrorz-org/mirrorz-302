package main

import (
	"fmt"
	"os"
	"path"
	"time"

	"github.com/juju/loggo"
)

type Logger struct {
	loggo.Logger
	f *os.File
}

func LoggerFileFormatter(entry loggo.Entry) string {
	ts := entry.Timestamp.In(time.UTC).Format("2006-01-02 15:04:05")
	return fmt.Sprintf("%s %s", ts, entry.Message)
}

func (l *Logger) Open(filename string, level loggo.Level) (err error) {
	context := loggo.NewContext(level)
	logfile := path.Join(config.LogDirectory, filename)
	f, err := os.OpenFile(logfile, os.O_CREATE|os.O_RDWR|os.O_APPEND, os.ModeAppend|0600)
	if err != nil {
		return
	}
	err = context.AddWriter("default", loggo.NewSimpleWriter(f, LoggerFileFormatter))
	if err != nil {
		return
	}
	l.Logger = context.GetLogger("default")
	err = l.f.Close()
	l.f = f
	return
}

func (l *Logger) Close() (err error) {
	if l.f != nil {
		err = l.f.Close()
		l.f = nil
	}
	return
}
