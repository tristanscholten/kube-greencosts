package webhook

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

func TestEnergyAwareCronJobValidatorMethods(t *testing.T) {
	validator := &EnergyAwareCronJobCustomValidator{}
	valid := validEnergyAwareCronJobForWebhook()

	if warnings, err := validator.ValidateCreate(context.Background(), valid); err != nil || len(warnings) != 0 {
		t.Fatalf("ValidateCreate(valid) = (%v, %v), want no warnings or error", warnings, err)
	}
	if warnings, err := validator.ValidateUpdate(context.Background(), valid, valid); err != nil || len(warnings) != 0 {
		t.Fatalf("ValidateUpdate(valid) = (%v, %v), want no warnings or error", warnings, err)
	}
	if warnings, err := validator.ValidateDelete(context.Background(), valid); err != nil || len(warnings) != 0 {
		t.Fatalf("ValidateDelete(valid) = (%v, %v), want no warnings or error", warnings, err)
	}

	invalid := validEnergyAwareCronJobForWebhook()
	invalid.Spec.CronJob.Schedule = "not a cron"
	if _, err := validator.ValidateCreate(context.Background(), invalid); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("ValidateCreate(invalid schedule) error = %v, want invalid schedule", err)
	}
	if _, err := validator.ValidateCreate(context.Background(), &corev1.Pod{}); err == nil || !strings.Contains(err.Error(), "expected an EnergyAwareCronJob") {
		t.Fatalf("ValidateCreate(wrong type) error = %v, want type error", err)
	}
	if _, err := validator.ValidateUpdate(context.Background(), valid, &corev1.Pod{}); err == nil || !strings.Contains(err.Error(), "expected an EnergyAwareCronJob") {
		t.Fatalf("ValidateUpdate(wrong type) error = %v, want type error", err)
	}
}

func TestHibernatePolicyValidatorMethods(t *testing.T) {
	validator := &HibernatePolicyCustomValidator{}
	valid := validHibernatePolicyForWebhook()

	if warnings, err := validator.ValidateCreate(context.Background(), valid); err != nil || len(warnings) != 0 {
		t.Fatalf("ValidateCreate(valid) = (%v, %v), want no warnings or error", warnings, err)
	}
	if warnings, err := validator.ValidateUpdate(context.Background(), valid, valid); err != nil || len(warnings) != 0 {
		t.Fatalf("ValidateUpdate(valid) = (%v, %v), want no warnings or error", warnings, err)
	}
	if warnings, err := validator.ValidateDelete(context.Background(), valid); err != nil || len(warnings) != 0 {
		t.Fatalf("ValidateDelete(valid) = (%v, %v), want no warnings or error", warnings, err)
	}

	invalid := validHibernatePolicyForWebhook()
	invalid.Spec.AvailabilityWindows[0].Timezone = "bad/timezone"
	if _, err := validator.ValidateUpdate(context.Background(), valid, invalid); err == nil || !strings.Contains(err.Error(), "timezone") {
		t.Fatalf("ValidateUpdate(invalid timezone) error = %v, want timezone error", err)
	}
	if _, err := validator.ValidateCreate(context.Background(), &corev1.Pod{}); err == nil || !strings.Contains(err.Error(), "expected a HibernatePolicy") {
		t.Fatalf("ValidateCreate(wrong type) error = %v, want type error", err)
	}
	if _, err := validator.ValidateUpdate(context.Background(), valid, &corev1.Pod{}); err == nil || !strings.Contains(err.Error(), "expected a HibernatePolicy") {
		t.Fatalf("ValidateUpdate(wrong type) error = %v, want type error", err)
	}
}

func TestClusterHibernatePolicyValidatorMethods(t *testing.T) {
	validator := &ClusterHibernatePolicyCustomValidator{}
	valid := validClusterHibernatePolicyForWebhook()

	if warnings, err := validator.ValidateCreate(context.Background(), valid); err != nil || len(warnings) != 0 {
		t.Fatalf("ValidateCreate(valid) = (%v, %v), want no warnings or error", warnings, err)
	}
	if warnings, err := validator.ValidateUpdate(context.Background(), valid, valid); err != nil || len(warnings) != 0 {
		t.Fatalf("ValidateUpdate(valid) = (%v, %v), want no warnings or error", warnings, err)
	}
	if warnings, err := validator.ValidateDelete(context.Background(), valid); err != nil || len(warnings) != 0 {
		t.Fatalf("ValidateDelete(valid) = (%v, %v), want no warnings or error", warnings, err)
	}

	invalid := validClusterHibernatePolicyForWebhook()
	invalid.Spec.AvailabilityWindows[0].Until = "08:00"
	if _, err := validator.ValidateCreate(context.Background(), invalid); err == nil || !strings.Contains(err.Error(), "from before until") {
		t.Fatalf("ValidateCreate(invalid window) error = %v, want window ordering error", err)
	}
	if _, err := validator.ValidateCreate(context.Background(), &corev1.Pod{}); err == nil || !strings.Contains(err.Error(), "expected a ClusterHibernatePolicy") {
		t.Fatalf("ValidateCreate(wrong type) error = %v, want type error", err)
	}
	if _, err := validator.ValidateUpdate(context.Background(), valid, &corev1.Pod{}); err == nil || !strings.Contains(err.Error(), "expected a ClusterHibernatePolicy") {
		t.Fatalf("ValidateUpdate(wrong type) error = %v, want type error", err)
	}
}

func validHibernatePolicyForWebhook() *greencostsv1alpha1.HibernatePolicy {
	return &greencostsv1alpha1.HibernatePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "business-hours", Namespace: "default"},
		Spec: greencostsv1alpha1.HibernatePolicySpec{
			WorkloadTypes: []greencostsv1alpha1.WorkloadType{greencostsv1alpha1.WorkloadTypeDeployment},
			AvailabilityWindows: []greencostsv1alpha1.AvailabilityWindow{
				{
					Weekdays: []greencostsv1alpha1.Weekday{greencostsv1alpha1.Monday},
					From:     testWindowStart,
					Until:    testWindowEnd,
					Timezone: testTimezoneUTC,
				},
			},
		},
	}
}

func validClusterHibernatePolicyForWebhook() *greencostsv1alpha1.ClusterHibernatePolicy {
	return &greencostsv1alpha1.ClusterHibernatePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "business-hours"},
		Spec: greencostsv1alpha1.ClusterHibernatePolicySpec{
			IncludedResources: []greencostsv1alpha1.WorkloadType{greencostsv1alpha1.WorkloadTypeDeployment},
			AvailabilityWindows: []greencostsv1alpha1.AvailabilityWindow{
				{
					Weekdays: []greencostsv1alpha1.Weekday{greencostsv1alpha1.Monday},
					From:     testWindowStart,
					Until:    testWindowEnd,
					Timezone: testTimezoneUTC,
				},
			},
		},
	}
}
