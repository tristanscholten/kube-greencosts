package controller

import (
	"context"
	"testing"
	"time"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	controllerTestClusterHibernatePolicyKind = "ClusterHibernatePolicy"
	controllerTestDaemonSetName              = "agents"
	controllerTestDeploymentAPI              = "Deployment/api"
	controllerTestSpotNodePool               = "spot"
	controllerTestSpotNodeSelectorJSON       = `{"nodepool":"spot"}`
	controllerTestUTC                        = "UTC"
)

//nolint:goconst // repeated workload names keep table cases readable.
func TestHibernatePolicyReconcilerHibernatesAndWakesReplicaWorkloads(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	max := int32(1)
	owner := hibernationOwner{Kind: controllerTestHibernatePolicyKind, Namespace: testDefaultNamespace, Name: testBusinessHoursPolicy}

	deploy := deploymentForHibernateTest("api", 5, nil)
	stateful := statefulSetForHibernateTest("db", 4, nil)
	replica := replicaSetForHibernateTest("worker", 3, nil, nil)
	deploymentOwnedReplica := replicaSetForHibernateTest("api-rs", 7, nil, []metav1.OwnerReference{{Kind: workloadKindDeployment, Name: "api"}})
	hpa := hpaForHibernateTest("api-hpa", workloadKindDeployment, "api", 2, 8)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy, stateful, replica, deploymentOwnedReplica, hpa).Build()
	r := &HibernatePolicyReconciler{Client: c, Scheme: s}
	action := greencostsv1alpha1.HibernateAction{MaxReplicas: &max}

	tests := []struct {
		name string
		kind greencostsv1alpha1.WorkloadType
		want []string
	}{
		{name: "deployment", kind: greencostsv1alpha1.WorkloadTypeDeployment, want: []string{controllerTestDeploymentAPI}},
		{name: "statefulset", kind: greencostsv1alpha1.WorkloadTypeStatefulSet, want: []string{"StatefulSet/db"}},
		{name: "replicaset skips Deployment-owned siblings", kind: greencostsv1alpha1.WorkloadTypeReplicaSet, want: []string{"ReplicaSet/worker"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.hibernateWorkloadType(ctx, testDefaultNamespace, tt.kind, action, owner)
			if err != nil {
				t.Fatalf("hibernateWorkloadType() error = %v", err)
			}
			if !stringSlicesEqual(got, tt.want) {
				t.Fatalf("hibernateWorkloadType() = %#v, want %#v", got, tt.want)
			}
		})
	}

	assertDeploymentReplicas(t, ctx, c, "api", max)
	assertStatefulSetReplicas(t, ctx, c, "db", max)
	assertReplicaSetReplicas(t, ctx, c, "worker", max)
	assertReplicaSetReplicas(t, ctx, c, "api-rs", 7)
	assertAPIHibernatedAnnotation(t, ctx, c, &appsv1.Deployment{}, owner)
	assertHPAClamped(t, ctx, c, "api-hpa", max)

	for _, wt := range []greencostsv1alpha1.WorkloadType{
		greencostsv1alpha1.WorkloadTypeDeployment,
		greencostsv1alpha1.WorkloadTypeStatefulSet,
		greencostsv1alpha1.WorkloadTypeReplicaSet,
	} {
		if err := r.wakeWorkloadType(ctx, testDefaultNamespace, wt, owner); err != nil {
			t.Fatalf("wakeWorkloadType(%s) error = %v", wt, err)
		}
	}

	assertDeploymentReplicas(t, ctx, c, "api", 5)
	assertStatefulSetReplicas(t, ctx, c, "db", 4)
	assertReplicaSetReplicas(t, ctx, c, "worker", 3)
	assertNoHibernationAnnotation(t, ctx, c, &appsv1.Deployment{}, "api")
	assertHPARestored(t, ctx, c, "api-hpa", 2, 8)
}

