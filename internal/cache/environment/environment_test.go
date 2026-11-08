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
// NewEnvironmentCache — the field-level Set/Get and PublishResolved logic
// itself is already exhaustively tested there. This test only proves the
// wrapper wires the given cache.Cache through correctly.
package environment

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
		t.Fatal("New() = nil, want an EnvironmentCache")
	}

	ctx := context.Background()
	nn := types.NamespacedName{Namespace: "default", Name: "app-sample"}

	if err := c.SetApplicationName(ctx, nn, 1, "app-sample"); err != nil {
		t.Fatalf("SetApplicationName() = %v, want nil", err)
	}
	got, found, err := c.GetApplicationName(ctx, nn, 1)
	if err != nil || !found || got != "app-sample" {
		t.Errorf("GetApplicationName() = (%q, %v, %v), want (\"app-sample\", true, nil)", got, found, err)
	}
}
