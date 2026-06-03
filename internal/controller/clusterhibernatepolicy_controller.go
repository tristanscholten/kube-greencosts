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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

// AnnotationClusterHibernatePolicy is the annotation placed on workloads or
// namespaces to opt-in to a specific ClusterHibernatePolicy.
const AnnotationClusterHibernatePolicy = "greencosts.hstr.nl/clusterhibernatepolicy"

// workloadRef identifies a single workload across all four supported types.
type workloadRef struct {
	Namespace string
	Kind      string
	Name      string
}

func (w workloadRef) String() string {
	return fmt.Sprintf("%s/%s/%s", w.Namespace, w.Kind, w.Name)
}

// ClusterHibernatePolicyReconciler reconciles a ClusterHibernatePolicy object.
type ClusterHibernatePolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=clusterhibernatepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=clusterhibernatepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=clusterhibernatepolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch;update;patch

func (r *ClusterHibernatePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var chp greencostsv1alpha1.ClusterHibernatePolicy
	if err := r.Get(ctx, req.NamespacedName, &chp); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching ClusterHibernatePolicy %s: %w", req.Name, err)
	}
	base := chp.DeepCopy()

	now := time.Now()
	inWindow, windowEnd := isInAvailabilityWindow(chp.Spec.AvailabilityWindows, now)

	// ── Collect all workloads governed by this policy ─────────────────────────
	refs, err := r.collectWorkloads(ctx, chp.Name)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("collecting workloads for ClusterHibernatePolicy %s: %w", chp.Name, err)
	}

	hibernated := []string{}
	var errs []error

	for _, ref := range refs {
		if inWindow {
			if err := r.wakeWorkload(ctx, ref); err != nil {
				errs = append(errs, fmt.Errorf("waking %s: %w", ref, err))
			}
		} else {
			if !chp.Spec.Action.ScaleToZero {
				continue
			}
			wasHibernated, err := r.hibernateWorkload(ctx, ref)
			if err != nil {
				errs = append(errs, fmt.Errorf("hibernating %s: %w", ref, err))
				continue
			}
			if wasHibernated {
				hibernated = append(hibernated, ref.String())
			}
		}
	}

	// ── Update status ─────────────────────────────────────────────────────────
	if inWindow {
		chp.Status.HibernatedWorkloads = nil
	} else {
		chp.Status.HibernatedWorkloads = hibernated
	}

	chp.Status.Conditions = setCondition(chp.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            fmt.Sprintf("%d workload(s) governed by this policy", len(refs)),
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Patch(ctx, &chp, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching ClusterHibernatePolicy status: %w", err)
	}

	// ── Requeue at next window boundary ───────────────────────────────────────
	requeueAfter := windowCheckInterval
	if inWindow {
		if d := time.Until(windowEnd); d > 0 && d < requeueAfter {
			requeueAfter = d
		}
	} else {
		if nextStart := nextAvailabilityWindowStart(chp.Spec.AvailabilityWindows, now); !nextStart.IsZero() {
			if d := time.Until(nextStart); d > 0 && d < requeueAfter {
				requeueAfter = d
			}
		}
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, errors.Join(errs...)
}

// ── Workload collection ───────────────────────────────────────────────────────

// collectWorkloads returns all workloads that reference this policy via annotation.
// Workloads that are inside an annotated namespace are also included.
// Results are deduplicated by (namespace/kind/name).
func (r *ClusterHibernatePolicyReconciler) collectWorkloads(ctx context.Context, policyName string) ([]workloadRef, error) {
	seen := map[string]struct{}{}
	refs := []workloadRef{}

	add := func(ref workloadRef) {
		if _, ok := seen[ref.String()]; !ok {
			seen[ref.String()] = struct{}{}
			refs = append(refs, ref)
		}
	}

	// ── Namespaces with annotation ────────────────────────────────────────────
	var nsList corev1.NamespaceList
	if err := r.List(ctx, &nsList); err != nil {
		return nil, fmt.Errorf("listing namespaces: %w", err)
	}
	for _, ns := range nsList.Items {
		if ns.Annotations[AnnotationClusterHibernatePolicy] != policyName {
			continue
		}
		nsRefs, err := r.allWorkloadsInNamespace(ctx, ns.Name)
		if err != nil {
			return nil, err
		}
		for _, ref := range nsRefs {
			add(ref)
		}
	}

	// ── Individually annotated Deployments ────────────────────────────────────
	{
		var list appsv1.DeploymentList
		if err := r.List(ctx, &list); err != nil {
			return nil, fmt.Errorf("listing Deployments: %w", err)
		}
		for _, d := range list.Items {
			if d.Annotations[AnnotationClusterHibernatePolicy] == policyName {
				add(workloadRef{Namespace: d.Namespace, Kind: "Deployment", Name: d.Name})
			}
		}
	}

	// ── Individually annotated StatefulSets ───────────────────────────────────
	{
		var list appsv1.StatefulSetList
		if err := r.List(ctx, &list); err != nil {
			return nil, fmt.Errorf("listing StatefulSets: %w", err)
		}
		for _, s := range list.Items {
			if s.Annotations[AnnotationClusterHibernatePolicy] == policyName {
				add(workloadRef{Namespace: s.Namespace, Kind: "StatefulSet", Name: s.Name})
			}
		}
	}

	// ── Individually annotated DaemonSets ─────────────────────────────────────
	{
		var list appsv1.DaemonSetList
		if err := r.List(ctx, &list); err != nil {
			return nil, fmt.Errorf("listing DaemonSets: %w", err)
		}
		for _, ds := range list.Items {
			if ds.Annotations[AnnotationClusterHibernatePolicy] == policyName {
				add(workloadRef{Namespace: ds.Namespace, Kind: "DaemonSet", Name: ds.Name})
			}
		}
	}

	// ── Individually annotated ReplicaSets ────────────────────────────────────
	{
		var list appsv1.ReplicaSetList
		if err := r.List(ctx, &list); err != nil {
			return nil, fmt.Errorf("listing ReplicaSets: %w", err)
		}
		for _, rs := range list.Items {
			if isOwnedByDeployment(&rs) {
				continue
			}
			if rs.Annotations[AnnotationClusterHibernatePolicy] == policyName {
				add(workloadRef{Namespace: rs.Namespace, Kind: "ReplicaSet", Name: rs.Name})
			}
		}
	}

	return refs, nil
}

// allWorkloadsInNamespace returns workloadRefs for all four types in a namespace.
func (r *ClusterHibernatePolicyReconciler) allWorkloadsInNamespace(ctx context.Context, namespace string) ([]workloadRef, error) {
	refs := []workloadRef{}

	var deploys appsv1.DeploymentList
	if err := r.List(ctx, &deploys, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing Deployments in %q: %w", namespace, err)
	}
	for _, d := range deploys.Items {
		refs = append(refs, workloadRef{Namespace: namespace, Kind: "Deployment", Name: d.Name})
	}

	var sts appsv1.StatefulSetList
	if err := r.List(ctx, &sts, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing StatefulSets in %q: %w", namespace, err)
	}
	for _, s := range sts.Items {
		refs = append(refs, workloadRef{Namespace: namespace, Kind: "StatefulSet", Name: s.Name})
	}

	var dss appsv1.DaemonSetList
	if err := r.List(ctx, &dss, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing DaemonSets in %q: %w", namespace, err)
	}
	for _, ds := range dss.Items {
		refs = append(refs, workloadRef{Namespace: namespace, Kind: "DaemonSet", Name: ds.Name})
	}

	var rss appsv1.ReplicaSetList
	if err := r.List(ctx, &rss, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing ReplicaSets in %q: %w", namespace, err)
	}
	for _, rs := range rss.Items {
		if !isOwnedByDeployment(&rs) {
			refs = append(refs, workloadRef{Namespace: namespace, Kind: "ReplicaSet", Name: rs.Name})
		}
	}

	return refs, nil
}

// ── Per-workload hibernate/wake ───────────────────────────────────────────────

// hibernateWorkload scales down a single workload identified by ref.
// Returns true if the workload was (or is already) hibernated.
func (r *ClusterHibernatePolicyReconciler) hibernateWorkload(ctx context.Context, ref workloadRef) (bool, error) {
	switch ref.Kind {
	case "Deployment":
		var d appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &d); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if d.Annotations[annotationHibernated] == "true" {
			return true, nil
		}
		replicas := int32(1)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		if d.Annotations == nil {
			d.Annotations = map[string]string{}
		}
		d.Annotations[annotationOriginalReplicas] = fmt.Sprintf("%d", replicas)
		d.Annotations[annotationHibernated] = "true"
		zero := int32(0)
		d.Spec.Replicas = &zero
		return true, r.Update(ctx, &d)

	case "StatefulSet":
		var s appsv1.StatefulSet
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &s); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if s.Annotations[annotationHibernated] == "true" {
			return true, nil
		}
		replicas := int32(1)
		if s.Spec.Replicas != nil {
			replicas = *s.Spec.Replicas
		}
		if s.Annotations == nil {
			s.Annotations = map[string]string{}
		}
		s.Annotations[annotationOriginalReplicas] = fmt.Sprintf("%d", replicas)
		s.Annotations[annotationHibernated] = "true"
		zero := int32(0)
		s.Spec.Replicas = &zero
		return true, r.Update(ctx, &s)

	case "DaemonSet":
		var ds appsv1.DaemonSet
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &ds); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if ds.Annotations[annotationHibernated] == "true" {
			return true, nil
		}
		origJSON, err := marshalNodeSelector(ds.Spec.Template.Spec.NodeSelector)
		if err != nil {
			return false, err
		}
		if ds.Annotations == nil {
			ds.Annotations = map[string]string{}
		}
		ds.Annotations[annotationOriginalNodeSelector] = origJSON
		ds.Annotations[annotationHibernated] = "true"
		ds.Spec.Template.Spec.NodeSelector = map[string]string{
			hibernateNodeSelectorKey: hibernateNodeSelectorValue,
		}
		return true, r.Update(ctx, &ds)

	case "ReplicaSet":
		var rs appsv1.ReplicaSet
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &rs); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if rs.Annotations[annotationHibernated] == "true" {
			return true, nil
		}
		replicas := int32(1)
		if rs.Spec.Replicas != nil {
			replicas = *rs.Spec.Replicas
		}
		if rs.Annotations == nil {
			rs.Annotations = map[string]string{}
		}
		rs.Annotations[annotationOriginalReplicas] = fmt.Sprintf("%d", replicas)
		rs.Annotations[annotationHibernated] = "true"
		zero := int32(0)
		rs.Spec.Replicas = &zero
		return true, r.Update(ctx, &rs)
	}
	return false, nil
}