func TestHibernatePolicyReconcilerHibernatesAndWakesDaemonSets(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	owner := hibernationOwner{Kind: controllerTestHibernatePolicyKind, Namespace: testDefaultNamespace, Name: testBusinessHoursPolicy}
	ds := daemonSetForHibernateTest(controllerTestDaemonSetName, map[string]string{"nodepool": controllerTestSpotNodePool})
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(ds).Build()
	r := &HibernatePolicyReconciler{Client: c, Scheme: s}

	got, err := r.hibernateWorkloadType(ctx, testDefaultNamespace, greencostsv1alpha1.WorkloadTypeDaemonSet, greencostsv1alpha1.HibernateAction{SleepDaemonSet: true}, owner)
	if err != nil {
		t.Fatalf("hibernateWorkloadType() error = %v", err)
	}
	if !stringSlicesEqual(got, []string{"DaemonSet/agents"}) {
		t.Fatalf("hibernateWorkloadType() = %#v, want DaemonSet/agents", got)
	}

	var hibernated appsv1.DaemonSet
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: controllerTestDaemonSetName}, &hibernated); err != nil {
		t.Fatalf("getting DaemonSet: %v", err)
	}
	if hibernated.Spec.Template.Spec.NodeSelector[hibernateNodeSelectorKey] != hibernateNodeSelectorValue {
		t.Fatalf("nodeSelector = %#v, want hibernate selector", hibernated.Spec.Template.Spec.NodeSelector)
	}
	if hibernated.Annotations[annotationOriginalNodeSelector] != controllerTestSpotNodeSelectorJSON {
		t.Fatalf("original nodeSelector annotation = %q", hibernated.Annotations[annotationOriginalNodeSelector])
	}

	if err := r.wakeWorkloadType(ctx, testDefaultNamespace, greencostsv1alpha1.WorkloadTypeDaemonSet, owner); err != nil {
		t.Fatalf("wakeWorkloadType() error = %v", err)
	}
	var woke appsv1.DaemonSet
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: controllerTestDaemonSetName}, &woke); err != nil {
		t.Fatalf("getting DaemonSet after wake: %v", err)
	}
	if woke.Spec.Template.Spec.NodeSelector["nodepool"] != controllerTestSpotNodePool || len(woke.Spec.Template.Spec.NodeSelector) != 1 {
		t.Fatalf("restored nodeSelector = %#v, want original", woke.Spec.Template.Spec.NodeSelector)
	}
	if woke.Annotations[annotationHibernated] != "" || woke.Annotations[annotationOriginalNodeSelector] != "" {
		t.Fatalf("wake left hibernation annotations: %#v", woke.Annotations)
	}
}

func TestHibernatePolicyReconcilerSkipsUnsafeNoops(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	max := int32(1)
	owner := hibernationOwner{Kind: controllerTestHibernatePolicyKind, Namespace: testDefaultNamespace, Name: testBusinessHoursPolicy}
	otherOwner := hibernationOwner{Kind: controllerTestHibernatePolicyKind, Namespace: testDefaultNamespace, Name: testOtherName}
	deploy := deploymentForHibernateTest("owned-by-other", 5, map[string]string{})
	markHibernated(deploy.Annotations, otherOwner)
	ds := daemonSetForHibernateTest(controllerTestDaemonSetName, nil)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy, ds).Build()
	r := &HibernatePolicyReconciler{Client: c, Scheme: s}

	got, err := r.hibernateWorkloadType(ctx, testDefaultNamespace, greencostsv1alpha1.WorkloadTypeDeployment, greencostsv1alpha1.HibernateAction{}, owner)
	if err != nil {
		t.Fatalf("hibernateWorkloadType(no action) error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("hibernateWorkloadType(no action) = %#v, want empty", got)
	}

	got, err = r.hibernateWorkloadType(ctx, testDefaultNamespace, greencostsv1alpha1.WorkloadTypeDaemonSet, greencostsv1alpha1.HibernateAction{MaxReplicas: &max}, owner)
	if err != nil {
		t.Fatalf("hibernateWorkloadType(daemonset max only) error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("hibernateWorkloadType(daemonset max only) = %#v, want empty", got)
	}

	got, err = r.hibernateWorkloadType(ctx, testDefaultNamespace, greencostsv1alpha1.WorkloadTypeDeployment, greencostsv1alpha1.HibernateAction{MaxReplicas: &max}, owner)
	if err != nil {
		t.Fatalf("hibernateWorkloadType(other owner) error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("hibernateWorkloadType(other owner) = %#v, want empty", got)
	}
	assertDeploymentReplicas(t, ctx, c, "owned-by-other", 5)
}

func TestHibernatePolicyReconcilerReconcileHibernatesOutsideWindow(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	max := int32(1)
	hp := &greencostsv1alpha1.HibernatePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: testBusinessHoursPolicy, Namespace: testDefaultNamespace},
		Spec: greencostsv1alpha1.HibernatePolicySpec{
			WorkloadTypes: []greencostsv1alpha1.WorkloadType{greencostsv1alpha1.WorkloadTypeDeployment},
			Action:        greencostsv1alpha1.HibernateAction{MaxReplicas: &max},
		},
	}
	deploy := deploymentForHibernateTest("api", 4, nil)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(hp, deploy).WithStatusSubresource(hp).Build()
	r := &HibernatePolicyReconciler{Client: c, Scheme: s}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(hp)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != windowCheckInterval {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, windowCheckInterval)
	}

	assertDeploymentReplicas(t, ctx, c, "api", max)
	assertAPIHibernatedAnnotation(t, ctx, c, &appsv1.Deployment{}, hibernationOwner{
		Kind:      controllerTestHibernatePolicyKind,
		Namespace: testDefaultNamespace,
		Name:      testBusinessHoursPolicy,
	})

	var got greencostsv1alpha1.HibernatePolicy
	if err := c.Get(ctx, client.ObjectKeyFromObject(hp), &got); err != nil {
		t.Fatalf("getting HibernatePolicy: %v", err)
	}
	if !stringSlicesEqual(got.Status.HibernatedWorkloads, []string{controllerTestDeploymentAPI}) {
		t.Fatalf("hibernated workloads = %#v, want Deployment/api", got.Status.HibernatedWorkloads)
	}
	if condition := findCondition(got.Status.Conditions, conditionTypeReady); condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("Ready condition = %#v, want true", condition)
	}
}

