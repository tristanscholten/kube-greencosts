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
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

const (
	annotationOriginalReplicas      = "greencosts.hstr.nl/original-replicas"
	annotationOriginalNodeSelector  = "greencosts.hstr.nl/original-nodeselector"
	annotationHibernated            = "greencosts.hstr.nl/hibernated"
	annotationHibernatedByKind      = "greencosts.hstr.nl/hibernated-by-kind"
	annotationHibernatedByName      = "greencosts.hstr.nl/hibernated-by-name"
	annotationHibernatedByNamespace = "greencosts.hstr.nl/hibernated-by-namespace"
	annotationTrueValue             = "true"

	workloadKindDeployment  = "Deployment"
	workloadKindStatefulSet = "StatefulSet"
	workloadKindDaemonSet   = "DaemonSet"
	workloadKindReplicaSet  = "ReplicaSet"

	defaultOriginalReplicas = 1

	// hibernateNodeSelectorKey is injected into DaemonSet podTemplates to
	// prevent scheduling. No real node should carry this label.
	hibernateNodeSelectorKey   = "greencosts.hstr.nl/hibernate"
	hibernateNodeSelectorValue = annotationTrueValue

	windowCheckInterval = 5 * time.Minute
)

type hibernationOwner struct {
	Kind      string
	Namespace string
	Name      string
}

// HibernatePolicyReconciler reconciles a HibernatePolicy object.
type HibernatePolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=hibernatepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=hibernatepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=hibernatepolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=create;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch

func (r *HibernatePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, retErr error) {
	ctx, span := otel.Tracer(controllerTracer).Start(ctx, "HibernatePolicy.Reconcile",
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

	var hp greencostsv1alpha1.HibernatePolicy
	if err := r.Get(ctx, req.NamespacedName, &hp); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching HibernatePolicy %s: %w", req.NamespacedName, err)
	}
	base := hp.DeepCopy()
	namespace := hp.Namespace
	owner := hibernationOwner{
		Kind:      "HibernatePolicy",
		Namespace: hp.Namespace,
		Name:      hp.Name,
	}

	now := time.Now()
	inWindow, windowEnd := isInAvailabilityWindow(hp.Spec.AvailabilityWindows, now)

	hibernated := []string{}
	var errs []error

	if inWindow {
		log.Info("inside availability window — waking workloads", "namespace", namespace, "windowEnd", windowEnd)
		for _, wt := range hp.Spec.WorkloadTypes {
			if err := r.wakeWorkloadType(ctx, namespace, wt, owner); err != nil {
				errs = append(errs, fmt.Errorf("waking %s: %w", wt, err))
			}
		}
	} else {
		for _, wt := range hp.Spec.WorkloadTypes {
			names, err := r.hibernateWorkloadType(ctx, namespace, wt, hp.Spec.Action, owner)
			if err != nil {
				errs = append(errs, fmt.Errorf("hibernating %s: %w", wt, err))
				continue
			}
			hibernated = append(hibernated, names...)
		}
	}

	// ── Update status ─────────────────────────────────────────────────────────
	if inWindow {
		hp.Status.HibernatedWorkloads = nil
	} else {
		hp.Status.HibernatedWorkloads = hibernated
	}

	hp.Status.Conditions = setCondition(hp.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            fmt.Sprintf("%d workload type(s) managed in namespace %s", len(hp.Spec.WorkloadTypes), namespace),
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Patch(ctx, &hp, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching HibernatePolicy status: %w", err)
	}

	// ── Requeue at next window boundary ───────────────────────────────────────
	requeueAfter := windowCheckInterval
	if inWindow {
		if d := time.Until(windowEnd); d > 0 && d < requeueAfter {
			requeueAfter = d
		}
	} else {
		if nextStart := nextAvailabilityWindowStart(hp.Spec.AvailabilityWindows, now); !nextStart.IsZero() {
			if d := time.Until(nextStart); d > 0 && d < requeueAfter {
				requeueAfter = d
			}
		}
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, errors.Join(errs...)
}

// ── Per-type hibernate/wake ───────────────────────────────────────────────────

func (r *HibernatePolicyReconciler) hibernateWorkloadType(
	ctx context.Context,
	namespace string,
	wt greencostsv1alpha1.WorkloadType,
	action greencostsv1alpha1.HibernateAction,
	owner hibernationOwner,
) ([]string, error) {
	actionSet := action.SleepDaemonSet || action.MaxReplicas != nil
	if !actionSet {
		return nil, nil
	}
	switch wt {
	case greencostsv1alpha1.WorkloadTypeDeployment:
		return r.hibernateDeployments(ctx, namespace, action, owner)
	case greencostsv1alpha1.WorkloadTypeStatefulSet:
		return r.hibernateStatefulSets(ctx, namespace, action, owner)
	case greencostsv1alpha1.WorkloadTypeReplicaSet:
		return r.hibernateReplicaSets(ctx, namespace, action, owner)
	case greencostsv1alpha1.WorkloadTypeDaemonSet:
		if !action.SleepDaemonSet {
			return nil, nil // DaemonSets are only hibernated when sleepDaemonSet is true; MaxReplicas never applies
		}
		return r.hibernateDaemonSets(ctx, namespace, owner)
	}
	return nil, nil
}

func (r *HibernatePolicyReconciler) wakeWorkloadType(
	ctx context.Context,
	namespace string,
	wt greencostsv1alpha1.WorkloadType,
	owner hibernationOwner,
) error {
	switch wt {
	case greencostsv1alpha1.WorkloadTypeDeployment:
		return r.wakeDeployments(ctx, namespace, owner)
	case greencostsv1alpha1.WorkloadTypeStatefulSet:
		return r.wakeStatefulSets(ctx, namespace, owner)
	case greencostsv1alpha1.WorkloadTypeReplicaSet:
		return r.wakeReplicaSets(ctx, namespace, owner)
	case greencostsv1alpha1.WorkloadTypeDaemonSet:
		return r.wakeDaemonSets(ctx, namespace, owner)
	}
	return nil
}

// ── Deployments ───────────────────────────────────────────────────────────────

func (r *HibernatePolicyReconciler) hibernateDeployments(
	ctx context.Context,
	namespace string,
	action greencostsv1alpha1.HibernateAction,
	owner hibernationOwner,
) ([]string, error) {
	var list appsv1.DeploymentList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing Deployments in %q: %w", namespace, err)
	}
	hibernated := []string{}
	for i := range list.Items {
		d := &list.Items[i]
		if d.Annotations[annotationHibernated] == annotationTrueValue {
			if !ownedBy(d.Annotations, owner) {
				continue
			}
			if err := suspendHPAForAction(ctx, r.Client, namespace, workloadKindDeployment, d.Name, action); err != nil {
				return hibernated, err
			}
			hibernated = append(hibernated, workloadKindDeployment+"/"+d.Name)
			continue
		}
		current := int32(1)
		if d.Spec.Replicas != nil {
			current = *d.Spec.Replicas
		}
		target, shouldScale := computeTargetReplicas(action, current)
		if !shouldScale {
			continue
		}
		base := d.DeepCopy()
		if d.Annotations == nil {
			d.Annotations = map[string]string{}
		}
		d.Annotations[annotationOriginalReplicas] = strconv.Itoa(int(current))
		markHibernated(d.Annotations, owner)
		d.Spec.Replicas = &target
		if err := r.Patch(ctx, d, client.MergeFrom(base)); err != nil {
			return hibernated, fmt.Errorf("scaling Deployment %s/%s: %w", namespace, d.Name, err)
		}
		if err := suspendHPA(ctx, r.Client, namespace, workloadKindDeployment, d.Name, target); err != nil {
			return hibernated, err
		}
		hibernated = append(hibernated, workloadKindDeployment+"/"+d.Name)
	}
	return hibernated, nil
}

func (r *HibernatePolicyReconciler) wakeDeployments(
	ctx context.Context,
	namespace string,
	owner hibernationOwner,
) error {
	var list appsv1.DeploymentList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing Deployments in %q: %w", namespace, err)
	}
	for i := range list.Items {
		d := &list.Items[i]
		if d.Annotations[annotationHibernated] != annotationTrueValue {
			continue
		}
		if !ownedBy(d.Annotations, owner) {
			continue
		}
		if err := restoreHPA(ctx, r.Client, namespace, workloadKindDeployment, d.Name); err != nil {
			return err
		}
		orig := parseOriginalReplicas(d.Annotations[annotationOriginalReplicas])
		replicas := int32(orig)
		base := d.DeepCopy()
		d.Spec.Replicas = &replicas
		clearHibernated(d.Annotations)
		delete(d.Annotations, annotationOriginalReplicas)
		if err := r.Patch(ctx, d, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("restoring Deployment %s/%s: %w", namespace, d.Name, err)
		}
	}
	return nil
}