// wakeWorkload restores a single workload identified by ref.
func (r *ClusterHibernatePolicyReconciler) wakeWorkload(ctx context.Context, ref workloadRef) error {
	switch ref.Kind {
	case "Deployment":
		var d appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &d); err != nil {
			return client.IgnoreNotFound(err)
		}
		if d.Annotations[annotationHibernated] != "true" {
			return nil
		}
		orig := parseOriginalReplicas(d.Annotations[annotationOriginalReplicas], 1)
		replicas := int32(orig)
		d.Spec.Replicas = &replicas
		delete(d.Annotations, annotationHibernated)
		delete(d.Annotations, annotationOriginalReplicas)
		return r.Update(ctx, &d)

	case "StatefulSet":
		var s appsv1.StatefulSet
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &s); err != nil {
			return client.IgnoreNotFound(err)
		}
		if s.Annotations[annotationHibernated] != "true" {
			return nil
		}
		orig := parseOriginalReplicas(s.Annotations[annotationOriginalReplicas], 1)
		replicas := int32(orig)
		s.Spec.Replicas = &replicas
		delete(s.Annotations, annotationHibernated)
		delete(s.Annotations, annotationOriginalReplicas)
		return r.Update(ctx, &s)

	case "DaemonSet":
		var ds appsv1.DaemonSet
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &ds); err != nil {
			return client.IgnoreNotFound(err)
		}
		if ds.Annotations[annotationHibernated] != "true" {
			return nil
		}
		origNS, err := unmarshalNodeSelector(ds.Annotations[annotationOriginalNodeSelector])
		if err != nil {
			log := logf.FromContext(ctx)
			log.Error(err, "parsing stored nodeSelector annotation, restoring with nil", "daemonset", ds.Name)
			origNS = nil
		}
		ds.Spec.Template.Spec.NodeSelector = origNS
		delete(ds.Annotations, annotationHibernated)
		delete(ds.Annotations, annotationOriginalNodeSelector)
		return r.Update(ctx, &ds)

	case "ReplicaSet":
		var rs appsv1.ReplicaSet
		if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &rs); err != nil {
			return client.IgnoreNotFound(err)
		}
		if rs.Annotations[annotationHibernated] != "true" {
			return nil
		}
		orig := parseOriginalReplicas(rs.Annotations[annotationOriginalReplicas], 1)
		replicas := int32(orig)
		rs.Spec.Replicas = &replicas
		delete(rs.Annotations, annotationHibernated)
		delete(rs.Annotations, annotationOriginalReplicas)
		return r.Update(ctx, &rs)
	}
	return nil
}

