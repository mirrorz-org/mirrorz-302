package logging

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/juju/loggo"
)

const DefaultWriter = "default"

var (
	contexts sync.Map // map[string]*loggo.Context
)

func GetContext(name string) *loggo.Context {
	if v, ok := contexts.Load(name); ok {
		return v.(*loggo.Context)
	}
	c := loggo.NewContext(loggo.INFO)
	contexts.Store(name, c)
	return c
}

func SetContextLevel(name string, level loggo.Level) {
	config, err := loggo.ParseConfigString("<root>=" + level.String())
	if err != nil {
		panic(err)
	}
	GetContext(name).ApplyConfig(config)
}

func SetContextFile(name, filename string) error {
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR|os.O_APPEND, os.ModeAppend|0600)
	if err != nil {
		return err
	}
	c := GetContext(name)

	// *os.File has a default finalizer from os.Open, so we don't need to close it
	c.RemoveWriter(DefaultWriter)
	c.AddWriter(DefaultWriter, loggo.NewSimpleWriter(f, LoggerFileFormatter))
	return nil
}

func GetLogger(name string) loggo.Logger {
	return GetContext(name).GetLogger("<root>")
}

func LoggerFileFormatter(entry loggo.Entry) string {
	ts := entry.Timestamp.In(time.UTC).Format("2006-01-02 15:04:05")
	return fmt.Sprintf("%s %s", ts, entry.Message)
}
