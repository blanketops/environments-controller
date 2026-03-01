package logging

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

func buildZap(cfg Config) (*zap.Logger, error) {
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

	core := zapcore.NewTee(cores...)

	// 👇 ADD THIS
	if pt := buildPapertrailCore(cfg, zapcore.NewJSONEncoder(encCfg)); pt != nil {
		cores = append(cores, pt)
	}

	return zap.New(
		core,
		zap.AddCaller(),
		zap.AddStacktrace(zap.ErrorLevel),
	), nil
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
