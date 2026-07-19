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

package cache

import (
	"testing"

	"github.com/blanketops/environments/core/cache"

	"github.com/blanketops/environments-controller/internal/testsupport"
)

func TestNewCaches_ConstructsEveryDomainCache(t *testing.T) {
	caches := NewCaches(&cache.Cache{External: testsupport.NewFakeExternalCache()})

	if caches.Build == nil {
		t.Error("caches.Build = nil, want a BuildCache")
	}
	if caches.Deployment == nil {
		t.Error("caches.Deployment = nil, want a DeploymentCache")
	}
	if caches.GitHubEvent == nil {
		t.Error("caches.GitHubEvent = nil, want a GitHubEventCache")
	}
	if caches.GitRepository == nil {
		t.Error("caches.GitRepository = nil, want a GitRepositoryCache")
	}
	if caches.ServiceUnit == nil {
		t.Error("caches.ServiceUnit = nil, want a ServiceUnitCache")
	}
	if caches.Packages == nil {
		t.Error("caches.Packages = nil, want a PackageCache")
	}
}
