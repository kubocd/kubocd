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
	"fmt"
	"github.com/bombsimon/logrusr/v4"
	"github.com/go-logr/logr"
	"github.com/sirupsen/logrus"
	"strings"
)

type LogConfig struct {
	Level string `yaml:"level"`
	Mode  string `yaml:"mode"`
}

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