// ── StatefulSets ──────────────────────────────────────────────────────────────

func (r *HibernatePolicyReconciler) hibernateStatefulSets(
	ctx context.Context,
	namespace string,
	action greencostsv1alpha1.HibernateAction,
	owner hibernationOwner,
) ([]string, error) {
	var list appsv1.StatefulSetList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing StatefulSets in %q: %w", namespace, err)
	}
	hibernated := []string{}
	for i := range list.Items {
		s := &list.Items[i]
		if s.Annotations[annotationHibernated] == annotationTrueValue {
			if !ownedBy(s.Annotations, owner) {
				continue
			}
			if err := suspendHPAForAction(ctx, r.Client, namespace, workloadKindStatefulSet, s.Name, action); err != nil {
				return hibernated, err
			}
			hibernated = append(hibernated, workloadKindStatefulSet+"/"+s.Name)
			continue
		}
		current := int32(1)
		if s.Spec.Replicas != nil {
			current = *s.Spec.Replicas
		}
		target, shouldScale := computeTargetReplicas(action, current)
		if !shouldScale {
			continue
		}
		base := s.DeepCopy()
		if s.Annotations == nil {
			s.Annotations = map[string]string{}
		}
		s.Annotations[annotationOriginalReplicas] = strconv.Itoa(int(current))
		markHibernated(s.Annotations, owner)
		s.Spec.Replicas = &target
		if err := r.Patch(ctx, s, client.MergeFrom(base)); err != nil {
			return hibernated, fmt.Errorf("scaling StatefulSet %s/%s: %w", namespace, s.Name, err)
		}
		if err := suspendHPA(ctx, r.Client, namespace, workloadKindStatefulSet, s.Name, target); err != nil {
			return hibernated, err
		}
		hibernated = append(hibernated, workloadKindStatefulSet+"/"+s.Name)
	}
	return hibernated, nil
}

func (r *HibernatePolicyReconciler) wakeStatefulSets(
	ctx context.Context,
	namespace string,
	owner hibernationOwner,
) error {
	var list appsv1.StatefulSetList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing StatefulSets in %q: %w", namespace, err)
	}
	for i := range list.Items {
		s := &list.Items[i]
		if s.Annotations[annotationHibernated] != annotationTrueValue {
			continue
		}
		if !ownedBy(s.Annotations, owner) {
			continue
		}
		if err := restoreHPA(ctx, r.Client, namespace, workloadKindStatefulSet, s.Name); err != nil {
			return err
		}
		orig := parseOriginalReplicas(s.Annotations[annotationOriginalReplicas])
		replicas := int32(orig)
		base := s.DeepCopy()
		s.Spec.Replicas = &replicas
		clearHibernated(s.Annotations)
		delete(s.Annotations, annotationOriginalReplicas)
		if err := r.Patch(ctx, s, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("restoring StatefulSet %s/%s: %w", namespace, s.Name, err)
		}
	}
	return nil
}

// ── ReplicaSets ───────────────────────────────────────────────────────────────

func (r *HibernatePolicyReconciler) hibernateReplicaSets(
	ctx context.Context,
	namespace string,
	action greencostsv1alpha1.HibernateAction,
	owner hibernationOwner,
) ([]string, error) {
	var list appsv1.ReplicaSetList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing ReplicaSets in %q: %w", namespace, err)
	}
	hibernated := []string{}
	for i := range list.Items {
		rs := &list.Items[i]
		// Skip ReplicaSets owned by a Deployment — they are managed by the Deployment controller.
		if isOwnedByDeployment(rs) {
			continue
		}
		if rs.Annotations[annotationHibernated] == annotationTrueValue {
			if !ownedBy(rs.Annotations, owner) {
				continue
			}
			if err := suspendHPAForAction(ctx, r.Client, namespace, workloadKindReplicaSet, rs.Name, action); err != nil {
				return hibernated, err
			}
			hibernated = append(hibernated, workloadKindReplicaSet+"/"+rs.Name)
			continue
		}
		current := int32(1)
		if rs.Spec.Replicas != nil {
			current = *rs.Spec.Replicas
		}
		target, shouldScale := computeTargetReplicas(action, current)
		if !shouldScale {
			continue
		}
		base := rs.DeepCopy()
		if rs.Annotations == nil {
			rs.Annotations = map[string]string{}
		}
		rs.Annotations[annotationOriginalReplicas] = strconv.Itoa(int(current))
		markHibernated(rs.Annotations, owner)
		rs.Spec.Replicas = &target
		if err := r.Patch(ctx, rs, client.MergeFrom(base)); err != nil {
			return hibernated, fmt.Errorf("scaling ReplicaSet %s/%s: %w", namespace, rs.Name, err)
		}
		if err := suspendHPA(ctx, r.Client, namespace, workloadKindReplicaSet, rs.Name, target); err != nil {
			return hibernated, err
		}
		hibernated = append(hibernated, workloadKindReplicaSet+"/"+rs.Name)
	}
	return hibernated, nil
}