func TestHibernatePolicyReconcilerReconcileWakesInsideWindow(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	min := int32(1)
	max := int32(8)
	owner := hibernationOwner{
		Kind:      controllerTestHibernatePolicyKind,
		Namespace: testDefaultNamespace,
		Name:      testBusinessHoursPolicy,
	}
	hp := &greencostsv1alpha1.HibernatePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: testBusinessHoursPolicy, Namespace: testDefaultNamespace},
		Spec: greencostsv1alpha1.HibernatePolicySpec{
			WorkloadTypes:       []greencostsv1alpha1.WorkloadType{greencostsv1alpha1.WorkloadTypeDeployment},
			AvailabilityWindows: []greencostsv1alpha1.AvailabilityWindow{currentAvailabilityWindow()},
		},
		Status: greencostsv1alpha1.HibernatePolicyStatus{HibernatedWorkloads: []string{controllerTestDeploymentAPI}},
	}
	deploy := deploymentForHibernateTest("api", 1, map[string]string{annotationOriginalReplicas: "4"})
	markHibernated(deploy.Annotations, owner)
	hpa := hpaForHibernateTest("api-hpa", workloadKindDeployment, "api", 1, 1)
	hpa.Annotations = map[string]string{
		annotationOriginalHPAMin: "1",
		annotationOriginalHPAMax: "8",
	}
	hpa.Spec.MinReplicas = &min
	hpa.Spec.MaxReplicas = 1
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(hp, deploy, hpa).WithStatusSubresource(hp).Build()
	r := &HibernatePolicyReconciler{Client: c, Scheme: s}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(hp)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > windowCheckInterval {
		t.Fatalf("RequeueAfter = %s, want positive duration up to %s", result.RequeueAfter, windowCheckInterval)
	}

	assertDeploymentReplicas(t, ctx, c, "api", 4)
	assertNoHibernationAnnotation(t, ctx, c, &appsv1.Deployment{}, "api")
	assertHPARestored(t, ctx, c, "api-hpa", min, max)

	var got greencostsv1alpha1.HibernatePolicy
	if err := c.Get(ctx, client.ObjectKeyFromObject(hp), &got); err != nil {
		t.Fatalf("getting HibernatePolicy: %v", err)
	}
	if len(got.Status.HibernatedWorkloads) != 0 {
		t.Fatalf("hibernated workloads = %#v, want empty", got.Status.HibernatedWorkloads)
	}
	if condition := findCondition(got.Status.Conditions, conditionTypeReady); condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("Ready condition = %#v, want true", condition)
	}
}

