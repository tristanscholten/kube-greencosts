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
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
	"github.com/tristanscholten/kube-greencosts/internal/metrics"
)

const (
	annotationOriginalReplicas = "greencosts.hstr.nl/original-replicas"
	annotationHibernated       = "greencosts.hstr.nl/hibernated"

	idleCheckInterval = 5 * time.Minute
)

// HibernatePolicyReconciler reconciles a HibernatePolicy object.
type HibernatePolicyReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	MetricsClient metrics.Client
}

// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=hibernatepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=hibernatepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=hibernatepolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=create;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch

func (r *HibernatePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var hp greencostsv1alpha1.HibernatePolicy
	if err := r.Get(ctx, req.NamespacedName, &hp); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching HibernatePolicy %s: %w", req.NamespacedName, err)
	}

	// ── List matching namespaces ──────────────────────────────────────────────
	selector, err := metav1.LabelSelectorAsSelector(&hp.Spec.Selector.NamespaceSelector)
	if err != nil {
		return r.setHPErrorCondition(ctx, &hp, fmt.Errorf("invalid namespaceSelector: %w", err))
	}

	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList, &client.ListOptions{LabelSelector: selector}); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing namespaces: %w", err)
	}

	now := time.Now()
	hibernated := []string{}
	var nextTransition time.Time

	for i := range nsList.Items {
		ns := &nsList.Items[i]
		inIgnore, windowEnd := isInIgnoreWindow(hp.Spec.IdleDetection.IgnoreDuring, now)

		if inIgnore {
			// Wake up namespaces that were previously hibernated.
			if wakeErr := r.wakeNamespace(ctx, ns.Name); wakeErr != nil {
				log.Error(wakeErr, "waking namespace", "namespace", ns.Name)
			}
			// Track the earliest window end so we know when to next check.
			if nextTransition.IsZero() || windowEnd.Before(nextTransition) {
				nextTransition = windowEnd
			}
			continue
		}

		// Track when the next ignore window begins so we can wake in time.
		nextStart := nextIgnoreWindowStart(hp.Spec.IdleDetection.IgnoreDuring, now)
		if !nextStart.IsZero() {
			if nextTransition.IsZero() || nextStart.Before(nextTransition) {
				nextTransition = nextStart
			}
		}

		// Check if namespace is already hibernated.
		isAlreadyHibernated, err := r.isNamespaceHibernated(ctx, ns.Name)
		if err != nil {
			log.Error(err, "checking hibernation state", "namespace", ns.Name)
			continue
		}

		if isAlreadyHibernated {
			hibernated = append(hibernated, ns.Name)
			continue
		}

		// Evaluate idle conditions.
		idle, err := r.isNamespaceIdle(ctx, ns.Name, hp.Spec.IdleDetection)
		if err != nil {
			log.Error(err, "evaluating idle conditions", "namespace", ns.Name)
			continue
		}

		if idle {
			log.Info("hibernating namespace", "namespace", ns.Name)
			if hibernateErr := r.hibernateNamespace(ctx, ns.Name, hp.Spec.Action); hibernateErr != nil {
				log.Error(hibernateErr, "hibernating namespace", "namespace", ns.Name)
				continue
			}
			hibernated = append(hibernated, ns.Name)
		}
	}

	// ── Update status ─────────────────────────────────────────────────────────
	hp.Status.HibernatedNamespaces = hibernated
	hp.Status.Conditions = setCondition(hp.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            fmt.Sprintf("%d namespaces under management", len(nsList.Items)),
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, &hp); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating HibernatePolicy status: %w", err)
	}

	requeueAfter := idleCheckInterval
	if !nextTransition.IsZero() {
		if d := time.Until(nextTransition); d > 0 && d < requeueAfter {
			requeueAfter = d
		}
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// isNamespaceIdle returns true when all configured idle thresholds are met.
func (r *HibernatePolicyReconciler) isNamespaceIdle(
	ctx context.Context,
	namespace string,
	detection greencostsv1alpha1.IdleDetection,
) (bool, error) {
	if r.MetricsClient == nil {
		return false, nil
	}

	// CPU check (metrics-server).
	if detection.CPUBelow != nil {
		cpu, err := r.MetricsClient.QueryNamespaceCPU(ctx, namespace)
		if err != nil {
			return false, fmt.Errorf("querying CPU for %q: %w", namespace, err)
		}
		if cpu.Cmp(*detection.CPUBelow) >= 0 {
			return false, nil
		}
	}

	// Network check (Prometheus).
	if detection.NetworkBelow != nil {
		window := defaultDuration(detection.NoIngressRequestsFor, 30*time.Minute)
		net, err := r.MetricsClient.QueryNamespaceNetwork(ctx, namespace, window)
		if err != nil {
			return false, fmt.Errorf("querying network for %q: %w", namespace, err)
		}
		if net.Cmp(*detection.NetworkBelow) >= 0 {
			return false, nil
		}
	}

	// Ingress request check (Prometheus).
	if detection.NoIngressRequestsFor != nil {
		rps, err := r.MetricsClient.QueryNamespaceIngressRPS(ctx, namespace, detection.NoIngressRequestsFor.Duration)
		if err != nil {
			return false, fmt.Errorf("querying ingress RPS for %q: %w", namespace, err)
		}
		if rps > 0 {
			return false, nil
		}
	}

	return true, nil
}

// hibernateNamespace scales all Deployments in the namespace to zero.
func (r *HibernatePolicyReconciler) hibernateNamespace(ctx context.Context, namespace string, action greencostsv1alpha1.HibernateAction) error {
	if !action.ScaleDeploymentsToZero {
		return nil
	}

	var deployList appsv1.DeploymentList
	if err := r.List(ctx, &deployList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing deployments in %q: %w", namespace, err)
	}

	for i := range deployList.Items {
		d := &deployList.Items[i]
		// Skip already-scaled-down deployments.
		if d.Annotations[annotationHibernated] == "true" {
			continue
		}

		replicas := int32(1)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}

		if d.Annotations == nil {
			d.Annotations = map[string]string{}
		}

		d.Annotations[annotationOriginalReplicas] = strconv.Itoa(int(replicas))
		d.Annotations[annotationHibernated] = "true"

		zero := int32(0)
		d.Spec.Replicas = &zero

		if err := r.Update(ctx, d); err != nil {
			return fmt.Errorf("scaling deployment %s/%s to zero: %w", namespace, d.Name, err)
		}
	}

	return nil
}

