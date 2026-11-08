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

package gitrepository

import (
	"context"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	sourcesv1alpha1 "github.com/blanketops/environments-api/api/sources/v1alpha1"
	corecache "github.com/blanketops/environments/core/cache"
	"github.com/blanketops/environments/core/command"
	gitrepoapi "github.com/blanketops/environments/pkg/apis/gitrepository/api"
	"github.com/blanketops/environments/pkg/apis/gitrepository/application"
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	gitrepomediator "github.com/blanketops/environments-controller/internal/mediators/gitrepository"
	"github.com/blanketops/environments-controller/internal/testsupport"
)

const testAppName = "app-sample"

// Repeated across fixtures below — named to satisfy goconst.
const keyProvider = "provider"

func newEnvironment() *environmentsv1alpha1.Environment {
	return &environmentsv1alpha1.Environment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testAppName,
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
				"environments.blanketops.dev/type": "dev",
			},
		},
		Spec: environmentsv1alpha1.EnvironmentSpec{
			Contract: testsupport.RawContract(map[string]any{
				"applicationName": testAppName,
				"branch":          "main",
				"gitOwner":        "blanketops",
				"environmentType": "dev",
				"version":         "v1",
			}),
		},
	}
}

func newGitRepositoryCR(contract map[string]any) *sourcesv1alpha1.GitRepository {
	r := &sourcesv1alpha1.GitRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gitrepo-sample",
			Namespace: "default",
			Labels: map[string]string{
				"environments.blanketops.dev/name": testAppName,
			},
		},
	}
	if contract != nil {
		r.Spec.Contract = testsupport.RawContract(contract)
	}
	return r
}

func validGitRepositoryContract() map[string]any {
	return map[string]any{
		keyProvider: "github",
		"hookUrl":   "https://events.blanketops.dev/hooks/app-sample",
		"repository": map[string]any{
			"owner": "blanketops",
			"name":  "app",
		},
	}
}

func newTestDomain(t *testing.T, objs ...client.Object) *GitRepositoryDomain {
	t.Helper()
	c := testsupport.NewFakeClient(objs...)
	log := logr.Discard()
	rawRec := testsupport.NoopRawRecorder()

	med := gitrepomediator.New(c, testsupport.NewScheme(), log, rawRec)

	mapper := application.NewMapper()
	statusWriter := application.NewStatusWriter()
	backend := application.NewBackendSelector(gitrepoapi.NewGitHubProvider(c, testsupport.NewScheme(), log, rawRec))
	service := application.NewGitRepositoryService(mapper, statusWriter, backend)

	cache := &corecache.Cache{External: corecache.NoopExternalCache{}}
	return New(med, service, cache, testsupport.NoopRecorder(), log)
}

func conditionStatus(conds []metav1.Condition, condType string) (metav1.ConditionStatus, bool) {
	for _, c := range conds {
		if c.Type == condType {
			return c.Status, true
		}
	}
	return "", false
}

func TestGitRepositoryDomain_GVK(t *testing.T) {
	d := &GitRepositoryDomain{}
	if gvk := d.GVK(); gvk.Kind != "GitRepository" {
		t.Errorf("GVK().Kind = %q, want %q", gvk.Kind, "GitRepository")
	}
}

func TestGitRepositoryDomain_CanCreate(t *testing.T) {
	d := &GitRepositoryDomain{}
	if !d.CanCreate(&sourcesv1alpha1.GitRepository{}) {
		t.Error("CanCreate(*GitRepository) = false, want true")
	}
	if d.CanCreate(&environmentsv1alpha1.Build{}) {
		t.Error("CanCreate(*Build) = true, want false")
	}
}

func TestGitRepositoryDomain_CanDelete(t *testing.T) {
	d := &GitRepositoryDomain{}
	if !d.CanDelete(&sourcesv1alpha1.GitRepository{}) {
		t.Error("CanDelete(*GitRepository) = false, want true")
	}
	if d.CanDelete(&environmentsv1alpha1.Build{}) {
		t.Error("CanDelete(*Build) = true, want false")
	}
}

