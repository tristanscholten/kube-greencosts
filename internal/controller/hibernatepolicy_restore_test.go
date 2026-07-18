package controller

import (
	"context"
	"testing"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	controllerTestDeploymentName  = "api"
	controllerTestDeploymentCase  = "deployment"
	controllerTestStatefulSetCase = "statefulset"
)

func TestHibernatePolicyReconcilerWakeReplicaWorkloadsFallsBackForInvalidOriginalReplicas(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	owner := hibernationOwner{Kind: controllerTestHibernatePolicyKind, Namespace: testDefaultNamespace, Name: testBusinessHoursPolicy}
	annotations := func() map[string]string {
		got := map[string]string{annotationOriginalReplicas: "not-an-int"}
		markHibernated(got, owner)
		return got
	}
	deploy := deploymentForHibernateTest(controllerTestDeploymentName, 0, annotations())
	stateful := statefulSetForHibernateTest("db", 0, annotations())
	replica := replicaSetForHibernateTest("worker", 0, annotations(), nil)
	deploymentOwnedReplica := replicaSetForHibernateTest("api-rs", 0, annotations(), []metav1.OwnerReference{{Kind: workloadKindDeployment, Name: controllerTestDeploymentName}})
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(deploy, stateful, replica, deploymentOwnedReplica).Build()
	r := &HibernatePolicyReconciler{Client: c, Scheme: s}

	for _, tt := range []struct {
		name string
		kind greencostsv1alpha1.WorkloadType
	}{
		{name: controllerTestDeploymentCase, kind: greencostsv1alpha1.WorkloadTypeDeployment},
		{name: controllerTestStatefulSetCase, kind: greencostsv1alpha1.WorkloadTypeStatefulSet},
		{name: "replicaset skips Deployment-owned siblings", kind: greencostsv1alpha1.WorkloadTypeReplicaSet},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := r.wakeWorkloadType(ctx, testDefaultNamespace, tt.kind, owner); err != nil {
				t.Fatalf("wakeWorkloadType() error = %v", err)
			}
		})
	}

	assertDeploymentReplicas(t, ctx, c, "api", defaultOriginalReplicas)
	assertStatefulSetReplicas(t, ctx, c, "db", defaultOriginalReplicas)
	assertReplicaSetReplicas(t, ctx, c, "worker", defaultOriginalReplicas)
	assertReplicaSetReplicas(t, ctx, c, "api-rs", 0)
	assertNoHibernationAnnotation(t, ctx, c, &appsv1.Deployment{}, "api")
	assertNoHibernationAnnotation(t, ctx, c, &appsv1.StatefulSet{}, "db")
	assertNoHibernationAnnotation(t, ctx, c, &appsv1.ReplicaSet{}, "worker")
}

func TestOwnedByMatchesCurrentAndLegacyHibernationMarkers(t *testing.T) {
	owner := hibernationOwner{Kind: controllerTestHibernatePolicyKind, Namespace: testDefaultNamespace, Name: testBusinessHoursPolicy}

	for _, tt := range []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{name: "legacy marker with no owner fields", annotations: map[string]string{}, want: true},
		{
			name: "matching owner fields",
			annotations: map[string]string{
				annotationHibernatedByKind:      owner.Kind,
				annotationHibernatedByNamespace: owner.Namespace,
				annotationHibernatedByName:      owner.Name,
			},
			want: true,
		},
		{
			name: "partial legacy marker is rejected",
			annotations: map[string]string{
				annotationHibernatedByKind: owner.Kind,
			},
			want: false,
		},
		{
			name: "different owner name is rejected",
			annotations: map[string]string{
				annotationHibernatedByKind:      owner.Kind,
				annotationHibernatedByNamespace: owner.Namespace,
				annotationHibernatedByName:      testOtherName,
			},
			want: false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ownedBy(tt.annotations, owner); got != tt.want {
				t.Fatalf("ownedBy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseOriginalReplicas(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want int
	}{
		{name: "empty uses default", in: "", want: defaultOriginalReplicas},
		{name: "negative uses default", in: "-1", want: defaultOriginalReplicas},
		{name: "invalid uses default", in: "nope", want: defaultOriginalReplicas},
		{name: "zero preserved", in: "0", want: 0},
		{name: "positive preserved", in: "7", want: 7},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseOriginalReplicas(tt.in); got != tt.want {
				t.Fatalf("parseOriginalReplicas(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
