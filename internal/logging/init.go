package logging

import (
	"sync"

	"github.com/go-logr/logr"
	"go.uber.org/zap"
)

var (
	rootZap *zap.Logger
	rootLog logr.Logger
	once    sync.Once
)

func Init(cfg Config) (logr.Logger, *zap.Logger, error) {
	var err error

	once.Do(func() {
		rootZap, err = buildZap(cfg)
		if err != nil {
			return
		}
		rootLog = AsLogr(rootZap)
	})

	return rootLog, rootZap, err
}
