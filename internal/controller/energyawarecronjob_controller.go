/*
Copyright 2026.

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

package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

const (
	// ownerLabel is applied to every Job created by EnergyAwareCronJob so the
	// controller can list its owned jobs without an indexer.
	ownerLabel = "greencosts.hstr.nl/owner"

	// retryShort is used when we want to recheck soon (e.g. waiting for price data).
	retryShort = 15 * time.Minute
)

// EnergyAwareCronJobReconciler reconciles a EnergyAwareCronJob object.
type EnergyAwareCronJobReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=energyawarecronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=energyawarecronjobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=energyawarecronjobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=energypricesources,verbs=get;list;watch

func (r *EnergyAwareCronJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, retErr error) {
	ctx, span := otel.Tracer(controllerTracer).Start(ctx, "EnergyAwareCronJob.Reconcile",
		trace.WithAttributes(
			attribute.String("k8s.resource.name", req.Name),
			attribute.String("k8s.resource.namespace", req.Namespace),
		))
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	log := logf.FromContext(ctx)

	var eacj greencostsv1alpha1.EnergyAwareCronJob
	if err := r.Get(ctx, req.NamespacedName, &eacj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	base := eacj.DeepCopy()

	// ── 1. Sync active Job list ───────────────────────────────────────────────
	// Updates eacj.Status.Active, .LastSuccessfulTime in-memory and deletes old
	// jobs according to history limits — does NOT call Status().Update().
	if err := r.syncActiveJobs(ctx, &eacj); err != nil {
		return ctrl.Result{}, err
	}

	// ── 2. Handle suspend ────────────────────────────────────────────────────
	if eacj.Spec.CronJob.Suspend != nil && *eacj.Spec.CronJob.Suspend {
		log.Info("EnergyAwareCronJob is suspended")
		eacj.Status.NextScheduledTime = nil
		return ctrl.Result{}, r.Status().Patch(ctx, &eacj, client.MergeFrom(base))
	}

	now := time.Now()

	// ── 3. Optimal time already determined — fire or wait ────────────────────
	if eacj.Status.NextScheduledTime != nil {
		if !now.Before(eacj.Status.NextScheduledTime.Time) {
			// Use the stored scheduled time, not now, so the job name is
			// deterministic across conflict retries (same Unix second → same name
			// → AlreadyExists is safely ignored).
			return r.dispatchJob(ctx, base, &eacj, eacj.Status.NextScheduledTime.Time)
		}
		// Status may have changed in syncActiveJobs; persist before sleeping.
		if err := r.Status().Patch(ctx, &eacj, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
		}
		return ctrl.Result{RequeueAfter: time.Until(eacj.Status.NextScheduledTime.Time)}, nil
	}

	// ── 4. Parse cron schedule ───────────────────────────────────────────────
	// Respect spec.cronJob.timeZone by prepending a TZ= directive unless the
	// schedule already carries its own timezone prefix.
	scheduleStr := eacj.Spec.CronJob.Schedule
	if !strings.HasPrefix(scheduleStr, "TZ=") && !strings.HasPrefix(scheduleStr, "CRON_TZ=") {
		tz := "UTC"
		if eacj.Spec.CronJob.TimeZone != nil && *eacj.Spec.CronJob.TimeZone != "" {
			tz = *eacj.Spec.CronJob.TimeZone
		}
		scheduleStr = fmt.Sprintf("TZ=%s %s", tz, scheduleStr)
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	sched, err := parser.Parse(scheduleStr)
	if err != nil {
		return r.failWith(ctx, base, &eacj, fmt.Errorf("invalid schedule %q: %w", eacj.Spec.CronJob.Schedule, err))
	}

	// ── 5. Find next cron-scheduled time ─────────────────────────────────────
	var lastRun time.Time
	if eacj.Status.LastScheduleTime != nil {
		lastRun = eacj.Status.LastScheduleTime.Time
	} else {
		lastRun = eacj.CreationTimestamp.Time
	}

	nextCronTime, err := nextScheduleTime(sched, lastRun, now, eacj.Spec.CronJob.StartingDeadlineSeconds)
	if err != nil {
		return r.failWith(ctx, base, &eacj, err)
	}

	// ── 6. Not yet time — persist active-list changes and sleep ──────────────
	if nextCronTime.After(now) {
		// Record when the next scheduling window opens so kubectl describe always
		// shows something useful before price optimisation has run.
		t := metav1.NewTime(nextCronTime)
		eacj.Status.NextCronWindow = &t
		if err := r.Status().Patch(ctx, &eacj, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
		}
		return ctrl.Result{RequeueAfter: time.Until(nextCronTime)}, nil
	}

	// ── 7. Forbid concurrency — skip if a job is still active ────────────────
	if eacj.Spec.CronJob.ConcurrencyPolicy == batchv1.ForbidConcurrent && len(eacj.Status.Active) > 0 {
		log.Info("skipping schedule: ForbidConcurrent and a job is still active",
			"active", len(eacj.Status.Active))
		t := metav1.NewTime(now)
		eacj.Status.LastScheduleTime = &t
		if err := r.Status().Patch(ctx, &eacj, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// ── 8. Zero-duration window — fire immediately ───────────────────────────
	if eacj.Spec.EnergyStrategy.ScheduleWindow.Duration == 0 {
		return r.dispatchJob(ctx, base, &eacj, now)
	}

	// ── 9. Fetch price data ───────────────────────────────────────────────────
	var eps greencostsv1alpha1.EnergyPriceSource
	epsKey := types.NamespacedName{
		Namespace: eacj.Namespace,
		Name:      eacj.Spec.EnergyPriceSource.Name,
	}
	if err := r.Get(ctx, epsKey, &eps); err != nil {
		if apierrors.IsNotFound(err) {
			return r.failWith(ctx, base, &eacj,
				fmt.Errorf("EnergyPriceSource %q not found", eacj.Spec.EnergyPriceSource.Name))
		}
		return ctrl.Result{}, fmt.Errorf("fetching EnergyPriceSource: %w", err)
	}

	// ── 10. Find the best price in window ─────────────────────────────────────
	windowEnd := nextCronTime.Add(eacj.Spec.EnergyStrategy.ScheduleWindow.Duration)
	window := filterPricesInWindow(eps.Status.Prices, nextCronTime, windowEnd)
	if len(window) == 0 {
		log.Info("no price data in window yet — will retry",
			"windowStart", nextCronTime.Format(time.RFC3339),
			"windowEnd", windowEnd.Format(time.RFC3339))
		if err := r.Status().Patch(ctx, &eacj, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating status: %w", err)
		}
		return ctrl.Result{RequeueAfter: retryShort}, nil
	}

	selected, err := selectPricePoint(window, eacj.Spec.EnergyStrategy.Strategy)
	if err != nil {
		return r.failWith(ctx, base, &eacj, err)
	}
	optimalTime := selected.At.Time

	// Optimal slot is already in the past within the window — fire now.
	if !optimalTime.After(now) {
		return r.dispatchJob(ctx, base, &eacj, now)
	}

	// ── 11. Persist optimal time and sleep until it arrives ──────────────────
	log.Info("optimal time selected",
		"at", optimalTime.Format(time.RFC3339),
		"price", selected.EurPerMWh,
		"strategy", eacj.Spec.EnergyStrategy.Strategy)
	next := metav1.NewTime(optimalTime)
	windowOpen := metav1.NewTime(nextCronTime)
	eacj.Status.NextCronWindow = &windowOpen
	eacj.Status.NextScheduledTime = &next
	eacj.Status.Conditions = setCondition(eacj.Status.Conditions, metav1.Condition{
		Type:   conditionTypeReady,
		Status: metav1.ConditionTrue,
		Reason: "Scheduled",
		Message: fmt.Sprintf("optimally scheduled at %s (%.2f €/MWh)",
			optimalTime.Format(time.RFC3339), selected.EurPerMWh),
		LastTransitionTime: metav1.Now(),
	})
	if err := r.Status().Patch(ctx, &eacj, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating NextScheduledTime: %w", err)
	}
	return ctrl.Result{RequeueAfter: time.Until(optimalTime)}, nil
}

// dispatchJob creates the batchv1.Job, applying Replace concurrency policy if
// necessary, and updates the EnergyAwareCronJob status.
func (r *EnergyAwareCronJobReconciler) dispatchJob(
	ctx context.Context,
	base *greencostsv1alpha1.EnergyAwareCronJob,
	eacj *greencostsv1alpha1.EnergyAwareCronJob,
	now time.Time,
) (ctrl.Result, error) {
	ctx, span := otel.Tracer(controllerTracer).Start(ctx, "EnergyAwareCronJob.dispatchJob",
		trace.WithAttributes(
			attribute.String("k8s.resource.name", eacj.Name),
			attribute.String("k8s.resource.namespace", eacj.Namespace),
			attribute.String("scheduled_time", now.Format(time.RFC3339)),
		))
	defer span.End()

	log := logf.FromContext(ctx)

	// Replace: delete all active jobs before creating the new one.
	if eacj.Spec.CronJob.ConcurrencyPolicy == batchv1.ReplaceConcurrent {
		for _, ref := range eacj.Status.Active {
			job := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: ref.Namespace},
			}
			if err := r.Delete(ctx, job,
				client.PropagationPolicy(metav1.DeletePropagationBackground),
			); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("deleting active job %s: %w", ref.Name, err)
			}
		}
	}

	job := buildJob(eacj, now)
	if err := ctrl.SetControllerReference(eacj, job, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting owner reference: %w", err)
	}
	if err := r.Create(ctx, job); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("creating Job for EnergyAwareCronJob %s: %w", eacj.Name, err)
		}
	} else {
		log.Info("job dispatched", "job", job.Name)
	}

	ref := corev1.ObjectReference{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Namespace:  job.Namespace,
		Name:       job.Name,
		UID:        job.UID,
	}
	t := metav1.NewTime(now)
	eacj.Status.LastScheduleTime = &t
	eacj.Status.NextCronWindow = nil
	eacj.Status.NextScheduledTime = nil
	eacj.Status.Active = append(eacj.Status.Active, ref)
	eacj.Status.Conditions = setCondition(eacj.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             "JobDispatched",
		Message:            fmt.Sprintf("job %s dispatched at %s", job.Name, now.Format(time.RFC3339)),
		LastTransitionTime: metav1.Now(),
	})
	if err := r.Status().Patch(ctx, eacj, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating status after dispatch: %w", err)
	}
	// Requeue immediately so the reconciler computes the next cron window and
	// optimal dispatch time and surfaces them in status without waiting for an
	// external watch event.
	return ctrl.Result{Requeue: true}, nil
}

// syncActiveJobs lists all Jobs owned by eacj, updates status.Active and
// status.LastSuccessfulTime in-memory, and deletes excess historical jobs
// according to successfulJobsHistoryLimit and failedJobsHistoryLimit.
// It does NOT call Status().Update(); the caller is responsible for persisting.
func (r *EnergyAwareCronJobReconciler) syncActiveJobs(
	ctx context.Context,
	eacj *greencostsv1alpha1.EnergyAwareCronJob,
) error {
	var jobList batchv1.JobList
	if err := r.List(ctx, &jobList,
		client.InNamespace(eacj.Namespace),
		client.MatchingLabels{ownerLabel: energyAwareCronJobOwnerLabelValue(eacj.Name)},
	); err != nil {
		return fmt.Errorf("listing Jobs for EnergyAwareCronJob %s: %w", eacj.Name, err)
	}

	var activeRefs []corev1.ObjectReference
	var successfulJobs, failedJobs []*batchv1.Job

	for i := range jobList.Items {
		job := &jobList.Items[i]
		finished, condType := jobFinished(job)
		switch {
		case !finished:
			activeRefs = append(activeRefs, corev1.ObjectReference{
				APIVersion: "batch/v1",
				Kind:       "Job",
				Namespace:  job.Namespace,
				Name:       job.Name,
				UID:        job.UID,
			})
		case condType == batchv1.JobComplete:
			successfulJobs = append(successfulJobs, job)
			if job.Status.CompletionTime != nil {
				t := metav1.NewTime(job.Status.CompletionTime.Time)
				if eacj.Status.LastSuccessfulTime == nil ||
					t.After(eacj.Status.LastSuccessfulTime.Time) {
					eacj.Status.LastSuccessfulTime = &t
				}
			}
		default:
			failedJobs = append(failedJobs, job)
		}
	}

	eacj.Status.Active = activeRefs

	// Delete excess successful jobs (oldest first).
	successLimit := int32(3)
	if eacj.Spec.CronJob.SuccessfulJobsHistoryLimit != nil {
		successLimit = *eacj.Spec.CronJob.SuccessfulJobsHistoryLimit
	}
	if excess := len(successfulJobs) - int(successLimit); excess > 0 {
		sort.Slice(successfulJobs, func(i, j int) bool {
			return jobStartTime(successfulJobs[i]).Before(jobStartTime(successfulJobs[j]))
		})
		for _, job := range successfulJobs[:excess] {
			if err := r.Delete(ctx, job,
				client.PropagationPolicy(metav1.DeletePropagationBackground),
			); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting old successful job %s: %w", job.Name, err)
			}
		}
	}

	// Delete excess failed jobs (oldest first).
	failedLimit := int32(1)
	if eacj.Spec.CronJob.FailedJobsHistoryLimit != nil {
		failedLimit = *eacj.Spec.CronJob.FailedJobsHistoryLimit
	}
	if excess := len(failedJobs) - int(failedLimit); excess > 0 {
		sort.Slice(failedJobs, func(i, j int) bool {
			return jobStartTime(failedJobs[i]).Before(jobStartTime(failedJobs[j]))
		})
		for _, job := range failedJobs[:excess] {
			if err := r.Delete(ctx, job,
				client.PropagationPolicy(metav1.DeletePropagationBackground),
			); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting old failed job %s: %w", job.Name, err)
			}
		}
	}

	return nil
}

// buildJob constructs a batchv1.Job from the EnergyAwareCronJob's cronJob.jobTemplate.
func buildJob(eacj *greencostsv1alpha1.EnergyAwareCronJob, scheduledAt time.Time) *batchv1.Job {
	labels := copyStringMap(eacj.Spec.CronJob.JobTemplate.Labels)
	labels[ownerLabel] = energyAwareCronJobOwnerLabelValue(eacj.Name)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        energyAwareCronJobName(eacj.Name, scheduledAt),
			Namespace:   eacj.Namespace,
			Labels:      labels,
			Annotations: copyStringMap(eacj.Spec.CronJob.JobTemplate.Annotations),
		},
		Spec: eacj.Spec.CronJob.JobTemplate.Spec,
	}
}

func energyAwareCronJobName(name string, scheduledAt time.Time) string {
	return shortObjectName(name, name, fmt.Sprintf("-%d", scheduledAt.Unix()))
}

func energyAwareCronJobOwnerLabelValue(name string) string {
	return shortLabelValue(name)
}

func copyStringMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

// nextScheduleTime returns the next cron fire time after lastRun.
// When startingDeadlineSeconds is set it fast-forwards past any schedules that
// are older than the deadline, preventing a burst of catch-up jobs.
func nextScheduleTime(
	sched cron.Schedule,
	lastRun, now time.Time,
	startingDeadlineSeconds *int64,
) (time.Time, error) {
	next := sched.Next(lastRun)

	if startingDeadlineSeconds == nil {
		return next, nil
	}

	deadline := now.Add(-time.Duration(*startingDeadlineSeconds) * time.Second)
	for i := 0; next.Before(deadline); i++ {
		if i >= 100 {
			return time.Time{}, fmt.Errorf(
				"too many missed schedules (> 100); check startingDeadlineSeconds")
		}
		next = sched.Next(next)
	}
	return next, nil
}

// filterPricesInWindow returns price points whose At falls in [earliest, latest).
func filterPricesInWindow(
	prices []greencostsv1alpha1.PricePoint,
	earliest, latest time.Time,
) []greencostsv1alpha1.PricePoint {
	var result []greencostsv1alpha1.PricePoint
	for _, p := range prices {
		t := p.At.Time
		if !t.Before(earliest) && t.Before(latest) {
			result = append(result, p)
		}
	}
	return result
}

func selectPricePoint(
	prices []greencostsv1alpha1.PricePoint,
	strategy greencostsv1alpha1.Strategy,
) (greencostsv1alpha1.PricePoint, error) {
	if len(prices) == 0 {
		return greencostsv1alpha1.PricePoint{}, fmt.Errorf("no prices available")
	}

	if strategy == "" {
		strategy = greencostsv1alpha1.LowestPrice
	}

	selected := prices[0]
	for _, p := range prices[1:] {
		switch strategy {
		case greencostsv1alpha1.LowestPrice:
			if p.EurPerMWh < selected.EurPerMWh {
				selected = p
			}
		case greencostsv1alpha1.HighestPrice:
			if p.EurPerMWh > selected.EurPerMWh {
				selected = p
			}
		default:
			return greencostsv1alpha1.PricePoint{}, fmt.Errorf("unsupported energyStrategy.strategy %q", strategy)
		}
	}

	return selected, nil
}

// jobFinished reports whether a Job has reached a terminal state and which condition type it is.
func jobFinished(job *batchv1.Job) (bool, batchv1.JobConditionType) {
	for _, cond := range job.Status.Conditions {
		if (cond.Type == batchv1.JobComplete || cond.Type == batchv1.JobFailed) &&
			cond.Status == corev1.ConditionTrue {
			return true, cond.Type
		}
	}
	return false, ""
}

// jobStartTime returns the start time of a Job, falling back to creation time.
func jobStartTime(job *batchv1.Job) time.Time {
	if job.Status.StartTime != nil {
		return job.Status.StartTime.Time
	}
	return job.CreationTimestamp.Time
}

func (r *EnergyAwareCronJobReconciler) failWith(
	ctx context.Context, base *greencostsv1alpha1.EnergyAwareCronJob, eacj *greencostsv1alpha1.EnergyAwareCronJob,
	err error,
) (ctrl.Result, error) {
	eacj.Status.Conditions = setCondition(eacj.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             conditionReasonError,
		Message:            err.Error(),
		LastTransitionTime: metav1.Now(),
	})
	if updateErr := r.Status().Patch(ctx, eacj, client.MergeFrom(base)); updateErr != nil {
		return ctrl.Result{}, fmt.Errorf("updating error condition (original: %w): %v", err, updateErr)
	}
	return ctrl.Result{RequeueAfter: retryShort}, nil
}

// SetupWithManager registers the controller and watches both Jobs (owned by
// EnergyAwareCronJob) and EnergyPriceSource events.
func (r *EnergyAwareCronJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&greencostsv1alpha1.EnergyAwareCronJob{}).
		Owns(&batchv1.Job{}).
		Watches(
			&greencostsv1alpha1.EnergyPriceSource{},
			handler.EnqueueRequestsFromMapFunc(r.energyPriceSourceToEACJs),
			builder.WithPredicates(),
		).
		Named("energyawarecronjob").
		Complete(r)
}

// energyPriceSourceToEACJs maps an EnergyPriceSource event to all
// EnergyAwareCronJob objects in the same namespace that reference it.
func (r *EnergyAwareCronJobReconciler) energyPriceSourceToEACJs(
	ctx context.Context,
	obj client.Object,
) []ctrl.Request {
	var list greencostsv1alpha1.EnergyAwareCronJobList
	if err := r.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}

	var requests []ctrl.Request
	for _, eacj := range list.Items {
		if eacj.Spec.EnergyPriceSource.Name == obj.GetName() {
			requests = append(requests, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Namespace: eacj.Namespace,
					Name:      eacj.Name,
				},
			})
		}
	}
	return requests
}
