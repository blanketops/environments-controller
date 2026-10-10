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

package environment

import (
	"regexp"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCheckCondition(t *testing.T) {
	tests := []struct {
		name      string
		conds     []metav1.Condition
		condType  string
		wantReady bool
		wantMsg   string
	}{
		{
			name:      "true",
			conds:     []metav1.Condition{{Type: condServiceUnitReady, Status: metav1.ConditionTrue, Message: "running image x"}},
			condType:  condServiceUnitReady,
			wantReady: true, wantMsg: "running image x",
		},
		{
			name:     "false, with the reason it gives",
			conds:    []metav1.Condition{{Type: condServiceUnitReady, Status: metav1.ConditionFalse, Message: "waiting for build app to push an image"}},
			condType: condServiceUnitReady,
			wantMsg:  "waiting for build app to push an image",
		},
		{
			name:     "not true, with nothing said",
			conds:    []metav1.Condition{{Type: condPackageSucceeded, Status: metav1.ConditionUnknown}},
			condType: condPackageSucceeded,
			wantMsg:  "Succeeded is not True",
		},
		{
			name:     "another Kind's condition is not this one",
			conds:    []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}},
			condType: condServiceUnitReady,
			wantMsg:  "no ServiceUnitReady condition",
		},
		{name: "no conditions", condType: condPackageSucceeded, wantMsg: "no Succeeded condition"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready, msg := checkCondition(tt.conds, tt.condType)
			if ready != tt.wantReady || msg != tt.wantMsg {
				t.Errorf("checkCondition = %v, %q; want %v, %q", ready, msg, tt.wantReady, tt.wantMsg)
			}
		})
	}
}

func TestCheckReady(t *testing.T) {
	if ready, _ := checkReady([]metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}); !ready {
		t.Error("checkReady = false for Ready=True")
	}
	if ready, msg := checkReady(nil); ready || msg != "no Ready condition" {
		t.Errorf("checkReady(nil) = %v, %q", ready, msg)
	}
}

// The API server rejects a status whose condition reason is not CamelCase:
// one bad reason fails the whole write, and the Environment then reports
// nothing. The reason is the Kind, never an object's name.
func TestMakeCondition_ReasonIsAcceptedByTheAPIServer(t *testing.T) {
	valid := regexp.MustCompile(`^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`)
	now := metav1.Now()

	for _, kind := range []string{"Build", "GitRepository", "Deployment", "Route", "Package", "ServiceUnit"} {
		c := makeCondition(kind+".for-kaniko-app-web.Ready", true, kind, "", now)
		if !valid.MatchString(c.Reason) {
			t.Errorf("reason %q for %s would be rejected", c.Reason, kind)
		}
		if c.Status != metav1.ConditionTrue || c.Type != kind+".for-kaniko-app-web.Ready" {
			t.Errorf("condition = %+v", c)
		}
	}
	if c := makeCondition("Build.x.Ready", false, "Build", "no image", now); c.Status != metav1.ConditionFalse || c.Message != "no image" {
		t.Errorf("condition = %+v, want False with the message", c)
	}
}
