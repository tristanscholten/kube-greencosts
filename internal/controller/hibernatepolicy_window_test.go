package controller

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

const (
	controllerTestHibernatePolicyKind = "HibernatePolicy"
	controllerTestWindowFrom          = "09:00"
	controllerTestWindowUntil         = "17:00"
)

func TestComputeTargetReplicas(t *testing.T) {
	max := int32(2)
	tests := []struct {
		name       string
		action     greencostsv1alpha1.HibernateAction
		current    int32
		wantTarget int32
		wantScale  bool
	}{
		{
			name:      "no max replicas leaves workload alone",
			current:   5,
			wantScale: false,
		},
		{
			name: "current above max scales down to cap",
			action: greencostsv1alpha1.HibernateAction{
				MaxReplicas: &max,
			},
			current:    5,
			wantTarget: 2,
			wantScale:  true,
		},
		{
			name: "current equal to max is no-op",
			action: greencostsv1alpha1.HibernateAction{
				MaxReplicas: &max,
			},
			current:   2,
			wantScale: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTarget, gotScale := computeTargetReplicas(tt.action, tt.current)
			if gotTarget != tt.wantTarget || gotScale != tt.wantScale {
				t.Fatalf("computeTargetReplicas() = (%d, %t), want (%d, %t)", gotTarget, gotScale, tt.wantTarget, tt.wantScale)
			}
		})
	}
}

func TestAvailabilityWindowLookup(t *testing.T) {
	amsterdam := "Europe/Amsterdam"
	windows := []greencostsv1alpha1.AvailabilityWindow{
		{
			Weekdays: []greencostsv1alpha1.Weekday{greencostsv1alpha1.Monday},
			From:     controllerTestWindowFrom,
			Until:    controllerTestWindowUntil,
			Timezone: amsterdam,
		},
		{
			Weekdays: []greencostsv1alpha1.Weekday{greencostsv1alpha1.Tuesday},
			From:     "08:30",
			Until:    "10:00",
			Timezone: amsterdam,
		},
		{
			Weekdays: []greencostsv1alpha1.Weekday{greencostsv1alpha1.Monday},
			From:     "bad",
			Until:    controllerTestWindowUntil,
			Timezone: amsterdam,
		},
		{
			Weekdays: []greencostsv1alpha1.Weekday{greencostsv1alpha1.Monday},
			From:     controllerTestWindowFrom,
			Until:    controllerTestWindowUntil,
			Timezone: "Mars/Amsterdam",
		},
	}

	now := time.Date(2026, 7, 6, 8, 0, 0, 0, time.UTC) // Monday 10:00 Amsterdam.
	inWindow, windowEnd := isInAvailabilityWindow(windows, now)
	if !inWindow {
		t.Fatal("isInAvailabilityWindow() = false, want true")
	}
	if got := windowEnd.In(time.UTC); !got.Equal(time.Date(2026, 7, 6, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("window end = %s, want 2026-07-06 15:00 UTC", got)
	}

	next := nextAvailabilityWindowStart(windows, time.Date(2026, 7, 6, 16, 30, 0, 0, time.UTC))
	if got := next.In(time.UTC); !got.Equal(time.Date(2026, 7, 7, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("nextAvailabilityWindowStart() = %s, want 2026-07-07 06:30 UTC", got)
	}
}

func TestAvailabilityWindowLookupSkipsInvalidWindows(t *testing.T) {
	windows := []greencostsv1alpha1.AvailabilityWindow{
		{
			Weekdays: []greencostsv1alpha1.Weekday{greencostsv1alpha1.Monday},
			From:     controllerTestWindowFrom,
			Until:    controllerTestWindowUntil,
			Timezone: "bad/timezone",
		},
		{
			Weekdays: []greencostsv1alpha1.Weekday{greencostsv1alpha1.Tuesday},
			From:     "bad",
			Until:    controllerTestWindowUntil,
			Timezone: "UTC",
		},
	}

	if inWindow, windowEnd := isInAvailabilityWindow(windows, time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)); inWindow || !windowEnd.IsZero() {
		t.Fatalf("isInAvailabilityWindow() = (%t, %s), want false and zero end", inWindow, windowEnd)
	}
	if next := nextAvailabilityWindowStart(windows, time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)); !next.IsZero() {
		t.Fatalf("nextAvailabilityWindowStart() = %s, want zero", next)
	}
}

func TestWeekdayFromSpec(t *testing.T) {
	tests := []struct {
		name string
		spec greencostsv1alpha1.Weekday
		want time.Weekday
	}{
		{name: "monday", spec: greencostsv1alpha1.Monday, want: time.Monday},
		{name: "tuesday", spec: greencostsv1alpha1.Tuesday, want: time.Tuesday},
		{name: "wednesday", spec: greencostsv1alpha1.Wednesday, want: time.Wednesday},
		{name: "thursday", spec: greencostsv1alpha1.Thursday, want: time.Thursday},
		{name: "friday", spec: greencostsv1alpha1.Friday, want: time.Friday},
		{name: "saturday", spec: greencostsv1alpha1.Saturday, want: time.Saturday},
		{name: "sunday", spec: greencostsv1alpha1.Sunday, want: time.Sunday},
		{name: "unknown defaults to sunday", spec: greencostsv1alpha1.Weekday("Funday"), want: time.Sunday},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := weekdayFromSpec(tt.spec); got != tt.want {
				t.Fatalf("weekdayFromSpec(%q) = %s, want %s", tt.spec, got, tt.want)
			}
		})
	}
}

func TestHibernationAnnotations(t *testing.T) {
	owner := hibernationOwner{Kind: controllerTestHibernatePolicyKind, Namespace: testDefaultNamespace, Name: testBusinessHoursPolicy}
	annotations := map[string]string{}

	markHibernated(annotations, owner)
	if annotations[annotationHibernated] != annotationTrueValue {
		t.Fatalf("hibernated marker = %q, want true", annotations[annotationHibernated])
	}
	if !ownedBy(annotations, owner) {
		t.Fatal("ownedBy() = false for matching owner")
	}
	if ownedBy(annotations, hibernationOwner{Kind: controllerTestHibernatePolicyKind, Namespace: testDefaultNamespace, Name: testOtherName}) {
		t.Fatal("ownedBy() = true for different owner")
	}

	clearHibernated(annotations)
	if annotations[annotationHibernated] != "" || annotations[annotationHibernatedByName] != "" {
		t.Fatalf("clearHibernated() left hibernation annotations: %#v", annotations)
	}
}

func TestReplicaSetDeploymentOwnership(t *testing.T) {
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			OwnerReferences: []metav1.OwnerReference{{Kind: workloadKindDeployment, Name: "web"}},
		},
	}
	if !isOwnedByDeployment(rs) {
		t.Fatal("isOwnedByDeployment() = false for Deployment owner")
	}

	rs.OwnerReferences[0].Kind = "StatefulSet"
	if isOwnedByDeployment(rs) {
		t.Fatal("isOwnedByDeployment() = true for non-Deployment owner")
	}
}
