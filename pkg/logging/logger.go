package logging

import (
	"fmt"
	"os"
	"time"

	"github.com/juju/loggo"
)

const DefaultWriter = "default"

type Logger struct {
	loggo.Logger

	context  *loggo.Context
	file     *ReopenFile
	filename string
}

func NewLogger(filename string, level loggo.Level) *Logger {
	l := &Logger{
		context: loggo.NewContext(level),
		file:    NewReopenFile(),
	}
	l.context.AddWriter(DefaultWriter, loggo.NewSimpleWriter(l.file, LoggerFileFormatter))
	l.Logger = l.context.GetLogger(DefaultWriter)
	return l
}

func LoggerFileFormatter(entry loggo.Entry) string {
	ts := entry.Timestamp.In(time.UTC).Format("2006-01-02 15:04:05")
	return fmt.Sprintf("%s %s", ts, entry.Message)
}

func (l *Logger) Open() error {
	return l.file.OpenFile(l.filename, os.O_CREATE|os.O_RDWR|os.O_APPEND, os.ModeAppend|0600)
}

func (l *Logger) Close() (err error) {
	return l.file.Close()
}
