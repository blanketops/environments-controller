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

/*
Package serviceunit implements the ServiceUnit prerequisite mediator.

Currently a stub relative to its siblings (build, deployment, packages):
EnsurePrerequisites validates that resolved and its Spec are non-nil and
provisions nothing: a ServiceUnit's workload is applied by the owning
Deployment's reconciliation, not by this mediator. Its one prerequisite is
read, not created — a ServiceUnit of type BUILD runs the image its Build
last pushed, and that image is injected into the resolved contract here.
*/
package serviceunit

import (
	"context"
	"fmt"

	serviceunitquery "github.com/blanketops/environments/pkg/apis/serviceunit/query"
	serviceunitResolution "github.com/blanketops/environments/resolution/serviceunit/resolve"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Mediator coordinates the various stages of the application lifecycle.
type Mediator struct {
	Client client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// New creates a new Mediator with all the sub-reconcilers.
func New(c client.Client, scheme *runtime.Scheme, log logr.Logger) *Mediator {
	return &Mediator{
		Client: c,
		Scheme: scheme,
		Log:    log,
	}
}

// EnsurePrerequisites validates resolved and ensures whatever the
// ServiceUnit needs before its workload can be reconciled.
func (m *Mediator) EnsurePrerequisites(
	ctx context.Context,
	resolved *serviceunitResolution.ResolvedServiceUnit,
) error {

	if resolved == nil || resolved.Spec == nil {
		return fmt.Errorf("nil ResolvedServiceUnit (resolver bug)")
	}

	s := resolved.ServiceUnit
	l := m.Log.WithValues(
		"serviceunit", s.Name,
		"namespace", resolved.ServiceUnit.Namespace,
	)
	log := l

	log.Info("mediator start")

	spec := resolved.Spec

	// A ServiceUnit of type BUILD names a Build, not an image. The image
	// stays empty while that Build has pushed nothing; the domain reports
	// that as waiting.
	if err := serviceunitquery.InjectBuildImage(ctx, m.Client, resolved); err != nil {
		return fmt.Errorf("resolve image from build: %w", err)
	}

	log.Info("resolved serviceunit contract",
		"name", resolved.ServiceUnit.Name,
		"replicas", spec.Size,
		"image", spec.Image,
		"port", spec.ContainerPort,
	)

	return nil
}
