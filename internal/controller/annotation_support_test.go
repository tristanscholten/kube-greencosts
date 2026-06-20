package controller

import (
	"context"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

const (
	testBusinessHoursPolicy = "business-hours"
	testDefaultNamespace    = "default"
	testProdNamespace       = "prod"
	testRunbookURL          = "https://example.com/runbook"
	testStagingNamespace    = "staging"
	testTrainerApp          = "trainer"
)

func TestBuildJobCopiesTemplateLabelsAndAnnotations(t *testing.T) {
	eacj := &greencostsv1alpha1.EnergyAwareCronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nightly",
			Namespace: "jobs",
		},
		Spec: greencostsv1alpha1.EnergyAwareCronJobSpec{
			CronJob: batchv1.CronJobSpec{
				JobTemplate: batchv1.JobTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app.kubernetes.io/name": testTrainerApp,
						},
						Annotations: map[string]string{
							"example.com/runbook": testRunbookURL,
						},
					},
				},
			},
		},
	}

	job := buildJob(eacj, time.Unix(1234, 0))

	if got := job.Labels[ownerLabel]; got != "nightly" {
		t.Fatalf("owner label = %q, want nightly", got)
	}
	if got := job.Labels["app.kubernetes.io/name"]; got != testTrainerApp {
		t.Fatalf("template label = %q, want trainer", got)
	}
	if got := job.Annotations["example.com/runbook"]; got != testRunbookURL {
		t.Fatalf("template annotation = %q, want runbook URL", got)
	}

	job.Labels["app.kubernetes.io/name"] = "mutated"
	job.Annotations["example.com/runbook"] = "mutated"

	if got := eacj.Spec.CronJob.JobTemplate.Labels["app.kubernetes.io/name"]; got != testTrainerApp {
		t.Fatalf("mutating job labels changed template label to %q", got)
	}
	if got := eacj.Spec.CronJob.JobTemplate.Annotations["example.com/runbook"]; got != testRunbookURL {
		t.Fatalf("mutating job annotations changed template annotation to %q", got)
	}
}

func TestClusterHibernatePolicyCollectsAnnotatedResources(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatalf("adding Kubernetes types to scheme: %v", err)
	}
	if err := greencostsv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding greencosts types to scheme: %v", err)
	}

	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: testStagingNamespace,
					Annotations: map[string]string{
						AnnotationClusterHibernatePolicy: testBusinessHoursPolicy,
					},
				},
			},
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: testProdNamespace},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "inherited",
					Namespace: testStagingNamespace,
				},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "overridden",
					Namespace: testStagingNamespace,
					Annotations: map[string]string{
						AnnotationClusterHibernatePolicy: "other-policy",
					},
				},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "direct",
					Namespace: testProdNamespace,
					Annotations: map[string]string{
						AnnotationClusterHibernatePolicy: testBusinessHoursPolicy,
					},
				},
			},
			&appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "database",
					Namespace: testProdNamespace,
					Annotations: map[string]string{
						AnnotationClusterHibernatePolicy: testBusinessHoursPolicy,
					},
				},
			},
			&appsv1.ReplicaSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "owned-by-deployment",
					Namespace: testProdNamespace,
					Annotations: map[string]string{
						AnnotationClusterHibernatePolicy: testBusinessHoursPolicy,
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "apps/v1",
							Kind:       workloadKindDeployment,
							Name:       "owner",
						},
					},
				},
			},
		).
		Build()

	reconciler := &ClusterHibernatePolicyReconciler{Client: c, Scheme: s}
	refs, err := reconciler.collectWorkloads(
		ctx,
		testBusinessHoursPolicy,
		greencostsv1alpha1.ClusterHibernatePolicySpec{},
	)
	if err != nil {
		t.Fatalf("collecting workloads: %v", err)
	}

	got := make([]string, 0, len(refs))
	for _, ref := range refs {
		got = append(got, ref.String())
	}
	slices.Sort(got)

	want := []string{
		"prod/Deployment/direct",
		"prod/StatefulSet/database",
		"staging/Deployment/inherited",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("collected refs = %v, want %v", got, want)
	}
}
