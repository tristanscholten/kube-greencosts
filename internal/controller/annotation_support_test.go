package controller

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	testWorkerName          = "worker"
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

func TestBuildJobShortensLongNamesAndOwnerLabels(t *testing.T) {
	longName := strings.Repeat("very-long-energy-aware-cronjob-", 9) + "worker"
	eacj := &greencostsv1alpha1.EnergyAwareCronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      longName,
			Namespace: "jobs",
		},
	}

	job := buildJob(eacj, time.Unix(1_781_971_200, 0))

	if errs := validation.IsDNS1123Label(job.Name); len(errs) > 0 {
		t.Fatalf("job name %q is not a valid DNS-1123 label: %v", job.Name, errs)
	}
	if !strings.HasSuffix(job.Name, "-1781971200") {
		t.Fatalf("job name %q does not preserve schedule suffix", job.Name)
	}

	owner := job.Labels[ownerLabel]
	if errs := validation.IsValidLabelValue(owner); len(errs) > 0 {
		t.Fatalf("owner label value %q is invalid: %v", owner, errs)
	}
	if len(owner) > maxKubernetesLabelLength {
		t.Fatalf("owner label value length = %d, want <= %d", len(owner), maxKubernetesLabelLength)
	}
}

func TestSelectPricePoint(t *testing.T) {
	base := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	prices := []greencostsv1alpha1.PricePoint{
		{At: metav1.NewTime(base), EurPerMWh: 50},
		{At: metav1.NewTime(base.Add(time.Hour)), EurPerMWh: -5},
		{At: metav1.NewTime(base.Add(2 * time.Hour)), EurPerMWh: 120},
	}

	tests := []struct {
		name      string
		strategy  greencostsv1alpha1.Strategy
		wantPrice float64
	}{
		{
			name:      "default strategy selects lowest price",
			strategy:  "",
			wantPrice: -5,
		},
		{
			name:      "lowest price",
			strategy:  greencostsv1alpha1.LowestPrice,
			wantPrice: -5,
		},
		{
			name:      "highest price",
			strategy:  greencostsv1alpha1.HighestPrice,
			wantPrice: 120,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectPricePoint(prices, tt.strategy, 0, base.Add(3*time.Hour))
			if err != nil {
				t.Fatalf("selectPricePoint() error = %v", err)
			}
			if got.EurPerMWh != tt.wantPrice {
				t.Fatalf("selected price = %v, want %v", got.EurPerMWh, tt.wantPrice)
			}
		})
	}
}

func TestSelectPricePointScoresEstimatedDuration(t *testing.T) {
	base := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	prices := []greencostsv1alpha1.PricePoint{
		{At: metav1.NewTime(base), EurPerMWh: 100},
		{At: metav1.NewTime(base.Add(15 * time.Minute)), EurPerMWh: 0},
		{At: metav1.NewTime(base.Add(30 * time.Minute)), EurPerMWh: 45},
		{At: metav1.NewTime(base.Add(45 * time.Minute)), EurPerMWh: 45},
		{At: metav1.NewTime(base.Add(time.Hour)), EurPerMWh: 200},
	}

	got, err := selectPricePoint(prices, greencostsv1alpha1.LowestPrice, 30*time.Minute, base.Add(time.Hour))
	if err != nil {
		t.Fatalf("selectPricePoint() error = %v", err)
	}
	if !got.At.Equal(&metav1.Time{Time: base.Add(15 * time.Minute)}) {
		t.Fatalf("selected start = %s, want %s", got.At.Time, base.Add(15*time.Minute))
	}
}

func TestSelectPricePointRequiresCompleteDurationInsideWindow(t *testing.T) {
	base := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	prices := []greencostsv1alpha1.PricePoint{
		{At: metav1.NewTime(base), EurPerMWh: 10},
		{At: metav1.NewTime(base.Add(15 * time.Minute)), EurPerMWh: 20},
	}

	_, err := selectPricePoint(prices, greencostsv1alpha1.LowestPrice, 30*time.Minute, base.Add(30*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "no complete price interval") {
		t.Fatalf("selectPricePoint() error = %v, want incomplete interval error", err)
	}
}

func TestParseHHMMRejectsTrailingText(t *testing.T) {
	_, err := parseHHMM("09:00x", time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC), time.UTC)
	if err == nil {
		t.Fatal("parseHHMM() accepted trailing text")
	}
}

