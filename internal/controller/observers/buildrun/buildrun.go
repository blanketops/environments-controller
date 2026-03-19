package buildrun

import (
	"context"
	"fmt"
	"strconv"

	buildv1 "github.com/ntlaletsi70/blanketops-environments-api/api/environments/v1alpha1"
	"github.com/ntlaletsi70/blanketops-environments/pkg/build/application"
	"github.com/ntlaletsi70/blanketops-environments/pkg/build/domain"
	buildresolution "github.com/ntlaletsi70/blanketops-environments/resolution/build"
	shipwrightv1beta1 "github.com/shipwright-io/build/pkg/apis/build/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const retryAttemptAnnotation = "build.blanketops.dev/retry-attempt"

type Reconciler struct {
	client.Client
	Status   *application.StatusWriter
	Recorder record.EventRecorder
}

func (r *Reconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx).WithValues(
		"controller", "buildrun",
		"buildRun", req.NamespacedName.String(),
	)
	log.Info("reconcile start")

	// ------------------------------------------------
	// Fetch BuildRun
	// ------------------------------------------------
	var br shipwrightv1beta1.BuildRun
	if err := r.Get(ctx, req.NamespacedName, &br); err != nil {
		log.Info("buildrun not found, ignoring")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ------------------------------------------------
	// Only act on terminal BuildRuns
	// ------------------------------------------------
	cond := br.Status.GetCondition("Succeeded")
	if cond == nil || cond.Status == corev1.ConditionUnknown {
		log.Info("skipping: buildrun not terminal yet")
		return ctrl.Result{}, nil
	}

	success := cond.Status == corev1.ConditionTrue
	log = log.WithValues(
		"succeeded", success,
		"reason", cond.Reason,
	)

	// ------------------------------------------------
	// Resolve owning Build
	// ------------------------------------------------
	buildName := br.Labels["build.blanketops.dev/name"]
	if buildName == "" {
		log.Info("skipping: buildrun has no owning build label")
		return ctrl.Result{}, nil
	}

	var build buildv1.Build
	if err := r.Get(
		ctx,
		client.ObjectKey{Namespace: br.Namespace, Name: buildName},
		&build,
	); err != nil {
		log.Error(err, "failed to fetch owning build")
		return ctrl.Result{}, err
	}

	log = log.WithValues(
		"build", build.Name,
		"namespace", build.Namespace,
	)

	// ------------------------------------------------
	// Resolve runtime Build (AUTHORITATIVE)
	// ------------------------------------------------
	resolved, err := buildresolution.ResolveBuild(&build)
	if err != nil {
		log.Error(err, "failed to resolve build contract")
		return ctrl.Result{}, err
	}

	buildHash := br.Labels["build-hash"]
	log = log.WithValues("buildHash", buildHash)
	log.Info("buildrun completed")

	// ------------------------------------------------
	// Retry-on-failure (AUTHORITATIVE)
	// ------------------------------------------------
	if !success &&
		resolved.Spec.Policy != nil &&
		resolved.Spec.Policy.Retry != nil &&
		resolved.Spec.Policy.Retry.OnFailure {
		retry := resolved.Spec.Policy.Retry

		var runs shipwrightv1beta1.BuildRunList
		if err := r.List(
			ctx,
			&runs,
			client.InNamespace(br.Namespace),
			client.MatchingLabels{
				"build.blanketops.dev/name": build.Name,
				"build-hash":                buildHash,
			},
		); err != nil {
			log.Error(err, "failed to list buildruns")
			return ctrl.Result{}, err
		}

		attempts := len(runs.Items)
		log.Info("retry evaluation",
			"attempts", attempts,
			"maxAttempts", retry.MaxAttempts,
		)

		if attempts < int(retry.MaxAttempts) {
			patch := client.MergeFrom(build.DeepCopy())
			if build.Annotations == nil {
				build.Annotations = map[string]string{}
			}
			build.Annotations[retryAttemptAnnotation] = strconv.Itoa(attempts + 1)
			log.Info("retry scheduled", "nextAttempt", attempts+1)

			if err := r.Patch(ctx, &build, patch); err != nil {
				log.Error(err, "failed to persist retry attempt")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}

		log.Info("retry limit reached, finalizing build as failed")
	}

	// ------------------------------------------------
	// Emit events (terminal only)
	// ------------------------------------------------
	if r.Recorder != nil {
		if success {
			r.Recorder.Event(
				&build,
				corev1.EventTypeNormal,
				"BuildSucceeded",
				fmt.Sprintf("BuildRun %s completed successfully", br.Name),
			)
		} else {
			r.Recorder.Event(
				&build,
				corev1.EventTypeWarning,
				"BuildFailed",
				fmt.Sprintf("BuildRun %s failed: %s", br.Name, cond.Message),
			)
		}
	}

	log.Info("finalizing build status")

	result := domain.BuildResult{
		Success:      success,
		Message:      cond.Message,
		ExecutionRef: br.Name,
		BuildHash:    buildHash,
	}

	if br.Status.Output != nil && br.Status.Output.Digest != "" {
		result.ArtifactRef = br.Status.Output.Digest
	}

	return ctrl.Result{}, r.Status.Write(ctx, &build, result, nil)
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorder("buildrun-observer")
	return ctrl.NewControllerManagedBy(mgr).
		For(&shipwrightv1beta1.BuildRun{}).
		Complete(r)
}
