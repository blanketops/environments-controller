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

// New is a one-line constructor forwarding to the external library's
// NewGitRepositoryCache — the field-level Set/Get and PublishResolved logic
// itself is already exhaustively tested there. This test only proves the
// wrapper wires the given cache.Cache through correctly.
package gitrepository

import (
	"context"
	"testing"

	"github.com/blanketops/environments/core/cache"
	"k8s.io/apimachinery/pkg/types"

	"github.com/blanketops/environments-controller/internal/testsupport"
)

func TestNew_WiresExternalCache(t *testing.T) {
	c := New(&cache.Cache{External: testsupport.NewFakeExternalCache()})
	if c == nil {
		t.Fatal("New() = nil, want a GitRepositoryCache")
	}

	ctx := context.Background()
	nn := types.NamespacedName{Namespace: "default", Name: "gitrepo-sample"}

	if err := c.SetProvider(ctx, nn, 1, "gitrepo-sample", "github"); err != nil {
		t.Fatalf("SetProvider() = %v, want nil", err)
	}
	got, found, err := c.GetProvider(ctx, nn, 1, "gitrepo-sample")
	if err != nil || !found || got != "github" {
		t.Errorf("GetProvider() = (%q, %v, %v), want (\"github\", true, nil)", got, found, err)
	}
}
