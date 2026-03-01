package logging

import (
	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
)

func AsLogr(z *zap.Logger) logr.Logger {
	return zapr.NewLogger(z)
}
