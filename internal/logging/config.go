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

// Config configures the root logger's output (console/file/Papertrail) and
// verbosity.
type Config struct {
	Development bool

	Console  bool
	File     bool
	FilePath string

	Level string // "debug", "info", "warn", "error"

	EnablePapertrail bool
	PapertrailAddr   string
	PapertrailTag    string
}

// DefaultConfig returns a development-mode Config: console output only,
// info level, no file or Papertrail sink.
func DefaultConfig() Config {
	return Config{
		Development: true,
		Console:     true,
		File:        false,
		FilePath:    "/tmp/blanketops-environment-controller.log",
		Level:       "info",
	}
}