func (r *HibernatePolicyReconciler) wakeReplicaSets(
	ctx context.Context,
	namespace string,
	owner hibernationOwner,
) error {
	var list appsv1.ReplicaSetList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing ReplicaSets in %q: %w", namespace, err)
	}
	for i := range list.Items {
		rs := &list.Items[i]
		if isOwnedByDeployment(rs) {
			continue
		}
		if rs.Annotations[annotationHibernated] != annotationTrueValue {
			continue
		}
		if !ownedBy(rs.Annotations, owner) {
			continue
		}
		if err := restoreHPA(ctx, r.Client, namespace, workloadKindReplicaSet, rs.Name); err != nil {
			return err
		}
		orig := parseOriginalReplicas(rs.Annotations[annotationOriginalReplicas])
		replicas := int32(orig)
		base := rs.DeepCopy()
		rs.Spec.Replicas = &replicas
		clearHibernated(rs.Annotations)
		delete(rs.Annotations, annotationOriginalReplicas)
		if err := r.Patch(ctx, rs, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("restoring ReplicaSet %s/%s: %w", namespace, rs.Name, err)
		}
	}
	return nil
}

// ── DaemonSets ────────────────────────────────────────────────────────────────

// hibernateDaemonSets injects a non-schedulable nodeSelector into each DaemonSet's
// pod template so no new pods are scheduled. The original nodeSelector is stored
// as JSON in the annotation greencosts.hstr.nl/original-nodeselector.
func (r *HibernatePolicyReconciler) hibernateDaemonSets(
	ctx context.Context,
	namespace string,
	owner hibernationOwner,
) ([]string, error) {
	var list appsv1.DaemonSetList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing DaemonSets in %q: %w", namespace, err)
	}
	hibernated := []string{}
	for i := range list.Items {
		ds := &list.Items[i]
		if ds.Annotations[annotationHibernated] == annotationTrueValue {
			if !ownedBy(ds.Annotations, owner) {
				continue
			}
			hibernated = append(hibernated, workloadKindDaemonSet+"/"+ds.Name)
			continue
		}
		origNS := ds.Spec.Template.Spec.NodeSelector
		origJSON, err := json.Marshal(origNS)
		if err != nil {
			return hibernated, fmt.Errorf("marshalling nodeSelector for DaemonSet %s/%s: %w", namespace, ds.Name, err)
		}
		base := ds.DeepCopy()
		if ds.Annotations == nil {
			ds.Annotations = map[string]string{}
		}
		ds.Annotations[annotationOriginalNodeSelector] = string(origJSON)
		markHibernated(ds.Annotations, owner)
		ds.Spec.Template.Spec.NodeSelector = map[string]string{
			hibernateNodeSelectorKey: hibernateNodeSelectorValue,
		}
		if err := r.Patch(ctx, ds, client.MergeFrom(base)); err != nil {
			return hibernated, fmt.Errorf("hibernating DaemonSet %s/%s: %w", namespace, ds.Name, err)
		}
		hibernated = append(hibernated, workloadKindDaemonSet+"/"+ds.Name)
	}
	return hibernated, nil
}

