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

// serviceunit.go constructs this controller's ServiceUnit domain cache: a thin
// wrapper around blanketops-environments-core's cache/serviceunit package.
//
// The cache itself, and the write path that populates it, live in the
// external core library; this file owns only the constructor.
package serviceunit

import (
	libserviceunit "github.com/blanketops/environments/cache/serviceunit"
	"github.com/blanketops/environments/core/cache"
)

// New constructs a ServiceUnit domain cache backed by c.
func New(c *cache.Cache) *libserviceunit.ServiceUnitCache {
	return libserviceunit.NewServiceUnitCache(c)
}
