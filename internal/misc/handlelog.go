/*
Copyright 2025 Kubotal

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package misc

import (
	"errors"
	"fmt"
	"github.com/bombsimon/logrusr/v4"
	"github.com/go-logr/logr"
	"github.com/sirupsen/logrus"
	"log/slog"
	"os"
	"strings"
	"time"
)

type LogConfig struct {
	Level string `yaml:"level"`
	Mode  string `yaml:"mode"`
}

// To remove, in favor od slog system

func HandleLog(logConfig *LogConfig) (logr.Logger, error) {
	logConfig.Mode = strings.ToLower(logConfig.Mode)
	logConfig.Level = strings.ToUpper(logConfig.Level)

	if logConfig.Mode != "dev" && logConfig.Mode != "json" {
		return logr.New(nil), fmt.Errorf("invalid logMode value: %s. Must be one of 'dev' or 'json'", logConfig.Mode)
	}
	llevel, ok := logLevelByString[logConfig.Level]
	if !ok {
		return logr.New(nil), fmt.Errorf("%s is an invalid value for Log.Level\n", logConfig.Level)
	}
	logrusLog := logrus.New()
	logrusLog.SetLevel(llevel)
	if logConfig.Mode == "json" {
		logrusLog.SetFormatter(&logrus.JSONFormatter{})
	}
	l := logrusr.New(logrusLog)

	return l, nil

}

var logLevelByString = map[string]logrus.Level{
	"PANIC": logrus.PanicLevel,
	"FATAL": logrus.FatalLevel,
	"ERROR": logrus.ErrorLevel,
	"WARN":  logrus.WarnLevel,
	"INFO":  logrus.InfoLevel,
	"DEBUG": logrus.DebugLevel,
	"TRACE": logrus.TraceLevel,
}

func NewSlogLogger(logConfig *LogConfig) (*slog.Logger, error) {
	// Validate config is not nil
	if logConfig == nil {
		return nil, errors.New("logConfig cannot be nil")
	}

	// Validate and parse log level
	if logConfig.Level == "" {
		return nil, errors.New("log level cannot be empty")
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(logConfig.Level)); err != nil {
		return nil, errors.New("invalid log level: " + logConfig.Level + ". Valid levels are: DEBUG, INFO, WARN, ERROR")
	}

	// Validate and parse log mode
	if logConfig.Mode == "" {
		return nil, errors.New("log mode cannot be empty")
	}

	// Create handler options with custom time formatting
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Shorten timestamp format
			if a.Key == slog.TimeKey {
				// Format: "15:04:05.000" (HH:MM:SS.mmm)
				if t, ok := a.Value.Any().(time.Time); ok {
					a.Value = slog.StringValue(t.Format("15:04:05.000"))
				}
			}
			return a
		},
	}

	var handler slog.Handler
	switch strings.ToLower(logConfig.Mode) {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	case "text":
		handler = slog.NewTextHandler(os.Stdout, opts)
	default:
		return nil, errors.New("invalid log mode: " + logConfig.Mode + ". Valid modes are: json, text")
	}

	// Create and return the logger
	logger := slog.New(handler)
	return logger, nil
}