func TestClusterHibernatePolicyReconcilerReconcileHibernatesAnnotatedWorkloads(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	max := int32(1)
	chp := &greencostsv1alpha1.ClusterHibernatePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: testBusinessHoursPolicy},
		Spec: greencostsv1alpha1.ClusterHibernatePolicySpec{
			Action: greencostsv1alpha1.HibernateAction{MaxReplicas: &max},
		},
	}
	deploy := deploymentForHibernateTest("api", 4, map[string]string{AnnotationClusterHibernatePolicy: testBusinessHoursPolicy})
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(chp, deploy).WithStatusSubresource(chp).Build()
	r := &ClusterHibernatePolicyReconciler{Client: c, Scheme: s}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(chp)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != windowCheckInterval {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, windowCheckInterval)
	}

	assertDeploymentReplicas(t, ctx, c, "api", max)
	assertAPIHibernatedAnnotation(t, ctx, c, &appsv1.Deployment{}, hibernationOwner{
		Kind: controllerTestClusterHibernatePolicyKind,
		Name: testBusinessHoursPolicy,
	})

	var got greencostsv1alpha1.ClusterHibernatePolicy
	if err := c.Get(ctx, client.ObjectKeyFromObject(chp), &got); err != nil {
		t.Fatalf("getting ClusterHibernatePolicy: %v", err)
	}
	if !stringSlicesEqual(got.Status.HibernatedWorkloads, []string{"default/Deployment/api"}) {
		t.Fatalf("hibernated workloads = %#v, want default/Deployment/api", got.Status.HibernatedWorkloads)
	}
	if condition := findCondition(got.Status.Conditions, conditionTypeReady); condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("Ready condition = %#v, want true", condition)
	}
}

func TestClusterHibernatePolicyReconcilerReconcileWakesNamespaceWorkloads(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	owner := hibernationOwner{Kind: controllerTestClusterHibernatePolicyKind, Name: testBusinessHoursPolicy}
	chp := &greencostsv1alpha1.ClusterHibernatePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: testBusinessHoursPolicy},
		Spec: greencostsv1alpha1.ClusterHibernatePolicySpec{
			AvailabilityWindows: []greencostsv1alpha1.AvailabilityWindow{currentAvailabilityWindow()},
		},
		Status: greencostsv1alpha1.ClusterHibernatePolicyStatus{HibernatedWorkloads: []string{"default/DaemonSet/agents"}},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:        testDefaultNamespace,
		Annotations: map[string]string{AnnotationClusterHibernatePolicy: testBusinessHoursPolicy},
	}}
	ds := daemonSetForHibernateTest(controllerTestDaemonSetName, map[string]string{hibernateNodeSelectorKey: hibernateNodeSelectorValue})
	ds.Annotations = map[string]string{annotationOriginalNodeSelector: controllerTestSpotNodeSelectorJSON}
	markHibernated(ds.Annotations, owner)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(chp, ns, ds).WithStatusSubresource(chp).Build()
	r := &ClusterHibernatePolicyReconciler{Client: c, Scheme: s}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(chp)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > windowCheckInterval {
		t.Fatalf("RequeueAfter = %s, want positive duration up to %s", result.RequeueAfter, windowCheckInterval)
	}

	var woke appsv1.DaemonSet
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: controllerTestDaemonSetName}, &woke); err != nil {
		t.Fatalf("getting DaemonSet after wake: %v", err)
	}
	if woke.Spec.Template.Spec.NodeSelector["nodepool"] != controllerTestSpotNodePool || len(woke.Spec.Template.Spec.NodeSelector) != 1 {
		t.Fatalf("restored nodeSelector = %#v, want original", woke.Spec.Template.Spec.NodeSelector)
	}
	if woke.Annotations[annotationHibernated] != "" || woke.Annotations[annotationOriginalNodeSelector] != "" {
		t.Fatalf("wake left hibernation annotations: %#v", woke.Annotations)
	}

	var got greencostsv1alpha1.ClusterHibernatePolicy
	if err := c.Get(ctx, client.ObjectKeyFromObject(chp), &got); err != nil {
		t.Fatalf("getting ClusterHibernatePolicy: %v", err)
	}
	if len(got.Status.HibernatedWorkloads) != 0 {
		t.Fatalf("hibernated workloads = %#v, want empty", got.Status.HibernatedWorkloads)
	}
	if condition := findCondition(got.Status.Conditions, conditionTypeReady); condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("Ready condition = %#v, want true", condition)
	}
}

