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

// build.go constructs this controller's Build domain cache: a thin
// wrapper around blanketops-environments-core's cache/build package.
//
// The cache itself, and the write path that populates it, live in the
// external core library; this file owns only the constructor.
package build

import (
	libbuild "github.com/ntlaletsi70/blanketops-environments/cache/build"
	"github.com/ntlaletsi70/blanketops-environments/core"
)

// New constructs the Build domain cache for this controller runtime.
func New(c *core.Cache) *libbuild.BuildCache {
	return libbuild.NewBuildCache(c)
}