func TestSuspendHPAZeroTargetDetachesAndRestoresScaleTarget(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatalf("adding Kubernetes types to scheme: %v", err)
	}

	minReplicas := int32(2)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWorkerName,
			Namespace: testDefaultNamespace,
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			MinReplicas: &minReplicas,
			MaxReplicas: 5,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       workloadKindDeployment,
				Name:       testWorkerName,
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(hpa).Build()

	if err := suspendHPA(ctx, c, testDefaultNamespace, workloadKindDeployment, testWorkerName, 0); err != nil {
		t.Fatalf("suspendHPA() error = %v", err)
	}

	var suspended autoscalingv2.HorizontalPodAutoscaler
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: testWorkerName}, &suspended); err != nil {
		t.Fatalf("getting suspended HPA: %v", err)
	}
	wantDetachedName := detachedHPATargetName(workloadKindDeployment, testWorkerName)
	if got := suspended.Spec.ScaleTargetRef.Name; got != wantDetachedName {
		t.Fatalf("scale target name = %q, want detached placeholder", got)
	}
	if len(wantDetachedName) > maxKubernetesLabelLength {
		t.Fatalf("detached target name length = %d, want <= %d", len(wantDetachedName), maxKubernetesLabelLength)
	}
	if got := suspended.Spec.MinReplicas; got == nil || *got != 2 {
		t.Fatalf("min replicas changed to %v, want original 2 while detached", got)
	}
	if got := suspended.Spec.MaxReplicas; got != 5 {
		t.Fatalf("max replicas = %d, want original 5 while detached", got)
	}
	if got := suspended.Annotations[annotationOriginalHPATargetName]; got != testWorkerName {
		t.Fatalf("stored original target = %q, want worker", got)
	}

	if err := restoreHPA(ctx, c, testDefaultNamespace, workloadKindDeployment, testWorkerName); err != nil {
		t.Fatalf("restoreHPA() error = %v", err)
	}

	var restored autoscalingv2.HorizontalPodAutoscaler
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: testWorkerName}, &restored); err != nil {
		t.Fatalf("getting restored HPA: %v", err)
	}
	if restored.Spec.ScaleTargetRef.Name != testWorkerName {
		t.Fatalf("restored target name = %q, want worker", restored.Spec.ScaleTargetRef.Name)
	}
	if restored.Annotations[annotationOriginalHPATargetName] != "" {
		t.Fatalf("restore left target annotation behind: %v", restored.Annotations)
	}
}

func TestDetachedHPATargetNameStaysShortForLongWorkloadNames(t *testing.T) {
	longName := strings.Repeat("very-long-deployment-name-", 11) + "worker"
	name := detachedHPATargetName(workloadKindDeployment, longName)

	if len(name) > maxKubernetesLabelLength {
		t.Fatalf("detached target name length = %d, want <= %d", len(name), maxKubernetesLabelLength)
	}
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		t.Fatalf("detached target name %q is not a valid DNS-1123 label: %v", name, errs)
	}
}

func TestWakeDeploymentSkipsWorkloadsOwnedByAnotherPolicy(t *testing.T) {
	ctx := context.Background()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatalf("adding Kubernetes types to scheme: %v", err)
	}

	replicas := int32(0)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testWorkerName,
			Namespace: testDefaultNamespace,
			Annotations: map[string]string{
				annotationHibernated:            annotationTrueValue,
				annotationOriginalReplicas:      "3",
				annotationHibernatedByKind:      "ClusterHibernatePolicy",
				annotationHibernatedByName:      "cluster-sleep",
				annotationHibernatedByNamespace: "",
			},
		},
		Spec: appsv1.DeploymentSpec{Replicas: &replicas},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(deployment).Build()
	r := &HibernatePolicyReconciler{Client: c}

	err := r.wakeDeployments(ctx, testDefaultNamespace, hibernationOwner{
		Kind:      "HibernatePolicy",
		Namespace: testDefaultNamespace,
		Name:      "namespace-sleep",
	})
	if err != nil {
		t.Fatalf("wakeDeployments() error = %v", err)
	}

	var got appsv1.Deployment
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: testWorkerName}, &got); err != nil {
		t.Fatalf("getting deployment: %v", err)
	}
	if got.Spec.Replicas == nil || *got.Spec.Replicas != 0 {
		t.Fatalf("replicas = %v, want still hibernated at 0", got.Spec.Replicas)
	}
	if got.Annotations[annotationHibernated] != annotationTrueValue {
		t.Fatalf("hibernate marker was removed by non-owner: %v", got.Annotations)
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
