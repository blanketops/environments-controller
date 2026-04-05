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