func TestClusterHibernatePolicyReconcilerHibernatesAndWakesReplicaWorkloads(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	max := int32(1)
	owner := hibernationOwner{Kind: controllerTestClusterHibernatePolicyKind, Name: testBusinessHoursPolicy}
	deploy := deploymentForHibernateTest("api", 6, nil)
	stateful := statefulSetForHibernateTest("cache", 4, nil)
	replica := replicaSetForHibernateTest("worker", 3, nil, nil)
	hpa := hpaForHibernateTest("api-hpa", workloadKindDeployment, "api", 2, 8)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy, stateful, replica, hpa).Build()
	r := &ClusterHibernatePolicyReconciler{Client: c, Scheme: s}
	action := greencostsv1alpha1.HibernateAction{MaxReplicas: &max}

	tests := []struct {
		name string
		ref  workloadRef
	}{
		{name: "deployment", ref: workloadRef{Namespace: testDefaultNamespace, Kind: workloadKindDeployment, Name: "api"}},
		{name: "statefulset", ref: workloadRef{Namespace: testDefaultNamespace, Kind: workloadKindStatefulSet, Name: "cache"}},
		{name: "replicaset", ref: workloadRef{Namespace: testDefaultNamespace, Kind: workloadKindReplicaSet, Name: "worker"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.hibernateWorkload(ctx, tt.ref, action, owner)
			if err != nil {
				t.Fatalf("hibernateWorkload() error = %v", err)
			}
			if !got {
				t.Fatalf("hibernateWorkload() = false, want true")
			}
		})
	}

	assertDeploymentReplicas(t, ctx, c, "api", max)
	assertStatefulSetReplicas(t, ctx, c, "cache", max)
	assertReplicaSetReplicas(t, ctx, c, "worker", max)
	assertAPIHibernatedAnnotation(t, ctx, c, &appsv1.Deployment{}, owner)
	assertHPAClamped(t, ctx, c, "api-hpa", max)

	for _, tt := range tests {
		if err := r.wakeWorkload(ctx, tt.ref, owner); err != nil {
			t.Fatalf("wakeWorkload(%s) error = %v", tt.ref, err)
		}
	}

	assertDeploymentReplicas(t, ctx, c, "api", 6)
	assertStatefulSetReplicas(t, ctx, c, "cache", 4)
	assertReplicaSetReplicas(t, ctx, c, "worker", 3)
	assertNoHibernationAnnotation(t, ctx, c, &appsv1.Deployment{}, "api")
	assertHPARestored(t, ctx, c, "api-hpa", 2, 8)
}

func TestClusterHibernatePolicyReconcilerHibernatesAndWakesDaemonSet(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	owner := hibernationOwner{Kind: controllerTestClusterHibernatePolicyKind, Name: testBusinessHoursPolicy}
	ds := daemonSetForHibernateTest(controllerTestDaemonSetName, map[string]string{"nodepool": controllerTestSpotNodePool})
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(ds).Build()
	r := &ClusterHibernatePolicyReconciler{Client: c, Scheme: s}
	ref := workloadRef{Namespace: testDefaultNamespace, Kind: workloadKindDaemonSet, Name: controllerTestDaemonSetName}

	got, err := r.hibernateWorkload(ctx, ref, greencostsv1alpha1.HibernateAction{SleepDaemonSet: true}, owner)
	if err != nil {
		t.Fatalf("hibernateWorkload() error = %v", err)
	}
	if !got {
		t.Fatalf("hibernateWorkload() = false, want true")
	}

	var hibernated appsv1.DaemonSet
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: controllerTestDaemonSetName}, &hibernated); err != nil {
		t.Fatalf("getting DaemonSet: %v", err)
	}
	if hibernated.Spec.Template.Spec.NodeSelector[hibernateNodeSelectorKey] != hibernateNodeSelectorValue {
		t.Fatalf("nodeSelector = %#v, want hibernate selector", hibernated.Spec.Template.Spec.NodeSelector)
	}
	if hibernated.Annotations[annotationOriginalNodeSelector] != controllerTestSpotNodeSelectorJSON {
		t.Fatalf("original nodeSelector annotation = %q", hibernated.Annotations[annotationOriginalNodeSelector])
	}

	if err := r.wakeWorkload(ctx, ref, owner); err != nil {
		t.Fatalf("wakeWorkload() error = %v", err)
	}
	var woke appsv1.DaemonSet
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: controllerTestDaemonSetName}, &woke); err != nil {
		t.Fatalf("getting DaemonSet after wake: %v", err)
	}
	if woke.Spec.Template.Spec.NodeSelector["nodepool"] != controllerTestSpotNodePool || len(woke.Spec.Template.Spec.NodeSelector) != 1 {
		t.Fatalf("restored nodeSelector = %#v, want original", woke.Spec.Template.Spec.NodeSelector)
	}
	if woke.Annotations[annotationHibernated] != "" || woke.Annotations[annotationOriginalNodeSelector] != "" {
		t.Fatalf("wake left hibernation annotations: %#v", woke.Annotations)
	}
}