// ── nodeSelector marshal helpers ──────────────────────────────────────────────

func marshalNodeSelector(ns map[string]string) (string, error) {
	b, err := json.Marshal(ns)
	if err != nil {
		return "", fmt.Errorf("marshalling nodeSelector: %w", err)
	}
	return string(b), nil
}

func unmarshalNodeSelector(s string) (map[string]string, error) {
	if s == "" {
		return nil, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("unmarshalling nodeSelector: %w", err)
	}
	return m, nil
}

// ── SetupWithManager ──────────────────────────────────────────────────────────

// SetupWithManager registers the controller and sets up watches on all annotated
// resource types so reconciliation is triggered when annotations change.
func (r *ClusterHibernatePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// annotationMapper maps an annotated resource event to the ClusterHibernatePolicy
	// named by the annotation value.
	annotationMapper := handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, obj client.Object) []reconcile.Request {
			name := obj.GetAnnotations()[AnnotationClusterHibernatePolicy]
			if name == "" {
				return nil
			}
			return []reconcile.Request{
				{NamespacedName: types.NamespacedName{Name: name}},
			}
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&greencostsv1alpha1.ClusterHibernatePolicy{}).
		Watches(&appsv1.Deployment{}, annotationMapper).
		Watches(&appsv1.StatefulSet{}, annotationMapper).
		Watches(&appsv1.DaemonSet{}, annotationMapper).
		Watches(&appsv1.ReplicaSet{}, annotationMapper).
		Watches(&corev1.Namespace{}, annotationMapper).
		Named("clusterhibernatepolicy").
		Complete(r)
}
