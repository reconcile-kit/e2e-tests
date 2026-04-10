package logger

import (
	"fmt"
	"os"

	"github.com/sirupsen/logrus"
)

type Level int32

const (
	LevelInfo Level = 4
)

type Logger struct {
	*logrus.Logger
}

type CustomFormatter struct{}

func (f *CustomFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	ts := entry.Time.Format("2006-01-02 15:04:05")
	return []byte(fmt.Sprintf("%s [%s] %s\n", ts, entry.Level.String(), entry.Message)), nil
}

func New(level int32) *Logger {
	l := &Logger{Logger: logrus.New()}
	l.SetOutput(os.Stdout)
	l.SetFormatter(&CustomFormatter{})
	l.SetLevel(logrus.Level(level))
	return l
}