// wakeNamespace restores original replica counts for Deployments in the namespace.
func (r *HibernatePolicyReconciler) wakeNamespace(ctx context.Context, namespace string) error {
	var deployList appsv1.DeploymentList
	if err := r.List(ctx, &deployList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing deployments in %q: %w", namespace, err)
	}

	for i := range deployList.Items {
		d := &deployList.Items[i]
		if d.Annotations[annotationHibernated] != "true" {
			continue
		}

		origStr, ok := d.Annotations[annotationOriginalReplicas]
		if !ok {
			origStr = "1"
		}

		orig, err := strconv.Atoi(origStr)
		if err != nil {
			orig = 1
		}

		replicas := int32(orig)
		d.Spec.Replicas = &replicas
		delete(d.Annotations, annotationHibernated)
		delete(d.Annotations, annotationOriginalReplicas)

		if err := r.Update(ctx, d); err != nil {
			return fmt.Errorf("restoring deployment %s/%s: %w", namespace, d.Name, err)
		}
	}

	return nil
}

// isNamespaceHibernated returns true if any Deployment in the namespace carries
// the hibernated annotation.
func (r *HibernatePolicyReconciler) isNamespaceHibernated(ctx context.Context, namespace string) (bool, error) {
	var deployList appsv1.DeploymentList
	if err := r.List(ctx, &deployList, client.InNamespace(namespace)); err != nil {
		return false, fmt.Errorf("listing deployments in %q: %w", namespace, err)
	}

	for _, d := range deployList.Items {
		if d.Annotations[annotationHibernated] == "true" {
			return true, nil
		}
	}

	return false, nil
}

