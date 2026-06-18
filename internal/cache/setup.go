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
	libbuild "github.com/ntlaletsi70/blanketops-environments/cache/build"
	libdeployment "github.com/ntlaletsi70/blanketops-environments/cache/deployment"
	libgithubevent "github.com/ntlaletsi70/blanketops-environments/cache/githubevent"
	libgitrepository "github.com/ntlaletsi70/blanketops-environments/cache/gitrepository"
	libpackages "github.com/ntlaletsi70/blanketops-environments/cache/packages"
	libserviceunit "github.com/ntlaletsi70/blanketops-environments/cache/serviceunit"
	"github.com/ntlaletsi70/blanketops-environments/core"
)

// Caches aggregates all domain caches, constructed once in main and
// injected into reconcilers.
type Caches struct {
	Build         *libbuild.BuildCache
	Deployment    *libdeployment.DeploymentCache
	GitHubEvent   *libgithubevent.GitHubEventCache
	GitRepository *libgitrepository.GitRepositoryCache
	ServiceUnit   *libserviceunit.ServiceUnitCache
	Packages      *libpackages.PackageCache
}

func NewCaches(c *core.Cache) *Caches {
	return &Caches{
		Build:         libbuild.NewBuildCache(c),
		Deployment:    libdeployment.NewDeploymentCache(c),
		GitHubEvent:   libgithubevent.NewGitHubEventCache(c),
		GitRepository: libgitrepository.NewGitRepositoryCache(c),
		ServiceUnit:   libserviceunit.NewServiceUnitCache(c),
		Packages:      libpackages.NewPackageCache(c),
	}
}
