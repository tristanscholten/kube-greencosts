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
	"math"
	"sort"
	"time"

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
	peakHourStart = 7  // 07:00 local time
	peakHourEnd   = 22 // 22:00 local time
	peakPenalty   = 50.0
	negativeBonus = 100.0

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
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=energypricesources,verbs=get;list;watch

func (r *EnergyAwareCronJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var eacj greencostsv1alpha1.EnergyAwareCronJob
	if err := r.Get(ctx, req.NamespacedName, &eacj); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching EnergyAwareCronJob %s: %w", req.NamespacedName, err)
	}

	loc, err := time.LoadLocation(eacj.Spec.TimeZone)
	if err != nil {
		return r.setEACJErrorCondition(ctx, &eacj, fmt.Errorf("invalid timeZone %q: %w", eacj.Spec.TimeZone, err))
	}

	now := time.Now().In(loc)

	// ── Already scheduled for today / hasn't fired yet ────────────────────────
	if eacj.Status.NextScheduledTime != nil {
		scheduled := eacj.Status.NextScheduledTime.In(loc)
		// If we were requeued because the scheduled time arrived, create the Job.
		if !time.Now().Before(eacj.Status.NextScheduledTime.Time) {
			return r.dispatchJob(ctx, &eacj, scheduled)
		}
		// Otherwise wait for the scheduled time.
		return ctrl.Result{RequeueAfter: time.Until(eacj.Status.NextScheduledTime.Time)}, nil
	}

	// ── Fetch EnergyPriceSource ───────────────────────────────────────────────
	var eps greencostsv1alpha1.EnergyPriceSource
	epsKey := types.NamespacedName{
		Namespace: eacj.Namespace,
		Name:      eacj.Spec.EnergyPriceSource.Name,
	}

	if err := r.Get(ctx, epsKey, &eps); err != nil {
		if apierrors.IsNotFound(err) {
			return r.setEACJErrorCondition(ctx, &eacj,
				fmt.Errorf("EnergyPriceSource %s not found", epsKey))
		}
		return ctrl.Result{}, fmt.Errorf("fetching EnergyPriceSource %s: %w", epsKey, err)
	}

	// ── Compute target date (next calendar day) ───────────────────────────────
	targetDate := beginningOfDay(now.Add(24 * time.Hour))

	// ── Select optimal slot ───────────────────────────────────────────────────
	selectedTime, err := r.selectOptimalTime(eacj.Spec, eps.Status.Prices, targetDate, loc)
	if err != nil {
		// No suitable slot and no fallback — recheck later.
		log.Info("no optimal slot found", "reason", err.Error())
		return ctrl.Result{RequeueAfter: retryShort}, nil
	}

	log.Info("scheduled job", "at", selectedTime)

	nextScheduled := metav1.NewTime(selectedTime)
	eacj.Status.NextScheduledTime = &nextScheduled
	eacj.Status.Conditions = setCondition(eacj.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Scheduled",
		Message:            fmt.Sprintf("job scheduled at %s", selectedTime.Format(time.RFC3339)),
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, &eacj); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating EnergyAwareCronJob status: %w", err)
	}

	return ctrl.Result{RequeueAfter: time.Until(selectedTime)}, nil
}

// selectOptimalTime picks the cheapest 30-min slot in [earliestStart, latestStart]
// for the target date. Falls back to spec.fallback.runAt when no price data is
// available. Returns an error only when no time can be determined at all.
func (r *EnergyAwareCronJobReconciler) selectOptimalTime(
	spec greencostsv1alpha1.EnergyAwareCronJobSpec,
	prices []greencostsv1alpha1.PricePoint,
	targetDate time.Time,
	loc *time.Location,
) (time.Time, error) {
	earliest, err := parseHHMM(spec.EarliestStart, targetDate, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing earliestStart: %w", err)
	}

	latest, err := parseHHMM(spec.LatestStart, targetDate, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing latestStart: %w", err)
	}

	window := filterPricesInWindow(prices, earliest, latest)

	if len(window) == 0 {
		if spec.Fallback.WhenPriceDataMissing {
			fallback, err := parseHHMM(spec.Fallback.RunAt, targetDate, loc)
			if err != nil {
				return time.Time{}, fmt.Errorf("parsing fallback.runAt: %w", err)
			}
			return fallback, nil
		}
		return time.Time{}, fmt.Errorf("no price intervals in window [%s, %s] and fallback disabled",
			spec.EarliestStart, spec.LatestStart)
	}

	best := scoredIntervals(window, spec.SchedulePolicy)
	return best[0].At.Time, nil
}