func TestClusterHibernatePolicyReconcilerSkipsUnsafeNoops(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	max := int32(1)
	owner := hibernationOwner{Kind: controllerTestClusterHibernatePolicyKind, Name: testBusinessHoursPolicy}
	otherOwner := hibernationOwner{Kind: controllerTestClusterHibernatePolicyKind, Name: testOtherName}
	deploy := deploymentForHibernateTest("owned-by-other", 5, map[string]string{})
	markHibernated(deploy.Annotations, otherOwner)
	ds := daemonSetForHibernateTest("no-daemon-action", nil)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy, ds).Build()
	r := &ClusterHibernatePolicyReconciler{Client: c, Scheme: s}
	deploymentRef := workloadRef{Namespace: testDefaultNamespace, Kind: workloadKindDeployment, Name: "owned-by-other"}
	daemonSetRef := workloadRef{Namespace: testDefaultNamespace, Kind: workloadKindDaemonSet, Name: "no-daemon-action"}

	got, err := r.hibernateWorkload(ctx, deploymentRef, greencostsv1alpha1.HibernateAction{}, owner)
	if err != nil {
		t.Fatalf("hibernateWorkload(no action) error = %v", err)
	}
	if got {
		t.Fatalf("hibernateWorkload(no action) = true, want false")
	}

	got, err = r.hibernateWorkload(ctx, daemonSetRef, greencostsv1alpha1.HibernateAction{MaxReplicas: &max}, owner)
	if err != nil {
		t.Fatalf("hibernateWorkload(daemonset max only) error = %v", err)
	}
	if got {
		t.Fatalf("hibernateWorkload(daemonset max only) = true, want false")
	}

	got, err = r.hibernateWorkload(ctx, deploymentRef, greencostsv1alpha1.HibernateAction{MaxReplicas: &max}, owner)
	if err != nil {
		t.Fatalf("hibernateWorkload(other owner) error = %v", err)
	}
	if got {
		t.Fatalf("hibernateWorkload(other owner) = true, want false")
	}
	assertDeploymentReplicas(t, ctx, c, "owned-by-other", 5)
}

func TestClusterHibernatePolicyReconcilerKeepsOwnedHibernatedDeploymentClamped(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	max := int32(1)
	owner := hibernationOwner{Kind: controllerTestClusterHibernatePolicyKind, Name: testBusinessHoursPolicy}
	deploy := deploymentForHibernateTest("api", 0, map[string]string{annotationOriginalReplicas: "6"})
	markHibernated(deploy.Annotations, owner)
	hpa := hpaForHibernateTest("api-hpa", workloadKindDeployment, "api", 2, 8)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy, hpa).Build()
	r := &ClusterHibernatePolicyReconciler{Client: c, Scheme: s}

	got, err := r.hibernateWorkload(ctx, workloadRef{Namespace: testDefaultNamespace, Kind: workloadKindDeployment, Name: "api"}, greencostsv1alpha1.HibernateAction{MaxReplicas: &max}, owner)
	if err != nil {
		t.Fatalf("hibernateWorkload(already hibernated) error = %v", err)
	}
	if !got {
		t.Fatalf("hibernateWorkload(already hibernated) = false, want true")
	}

	assertDeploymentReplicas(t, ctx, c, "api", 0)
	assertHPAClamped(t, ctx, c, "api-hpa", max)
	assertAPIHibernatedAnnotation(t, ctx, c, &appsv1.Deployment{}, owner)
}

//nolint:goconst // repeated missing name keeps table cases readable.
func TestClusterHibernatePolicyReconcilerIgnoresMissingWorkloads(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	max := int32(1)
	owner := hibernationOwner{Kind: controllerTestClusterHibernatePolicyKind, Name: testBusinessHoursPolicy}
	c := fake.NewClientBuilder().WithScheme(s).Build()
	r := &ClusterHibernatePolicyReconciler{Client: c, Scheme: s}

	tests := []struct {
		name string
		ref  workloadRef
	}{
		{name: "deployment", ref: workloadRef{Namespace: testDefaultNamespace, Kind: workloadKindDeployment, Name: "missing"}},
		{name: "statefulset", ref: workloadRef{Namespace: testDefaultNamespace, Kind: workloadKindStatefulSet, Name: "missing"}},
		{name: "daemonset", ref: workloadRef{Namespace: testDefaultNamespace, Kind: workloadKindDaemonSet, Name: "missing"}},
		{name: "replicaset", ref: workloadRef{Namespace: testDefaultNamespace, Kind: workloadKindReplicaSet, Name: "missing"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.hibernateWorkload(ctx, tt.ref, greencostsv1alpha1.HibernateAction{MaxReplicas: &max, SleepDaemonSet: true}, owner)
			if err != nil {
				t.Fatalf("hibernateWorkload() error = %v", err)
			}
			if got {
				t.Fatalf("hibernateWorkload() = true, want false")
			}
			if err := r.wakeWorkload(ctx, tt.ref, owner); err != nil {
				t.Fatalf("wakeWorkload() error = %v", err)
			}
		})
	}
}