// isInIgnoreWindow returns true if now falls within any of the specified ignore
// periods. When inside a window, windowEnd is the time the current window ends.
func isInIgnoreWindow(periods []greencostsv1alpha1.IgnorePeriod, now time.Time) (bool, time.Time) {
	for _, p := range periods {
		loc, err := time.LoadLocation(p.Timezone)
		if err != nil {
			continue
		}

		local := now.In(loc)
		weekday := local.Weekday()

		if !containsWeekday(p.Weekdays, weekday) {
			continue
		}

		from, err := parseHHMM(p.From, local, loc)
		if err != nil {
			continue
		}

		until, err := parseHHMM(p.Until, local, loc)
		if err != nil {
			continue
		}

		if !local.Before(from) && local.Before(until) {
			return true, until
		}
	}

	return false, time.Time{}
}

// nextIgnoreWindowStart returns the next time any ignore window will begin.
func nextIgnoreWindowStart(periods []greencostsv1alpha1.IgnorePeriod, now time.Time) time.Time {
	var earliest time.Time

	for _, p := range periods {
		loc, err := time.LoadLocation(p.Timezone)
		if err != nil {
			continue
		}

		local := now.In(loc)

		for daysAhead := 0; daysAhead <= 7; daysAhead++ {
			candidate := local.AddDate(0, 0, daysAhead)
			if !containsWeekday(p.Weekdays, candidate.Weekday()) {
				continue
			}

			from, err := parseHHMM(p.From, candidate, loc)
			if err != nil {
				continue
			}

			if from.After(now) {
				if earliest.IsZero() || from.Before(earliest) {
					earliest = from
				}
				break
			}
		}
	}

	return earliest
}

func containsWeekday(weekdays []greencostsv1alpha1.Weekday, wd time.Weekday) bool {
	for _, w := range weekdays {
		if weekdayFromSpec(w) == wd {
			return true
		}
	}
	return false
}

func weekdayFromSpec(w greencostsv1alpha1.Weekday) time.Weekday {
	switch w {
	case greencostsv1alpha1.Monday:
		return time.Monday
	case greencostsv1alpha1.Tuesday:
		return time.Tuesday
	case greencostsv1alpha1.Wednesday:
		return time.Wednesday
	case greencostsv1alpha1.Thursday:
		return time.Thursday
	case greencostsv1alpha1.Friday:
		return time.Friday
	case greencostsv1alpha1.Saturday:
		return time.Saturday
	case greencostsv1alpha1.Sunday:
		return time.Sunday
	default:
		return time.Sunday
	}
}

func (r *HibernatePolicyReconciler) setHPErrorCondition(ctx context.Context, hp *greencostsv1alpha1.HibernatePolicy, err error) (ctrl.Result, error) {
	hp.Status.Conditions = setCondition(hp.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             conditionReasonError,
		Message:            err.Error(),
		LastTransitionTime: metav1.Now(),
	})

	if updateErr := r.Status().Update(ctx, hp); updateErr != nil {
		return ctrl.Result{}, fmt.Errorf("updating error condition (original: %w): %v", err, updateErr)
	}

	return ctrl.Result{RequeueAfter: retryShort}, nil
}

// SetupWithManager registers the controller.
func (r *HibernatePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&greencostsv1alpha1.HibernatePolicy{}).
		Named("hibernatepolicy").
		Complete(r)
}

func defaultDuration(d *metav1.Duration, fallback time.Duration) time.Duration {
	if d == nil {
		return fallback
	}
	return d.Duration
}

// Ensure resource package is used for the Quantity comparisons.
var _ = resource.MustParse("0")

// Ensure labels package is used.
var _ = labels.Everything()