type scoredInterval struct {
	greencostsv1alpha1.PricePoint
	score float64
}

func scoredIntervals(intervals []greencostsv1alpha1.PricePoint, policy greencostsv1alpha1.SchedulePolicy) []scoredInterval {
	scored := make([]scoredInterval, 0, len(intervals))

	for _, iv := range intervals {
		score := iv.EurPerMWh * policy.PriceWeight

		hour := iv.At.Hour()
		if policy.AvoidPeakHours && hour >= peakHourStart && hour < peakHourEnd {
			score += peakPenalty
		}

		if policy.PreferNegativePrices && iv.EurPerMWh < 0 {
			score -= negativeBonus
		}

		scored = append(scored, scoredInterval{PricePoint: iv, score: score})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score < scored[j].score
	})

	return scored
}

// filterPricesInWindow returns price points whose At falls within [earliest, latest).
func filterPricesInWindow(prices []greencostsv1alpha1.PricePoint, earliest, latest time.Time) []greencostsv1alpha1.PricePoint {
	var result []greencostsv1alpha1.PricePoint
	for _, p := range prices {
		t := p.At.Time
		if !t.Before(earliest) && t.Before(latest) {
			result = append(result, p)
		}
	}
	return result
}

// dispatchJob creates the batchv1.Job and clears NextScheduledTime so the
// controller schedules the next day's run on the following reconcile.
func (r *EnergyAwareCronJobReconciler) dispatchJob(
	ctx context.Context,
	eacj *greencostsv1alpha1.EnergyAwareCronJob,
	scheduledAt time.Time,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	job := buildJob(eacj, scheduledAt)
	if err := ctrl.SetControllerReference(eacj, job, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting owner reference on Job: %w", err)
	}

	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, fmt.Errorf("creating Job for EnergyAwareCronJob %s: %w", eacj.Name, err)
	}

	log.Info("job created", "job", job.Name)

	ref := &corev1.ObjectReference{
		APIVersion: "batch/v1",
		Kind:       "Job",
		Namespace:  job.Namespace,
		Name:       job.Name,
	}

	eacj.Status.LastJobRef = ref
	eacj.Status.NextScheduledTime = nil // trigger next-day recalculation

	if err := r.Status().Update(ctx, eacj); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating EnergyAwareCronJob status after dispatch: %w", err)
	}

	// Requeue at a short offset so next-day scheduling happens quickly.
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

func buildJob(eacj *greencostsv1alpha1.EnergyAwareCronJob, scheduledAt time.Time) *batchv1.Job {
	name := fmt.Sprintf("%s-%d", eacj.Name, scheduledAt.Unix())
	maxLen := 63
	if len(name) > maxLen {
		name = name[len(name)-maxLen:]
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   eacj.Namespace,
			Labels:      eacj.Spec.JobTemplate.Labels,
			Annotations: eacj.Spec.JobTemplate.Annotations,
		},
		Spec: eacj.Spec.JobTemplate.Spec,
	}

	return job
}

func (r *EnergyAwareCronJobReconciler) setEACJErrorCondition(ctx context.Context, eacj *greencostsv1alpha1.EnergyAwareCronJob, err error) (ctrl.Result, error) {
	eacj.Status.Conditions = setCondition(eacj.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             conditionReasonError,
		Message:            err.Error(),
		LastTransitionTime: metav1.Now(),
	})

	if updateErr := r.Status().Update(ctx, eacj); updateErr != nil {
		return ctrl.Result{}, fmt.Errorf("updating error condition (original: %w): %v", err, updateErr)
	}

	return ctrl.Result{RequeueAfter: retryShort}, nil
}

// SetupWithManager registers the controller and watches EnergyPriceSource events
// so that updates to price data trigger re-scheduling.
func (r *EnergyAwareCronJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&greencostsv1alpha1.EnergyAwareCronJob{}).
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
func (r *EnergyAwareCronJobReconciler) energyPriceSourceToEACJs(ctx context.Context, obj client.Object) []ctrl.Request {
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

// parseHHMM parses an "HH:MM" string and returns a time.Time on the given date in loc.
func parseHHMM(hhmm string, date time.Time, loc *time.Location) (time.Time, error) {
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil {
		return time.Time{}, fmt.Errorf("parsing %q as HH:MM: %w", hhmm, err)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("%q is not a valid HH:MM time", hhmm)
	}
	return time.Date(date.Year(), date.Month(), date.Day(), h, m, 0, 0, loc), nil
}

// beginningOfDay returns midnight of the given time's date in the same location.
func beginningOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// Ensure math is imported (used for peakPenalty/negativeBonus constants indirectly).
var _ = math.MaxFloat64