func TestClusterHibernatePolicyReconcilerWakesDaemonSetWithMalformedNodeSelectorAnnotation(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	owner := hibernationOwner{Kind: controllerTestClusterHibernatePolicyKind, Name: testBusinessHoursPolicy}
	ds := daemonSetForHibernateTest(controllerTestDaemonSetName, map[string]string{hibernateNodeSelectorKey: hibernateNodeSelectorValue})
	ds.Annotations = map[string]string{annotationOriginalNodeSelector: "not-json"}
	markHibernated(ds.Annotations, owner)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(ds).Build()
	r := &ClusterHibernatePolicyReconciler{Client: c, Scheme: s}

	if err := r.wakeWorkload(ctx, workloadRef{Namespace: testDefaultNamespace, Kind: workloadKindDaemonSet, Name: controllerTestDaemonSetName}, owner); err != nil {
		t.Fatalf("wakeWorkload() error = %v", err)
	}

	var got appsv1.DaemonSet
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: controllerTestDaemonSetName}, &got); err != nil {
		t.Fatalf("getting DaemonSet: %v", err)
	}
	if got.Spec.Template.Spec.NodeSelector != nil {
		t.Fatalf("nodeSelector = %#v, want nil fallback", got.Spec.Template.Spec.NodeSelector)
	}
	if got.Annotations[annotationHibernated] != "" || got.Annotations[annotationOriginalNodeSelector] != "" {
		t.Fatalf("wake left hibernation annotations: %#v", got.Annotations)
	}
}

func deploymentForHibernateTest(name string, replicas int32, annotations map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testDefaultNamespace, Annotations: annotations},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
	}
}

func statefulSetForHibernateTest(name string, replicas int32, annotations map[string]string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testDefaultNamespace, Annotations: annotations},
		Spec:       appsv1.StatefulSetSpec{Replicas: &replicas},
	}
}

func replicaSetForHibernateTest(name string, replicas int32, annotations map[string]string, ownerRefs []metav1.OwnerReference) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testDefaultNamespace, Annotations: annotations, OwnerReferences: ownerRefs},
		Spec:       appsv1.ReplicaSetSpec{Replicas: &replicas},
	}
}

func daemonSetForHibernateTest(name string, selector map[string]string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testDefaultNamespace},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{NodeSelector: selector},
			},
		},
	}
}

func currentAvailabilityWindow() greencostsv1alpha1.AvailabilityWindow {
	zone := controllerTestUTC
	loc := time.UTC
	if time.Now().In(loc).Hour() == 23 {
		zone = "Pacific/Honolulu"
		loc, _ = time.LoadLocation(zone)
	}
	now := time.Now().In(loc)
	from := now.Add(-time.Hour)
	if from.Day() != now.Day() {
		from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	}

	return greencostsv1alpha1.AvailabilityWindow{
		From:     from.Format("15:04"),
		Until:    now.Add(time.Hour).Format("15:04"),
		Timezone: zone,
		Weekdays: []greencostsv1alpha1.Weekday{weekdaySpec(now.Weekday())},
	}
}

func weekdaySpec(day time.Weekday) greencostsv1alpha1.Weekday {
	switch day {
	case time.Monday:
		return greencostsv1alpha1.Monday
	case time.Tuesday:
		return greencostsv1alpha1.Tuesday
	case time.Wednesday:
		return greencostsv1alpha1.Wednesday
	case time.Thursday:
		return greencostsv1alpha1.Thursday
	case time.Friday:
		return greencostsv1alpha1.Friday
	case time.Saturday:
		return greencostsv1alpha1.Saturday
	default:
		return greencostsv1alpha1.Sunday
	}
}