func (r *HibernatePolicyReconciler) wakeDaemonSets(
	ctx context.Context,
	namespace string,
	owner hibernationOwner,
) error {
	log := logf.FromContext(ctx)
	var list appsv1.DaemonSetList
	if err := r.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("listing DaemonSets in %q: %w", namespace, err)
	}
	for i := range list.Items {
		ds := &list.Items[i]
		if ds.Annotations[annotationHibernated] != annotationTrueValue {
			continue
		}
		if !ownedBy(ds.Annotations, owner) {
			continue
		}
		origJSON := ds.Annotations[annotationOriginalNodeSelector]
		var origNS map[string]string
		if origJSON != "" {
			if err := json.Unmarshal([]byte(origJSON), &origNS); err != nil {
				log.Error(err, "parsing stored nodeSelector annotation, restoring with nil", "daemonset", ds.Name)
				origNS = nil
			}
		}
		base := ds.DeepCopy()
		ds.Spec.Template.Spec.NodeSelector = origNS
		clearHibernated(ds.Annotations)
		delete(ds.Annotations, annotationOriginalNodeSelector)
		if err := r.Patch(ctx, ds, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("restoring DaemonSet %s/%s: %w", namespace, ds.Name, err)
		}
	}
	return nil
}

// ── Time-window helpers ───────────────────────────────────────────────────────

// isInAvailabilityWindow returns true when now falls within any configured
// availability window. windowEnd is the time the current window ends.
func isInAvailabilityWindow(windows []greencostsv1alpha1.AvailabilityWindow, now time.Time) (bool, time.Time) {
	for _, w := range windows {
		loc, err := time.LoadLocation(w.Timezone)
		if err != nil {
			continue
		}
		local := now.In(loc)
		if !containsWeekday(w.Weekdays, local.Weekday()) {
			continue
		}
		from, err := parseHHMM(w.From, local, loc)
		if err != nil {
			continue
		}
		until, err := parseHHMM(w.Until, local, loc)
		if err != nil {
			continue
		}
		if !local.Before(from) && local.Before(until) {
			return true, until
		}
	}
	return false, time.Time{}
}

// nextAvailabilityWindowStart returns the next time any availability window begins.
func nextAvailabilityWindowStart(windows []greencostsv1alpha1.AvailabilityWindow, now time.Time) time.Time {
	var earliest time.Time
	for _, w := range windows {
		loc, err := time.LoadLocation(w.Timezone)
		if err != nil {
			continue
		}
		local := now.In(loc)
		for daysAhead := 0; daysAhead <= 7; daysAhead++ {
			candidate := local.AddDate(0, 0, daysAhead)
			if !containsWeekday(w.Weekdays, candidate.Weekday()) {
				continue
			}
			from, err := parseHHMM(w.From, candidate, loc)
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

// ── Shared utilities ──────────────────────────────────────────────────────────

func parseOriginalReplicas(s string) int {
	if s == "" {
		return defaultOriginalReplicas
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return defaultOriginalReplicas
	}
	return v
}

func markHibernated(annotations map[string]string, owner hibernationOwner) {
	annotations[annotationHibernated] = annotationTrueValue
	annotations[annotationHibernatedByKind] = owner.Kind
	annotations[annotationHibernatedByName] = owner.Name
	annotations[annotationHibernatedByNamespace] = owner.Namespace
}

func clearHibernated(annotations map[string]string) {
	delete(annotations, annotationHibernated)
	delete(annotations, annotationHibernatedByKind)
	delete(annotations, annotationHibernatedByName)
	delete(annotations, annotationHibernatedByNamespace)
}

func ownedBy(annotations map[string]string, owner hibernationOwner) bool {
	kind := annotations[annotationHibernatedByKind]
	name := annotations[annotationHibernatedByName]
	namespace := annotations[annotationHibernatedByNamespace]
	if kind == "" && name == "" && namespace == "" {
		return true // legacy hibernation marker from versions before ownership annotations
	}
	return kind == owner.Kind && name == owner.Name && namespace == owner.Namespace
}

// isOwnedByDeployment returns true if the ReplicaSet has a Deployment owner reference.
func isOwnedByDeployment(rs *appsv1.ReplicaSet) bool {
	for _, ref := range rs.OwnerReferences {
		if ref.Kind == workloadKindDeployment {
			return true
		}
	}
	return false
}

// SetupWithManager registers the controller.
func (r *HibernatePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&greencostsv1alpha1.HibernatePolicy{}).
		Named("hibernatepolicy").
		Complete(r)
}
