/*
Copyright 2026 The BlanketOps Authors.
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

package logging

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

func buildZap(cfg Config) *zap.Logger {
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.TimeKey = "ts"
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	var cores []zapcore.Core

	if cfg.Console {
		cores = append(cores,
			zapcore.NewCore(
				zapcore.NewConsoleEncoder(encCfg),
				zapcore.AddSync(os.Stdout),
				parseLevel(cfg.Level),
			),
		)
	}

	if cfg.File {
		cores = append(cores,
			zapcore.NewCore(
				zapcore.NewJSONEncoder(encCfg),
				zapcore.AddSync(&lumberjack.Logger{
					Filename: cfg.FilePath,
					MaxSize:  100,
					MaxAge:   28,
				}),
				parseLevel(cfg.Level),
			),
		)
	}

	if pt := buildPapertrailCore(cfg, zapcore.NewJSONEncoder(encCfg)); pt != nil {
		cores = append(cores, pt)
	}

	core := zapcore.NewTee(cores...)

	return zap.New(
		core,
		zap.AddCaller(),
		zap.AddStacktrace(zap.ErrorLevel),
	)
}

func parseLevel(lvl string) zapcore.Level {
	switch lvl {
	case "debug":
		return zapcore.DebugLevel
	case "warn":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
