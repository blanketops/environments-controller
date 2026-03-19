/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package deployment

import (
	"context"
	"fmt"
	"time"

	environmentv1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	deploymentResolution "github.com/ntlaletsi70/blanketops-environments/resolution/deployment"

	"github.com/ntlaletsi70/blanketops-environments/pkg/deployment/application"
	"github.com/ntlaletsi70/blanketops-environments/pkg/deployment/domain"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	fluxkustomize "github.com/fluxcd/kustomize-controller/api/v1"
)

// Reconciler observes Flux Kustomizations and updates Deployment status
// based on terminal reconciliation outcomes.
type Reconciler struct {
	client.Client
	Status   *application.StatusWriter
	Recorder record.EventRecorder
}

func (r *Reconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {

	var ks fluxkustomize.Kustomization
	if err := r.Get(ctx, req.NamespacedName, &ks); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ------------------------------------------------
	// Extract Ready condition (Flux is raw)
	// ------------------------------------------------
	var readyCond *metav1.Condition
	for i := range ks.Status.Conditions {
		if ks.Status.Conditions[i].Type == "Ready" {
			readyCond = &ks.Status.Conditions[i]
			break
		}
	}

	// Not terminal → ignore
	if readyCond == nil || readyCond.Status == metav1.ConditionUnknown {
		return ctrl.Result{}, nil
	}

	// ------------------------------------------------
	// Resolve owning Deployment CR
	// ------------------------------------------------
	deploymentName := ks.Labels["deployment.blanketops.dev/name"]
	if deploymentName == "" {
		return ctrl.Result{}, nil
	}

	var deployment environmentv1.Deployment
	if err := r.Get(
		ctx,
		types.NamespacedName{
			Name:      deploymentName,
			Namespace: ks.Namespace,
		},
		&deployment,
	); err != nil {
		return ctrl.Result{}, err
	}

	// ------------------------------------------------
	// Resolve Deployment (AUTHORITATIVE)
	// ------------------------------------------------
	resolved, err := deploymentResolution.ResolveDeployment(&deployment)
	if err != nil {
		return ctrl.Result{}, err
	}

	// ------------------------------------------------
	// Map Flux condition → domain result
	// ------------------------------------------------
	var phase domain.DeploymentPhase

	switch readyCond.Status {
	case metav1.ConditionTrue:
		phase = domain.DeploymentPhase("Ready")

	case metav1.ConditionFalse:
		phase = domain.DeploymentPhase("Failed")

	default:
		phase = domain.DeploymentPhase("Reconciling")
	}

	result := &domain.DeploymentResult{
		Phase:          phase,
		Message:        readyCond.Message,
		Runtime:        domain.Runtime(resolved.Spec.Runtime.String()),
		LastUpdateTime: time.Now(),
	}

	// ------------------------------------------------
	// Emit events (terminal only)
	// ------------------------------------------------
	if r.Recorder != nil {
		switch phase {

		case domain.DeploymentPhase("Ready"):
			r.Recorder.Event(
				&deployment,
				corev1.EventTypeNormal,
				"DeploymentSucceeded",
				fmt.Sprintf(
					"Kustomization %s applied successfully",
					ks.Name,
				),
			)

		case domain.DeploymentPhase("Failed"):
			r.Recorder.Event(
				&deployment,
				corev1.EventTypeWarning,
				"DeploymentFailed",
				fmt.Sprintf(
					"Kustomization %s failed: %s",
					ks.Name,
					readyCond.Message,
				),
			)
		}
	}

	// ------------------------------------------------
	// Write status (single authoritative write)
	// ------------------------------------------------
	return ctrl.Result{}, r.Status.WriteDeploymentResult(
		ctx,
		&deployment,
		result,
		nil,
	)
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorder("deployment-observer")

	return ctrl.NewControllerManagedBy(mgr).
		For(&fluxkustomize.Kustomization{}).
		Complete(r)
}
