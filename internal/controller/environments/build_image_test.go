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

// package.go reconciles the Package CR: routes create/update through the
// core CQRS engine and, on setup, wires the Package domain's mediator,

package environments

import (
	"context"
	"sort"
	"testing"

	environmentsv1alpha1 "github.com/blanketops/environments-api/api/environments/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/blanketops/environments-controller/internal/testsupport"
)

const (
	// Named to satisfy goconst: the same literals appear in the other test
	// files of this package.
	imgBuildName = "the-build"
	imgNamespace = "images"
	keyUnitType  = "type"
	keyUnitImage = "image"
	unitStatic   = "static"

	imageOne = "ghcr.io/example-org/app:one@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	imageTwo = "ghcr.io/example-org/app:two@sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

func buildWithImage(image string) *environmentsv1alpha1.Build {
	b := &environmentsv1alpha1.Build{ObjectMeta: metav1.ObjectMeta{Name: imgBuildName, Namespace: imgNamespace}}
	if image != "" {
		b.Status.Contract = testsupport.RawContract(map[string]any{"Image": image})
	}
	return b
}

func unit(namespace, name string, contract map[string]any) *environmentsv1alpha1.ServiceUnit {
	su := &environmentsv1alpha1.ServiceUnit{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	su.Spec.Contract = testsupport.RawContract(contract)
	return su
}

func buildUnit(namespace, name, build string) *environmentsv1alpha1.ServiceUnit {
	return unit(namespace, name, map[string]any{keyUnitType: "build", "buildRef": map[string]any{"name": build}})
}

func deploymentOf(namespace, name string, units ...string) *environmentsv1alpha1.Deployment {
	d := &environmentsv1alpha1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	list := make([]any, 0, len(units))
	for _, u := range units {
		list = append(list, u)
	}
	d.Spec.Contract = testsupport.RawContract(map[string]any{
		"serviceUnits": list, "runtime": "kubernetes", "strategy": "Rolling",
	})
	return d
}

func requestNames(reqs []reconcile.Request) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.String())
	}
	sort.Strings(out)
	return out
}

func sameNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Only a Build whose pushed image changed concerns the objects that run it.
func TestBuildImageChanged(t *testing.T) {
	p := buildImageChanged()

	undecodable := buildWithImage("")
	undecodable.Status.Contract.Raw = []byte(`{"Image":`)

	updates := []struct {
		name     string
		old, new client.Object
		want     bool
	}{
		{name: "first image", old: buildWithImage(""), new: buildWithImage(imageOne), want: true},
		{name: "new image", old: buildWithImage(imageOne), new: buildWithImage(imageTwo), want: true},
		{name: "same image", old: buildWithImage(imageOne), new: buildWithImage(imageOne)},
		{name: "no image yet", old: buildWithImage(""), new: buildWithImage("")},
		{name: "image gone", old: buildWithImage(imageOne), new: buildWithImage("")},
		{name: "new status does not decode", old: buildWithImage(imageOne), new: undecodable},
		{name: "old status does not decode", old: undecodable, new: buildWithImage(imageOne), want: true},
		{name: "old object is not a Build", old: &environmentsv1alpha1.ServiceUnit{}, new: buildWithImage(imageOne)},
		{name: "new object is not a Build", old: buildWithImage(imageOne), new: &environmentsv1alpha1.ServiceUnit{}},
	}
	for _, tt := range updates {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.Update(event.UpdateEvent{ObjectOld: tt.old, ObjectNew: tt.new}); got != tt.want {
				t.Errorf("Update = %v, want %v", got, tt.want)
			}
		})
	}

	b := buildWithImage(imageOne)
	if p.Create(event.CreateEvent{Object: b}) || p.Delete(event.DeleteEvent{Object: b}) || p.Generic(event.GenericEvent{Object: b}) {
		t.Error("a Build's creation, deletion and generic events must not pass")
	}
}

// A Build re-triggers the ServiceUnits of type build that name it, in its
// own namespace, and nothing else.
func TestMapBuildToServiceUnits(t *testing.T) {
	c := testsupport.NewFakeClient(
		buildUnit(imgNamespace, "web", imgBuildName),
		buildUnit(imgNamespace, "worker", imgBuildName),
		buildUnit(imgNamespace, "other-build", "something-else"),
		buildUnit("other", "web", imgBuildName),
		unit(imgNamespace, "static", map[string]any{keyUnitType: unitStatic, keyUnitImage: "ghcr.io/example-org/app:v1"}),
		unit(imgNamespace, "broken", map[string]any{keyUnitType: "build"}),
	)
	r := &ServiceUnitReconciler{Client: c}

	got := requestNames(r.mapBuildToServiceUnits(context.Background(), buildWithImage(imageOne)))
	want := []string{imgNamespace + "/web", imgNamespace + "/worker"}
	if !sameNames(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

// A Build re-triggers the Deployments that list a ServiceUnit running its
// image. A Deployment lists ServiceUnits by name in its own namespace.
func TestMapBuildToDeployments(t *testing.T) {
	c := testsupport.NewFakeClient(
		buildUnit(imgNamespace, "web", imgBuildName),
		buildUnit(imgNamespace, "worker", imgBuildName),
		unit(imgNamespace, "static", map[string]any{keyUnitType: unitStatic, keyUnitImage: "ghcr.io/example-org/app:v1"}),
		deploymentOf(imgNamespace, "site", "static", "web"),
		deploymentOf(imgNamespace, "jobs", "worker", "web"),
		deploymentOf(imgNamespace, "unrelated", "static"),
		deploymentOf("other", "elsewhere", "web"),
		&environmentsv1alpha1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "broken", Namespace: imgNamespace}},
	)
	r := &DeploymentReconciler{Client: c}

	got := requestNames(r.mapBuildToDeployments(context.Background(), buildWithImage(imageOne)))
	want := []string{imgNamespace + "/jobs", imgNamespace + "/site"}
	if !sameNames(got, want) {
		t.Errorf("requests = %v, want %v (each Deployment once)", got, want)
	}

	// A Build no ServiceUnit names re-triggers nothing.
	orphan := buildWithImage(imageOne)
	orphan.Name = "nobody-uses-this"
	if reqs := r.mapBuildToDeployments(context.Background(), orphan); len(reqs) != 0 {
		t.Errorf("requests = %v, want none", requestNames(reqs))
	}
}

func TestServiceUnitsOfBuild_NoneInAnEmptyCluster(t *testing.T) {
	units, err := serviceUnitsOfBuild(context.Background(), testsupport.NewFakeClient(), types.NamespacedName{Namespace: imgNamespace, Name: "app"})
	if err != nil || len(units) != 0 {
		t.Errorf("serviceUnitsOfBuild = %v, %v; want none and no error", units, err)
	}
}
