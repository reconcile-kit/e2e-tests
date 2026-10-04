package logger

import (
	"bytes"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestCustomFormatter(t *testing.T) {
	ts := time.Date(2026, 10, 4, 12, 30, 45, 0, time.UTC)
	out, err := (&CustomFormatter{}).Format(&logrus.Entry{Time: ts, Level: logrus.WarnLevel, Message: "hello"})
	require.NoError(t, err)
	require.Equal(t, "2026-10-04 12:30:45 [warning] hello\n", string(out))
}

func TestNew_Level(t *testing.T) {
	l := New(int32(LevelInfo))
	require.Equal(t, logrus.InfoLevel, l.GetLevel())

	var buf bytes.Buffer
	l.SetOutput(&buf)
	l.Debug("hidden")
	l.Info("shown")
	require.NotContains(t, buf.String(), "hidden")
	require.Contains(t, buf.String(), "[info] shown")
}