//nolint:unparam // fixed current callers, parameter keeps helper useful for focused HPA cases.
func hpaForHibernateTest(name, targetKind, targetName string, minReplicas, maxReplicas int32) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testDefaultNamespace},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MinReplicas: &minReplicas,
			MaxReplicas: maxReplicas,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: testAppsV1,
				Kind:       targetKind,
				Name:       targetName,
			},
		},
	}
}

func assertDeploymentReplicas(t *testing.T, ctx context.Context, c client.Client, name string, want int32) {
	t.Helper()
	var got appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: name}, &got); err != nil {
		t.Fatalf("getting Deployment %s: %v", name, err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != want {
		t.Fatalf("Deployment %s replicas = %v, want %d", name, got.Spec.Replicas, want)
	}
}

func assertStatefulSetReplicas(t *testing.T, ctx context.Context, c client.Client, name string, want int32) {
	t.Helper()
	var got appsv1.StatefulSet
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: name}, &got); err != nil {
		t.Fatalf("getting StatefulSet %s: %v", name, err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != want {
		t.Fatalf("StatefulSet %s replicas = %v, want %d", name, got.Spec.Replicas, want)
	}
}

func assertReplicaSetReplicas(t *testing.T, ctx context.Context, c client.Client, name string, want int32) {
	t.Helper()
	var got appsv1.ReplicaSet
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: name}, &got); err != nil {
		t.Fatalf("getting ReplicaSet %s: %v", name, err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != want {
		t.Fatalf("ReplicaSet %s replicas = %v, want %d", name, got.Spec.Replicas, want)
	}
}

func assertAPIHibernatedAnnotation(t *testing.T, ctx context.Context, c client.Client, obj client.Object, owner hibernationOwner) {
	t.Helper()
	obj.SetNamespace(testDefaultNamespace)
	obj.SetName("api")
	if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		t.Fatalf("getting %T api: %v", obj, err)
	}
	if obj.GetAnnotations()[annotationHibernated] != annotationTrueValue || !ownedBy(obj.GetAnnotations(), owner) {
		t.Fatalf("hibernation annotations = %#v, want owned by %#v", obj.GetAnnotations(), owner)
	}
}

func assertNoHibernationAnnotation(t *testing.T, ctx context.Context, c client.Client, obj client.Object, name string) {
	t.Helper()
	obj.SetNamespace(testDefaultNamespace)
	obj.SetName(name)
	if err := c.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		t.Fatalf("getting %T %s: %v", obj, name, err)
	}
	if obj.GetAnnotations()[annotationHibernated] != "" || obj.GetAnnotations()[annotationOriginalReplicas] != "" {
		t.Fatalf("hibernation annotations remain: %#v", obj.GetAnnotations())
	}
}

func assertHPAClamped(t *testing.T, ctx context.Context, c client.Client, name string, want int32) {
	t.Helper()
	var got autoscalingv2.HorizontalPodAutoscaler
	if err := c.Get(ctx, types.NamespacedName{Namespace: testDefaultNamespace, Name: name}, &got); err != nil {
		t.Fatalf("getting HPA %s: %v", name, err)
	}
	if got.Spec.MinReplicas == nil || *got.Spec.MinReplicas != want || got.Spec.MaxReplicas != want {
		t.Fatalf("HPA replicas = min %v max %d, want %d", got.Spec.MinReplicas, got.Spec.MaxReplicas, want)
	}
	if got.Annotations[annotationOriginalHPAMin] != "2" || got.Annotations[annotationOriginalHPAMax] != "8" {
		t.Fatalf("HPA original annotations = %#v, want original min/max", got.Annotations)
	}
}

func assertHPARestored(t *testing.T, ctx context.Context, c client.Client, name string, wantMin int32, wantMax int32) {
	t.Helper()
	var got autoscalingv2.HorizontalPodAutoscaler
	if err := c.Get(ctx, types.NamespacedName{Namespace: testDefaultNamespace, Name: name}, &got); err != nil {
		t.Fatalf("getting HPA %s: %v", name, err)
	}
	if got.Spec.MinReplicas == nil || *got.Spec.MinReplicas != wantMin || got.Spec.MaxReplicas != wantMax {
		t.Fatalf("HPA replicas = min %v max %d, want min %d max %d", got.Spec.MinReplicas, got.Spec.MaxReplicas, wantMin, wantMax)
	}
	if got.Annotations[annotationOriginalHPAMin] != "" || got.Annotations[annotationOriginalHPAMax] != "" {
		t.Fatalf("HPA restore annotations remain: %#v", got.Annotations)
	}
}

func stringSlicesEqual(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