func TestGitRepositoryDomain_CanUpdate(t *testing.T) {
	tests := []struct {
		name   string
		oldObj client.Object
		newObj client.Object
		want   bool
	}{
		{
			name:   "spec changed",
			oldObj: newGitRepositoryCR(map[string]any{keyProvider: "a"}),
			newObj: newGitRepositoryCR(map[string]any{keyProvider: "b"}),
			want:   true,
		},
		{
			name:   "spec unchanged",
			oldObj: newGitRepositoryCR(validGitRepositoryContract()),
			newObj: newGitRepositoryCR(validGitRepositoryContract()),
			want:   false,
		},
		{
			name:   "wrong type",
			oldObj: &environmentsv1alpha1.Build{},
			newObj: newGitRepositoryCR(validGitRepositoryContract()),
			want:   false,
		},
	}

	d := &GitRepositoryDomain{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := d.CanUpdate(tt.oldObj, tt.newObj); got != tt.want {
				t.Errorf("CanUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGitRepositoryDomain_Handle_InvalidObject(t *testing.T) {
	d := &GitRepositoryDomain{}
	if err := d.Handle(context.Background(), command.Command{Obj: &environmentsv1alpha1.Build{}}); err == nil {
		t.Fatal("Handle() with non-GitRepository object = nil error, want error")
	}
}

func TestGitRepositoryDomain_Handle_Create_ResolutionFailure(t *testing.T) {
	repo := newGitRepositoryCR(nil)
	d := newTestDomain(t, repo)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: repo})
	if err == nil {
		t.Fatal("Handle() with empty contract = nil error, want error")
	}
	if status, ok := conditionStatus(repo.Status.Conditions, "GitRepositoryResolveFailed"); !ok || status != metav1.ConditionFalse {
		t.Errorf("GitRepositoryResolveFailed condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestGitRepositoryDomain_Handle_Create_MissingEnvironment(t *testing.T) {
	repo := newGitRepositoryCR(validGitRepositoryContract())
	d := newTestDomain(t, repo)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: repo})
	if err == nil {
		t.Fatal("Handle() with no owning Environment = nil error, want error")
	}
	if status, ok := conditionStatus(repo.Status.Conditions, "GitRepositoryPrerequisitesCreateFailed"); !ok || status != metav1.ConditionFalse {
		t.Errorf("GitRepositoryPrerequisitesCreateFailed condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestGitRepositoryDomain_Handle_Create_Succeeds(t *testing.T) {
	env := newEnvironment()
	repo := newGitRepositoryCR(validGitRepositoryContract())
	d := newTestDomain(t, env, repo)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdCreate, Obj: repo}); err != nil {
		t.Fatalf("Handle() = %v, want nil", err)
	}

	for _, condType := range []string{"GitRepositoryResolved", "GitRepositoryCached", "GitRepositoryPrerequisitesReady", "GitRepositoryStart"} {
		if status, ok := conditionStatus(repo.Status.Conditions, condType); !ok || status != metav1.ConditionTrue {
			t.Errorf("%s condition = (%v, found=%v), want (True, true)", condType, status, ok)
		}
	}
}

func TestGitRepositoryDomain_Handle_Delete_ResolutionFailure(t *testing.T) {
	repo := newGitRepositoryCR(nil)
	d := newTestDomain(t, repo)

	err := d.Handle(context.Background(), command.Command{Type: command.CmdDelete, Obj: repo})
	if err == nil {
		t.Fatal("Handle() delete with empty contract = nil error, want error")
	}
	if status, ok := conditionStatus(repo.Status.Conditions, "GitRepositoryDeleted"); !ok || status != metav1.ConditionFalse {
		t.Errorf("GitRepositoryDeleted condition = (%v, found=%v), want (False, true)", status, ok)
	}
}

func TestGitRepositoryDomain_Handle_Delete_Succeeds(t *testing.T) {
	env := newEnvironment()
	repo := newGitRepositoryCR(validGitRepositoryContract())
	d := newTestDomain(t, env, repo)

	if err := d.Handle(context.Background(), command.Command{Type: command.CmdDelete, Obj: repo}); err != nil {
		t.Fatalf("Handle() delete = %v, want nil", err)
	}
	if status, ok := conditionStatus(repo.Status.Conditions, "GitRepositoryDeleted"); !ok || status != metav1.ConditionTrue {
		t.Errorf("GitRepositoryDeleted condition = (%v, found=%v), want (True, true)", status, ok)
	}
}
