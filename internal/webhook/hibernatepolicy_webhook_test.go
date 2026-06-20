package webhook

import (
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

const (
	testWindowStart = "09:00"
	testTimezoneUTC = "UTC"
)

func TestValidateAvailabilityWindows(t *testing.T) {
	tests := []struct {
		name    string
		window  greencostsv1alpha1.AvailabilityWindow
		wantErr string
	}{
		{
			name: "valid timezone and daytime window",
			window: greencostsv1alpha1.AvailabilityWindow{
				Weekdays: []greencostsv1alpha1.Weekday{greencostsv1alpha1.Monday},
				From:     testWindowStart,
				Until:    "17:00",
				Timezone: "Europe/Amsterdam",
			},
		},
		{
			name: "invalid timezone",
			window: greencostsv1alpha1.AvailabilityWindow{
				Weekdays: []greencostsv1alpha1.Weekday{greencostsv1alpha1.Monday},
				From:     testWindowStart,
				Until:    "17:00",
				Timezone: "Mars/Amsterdam",
			},
			wantErr: "timezone",
		},
		{
			name: "overnight windows are rejected",
			window: greencostsv1alpha1.AvailabilityWindow{
				Weekdays: []greencostsv1alpha1.Weekday{greencostsv1alpha1.Monday},
				From:     "22:00",
				Until:    "06:00",
				Timezone: testTimezoneUTC,
			},
			wantErr: "overnight",
		},
		{
			name: "equal boundaries are rejected",
			window: greencostsv1alpha1.AvailabilityWindow{
				Weekdays: []greencostsv1alpha1.Weekday{greencostsv1alpha1.Monday},
				From:     testWindowStart,
				Until:    testWindowStart,
				Timezone: testTimezoneUTC,
			},
			wantErr: "from before until",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAvailabilityWindows([]greencostsv1alpha1.AvailabilityWindow{tt.window})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateAvailabilityWindows() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateAvailabilityWindows() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestMinimumCronIntervalCoversDSTSchedule(t *testing.T) {
	eacj := validEnergyAwareCronJobForWebhook()
	tz := "Europe/Amsterdam"
	eacj.Spec.CronJob.TimeZone = &tz
	eacj.Spec.CronJob.Schedule = "0 2 * * *"
	eacj.Spec.EnergyStrategy.ScheduleWindow.Duration = 30 * time.Minute
	eacj.Spec.EnergyStrategy.EstimatedDuration.Duration = 15 * time.Minute

	if _, err := validateEnergyAwareCronJobSpec(eacj); err != nil {
		t.Fatalf("validateEnergyAwareCronJobSpec() DST schedule error = %v", err)
	}
}

func TestValidateEnergyAwareCronJobSpecEdgeSchedules(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*greencostsv1alpha1.EnergyAwareCronJob)
		wantErr string
	}{
		{
			name: "descriptor schedule",
			mutate: func(eacj *greencostsv1alpha1.EnergyAwareCronJob) {
				eacj.Spec.CronJob.Schedule = "@daily"
				eacj.Spec.EnergyStrategy.ScheduleWindow.Duration = time.Hour
			},
		},
		{
			name: "zero schedule window skips cron interval check",
			mutate: func(eacj *greencostsv1alpha1.EnergyAwareCronJob) {
				eacj.Spec.CronJob.Schedule = "*/5 * * * *"
				eacj.Spec.EnergyStrategy.ScheduleWindow.Duration = 0
				eacj.Spec.EnergyStrategy.EstimatedDuration.Duration = 0
			},
		},
		{
			name: "estimated duration exceeds window",
			mutate: func(eacj *greencostsv1alpha1.EnergyAwareCronJob) {
				eacj.Spec.EnergyStrategy.ScheduleWindow.Duration = 10 * time.Minute
				eacj.Spec.EnergyStrategy.EstimatedDuration.Duration = 20 * time.Minute
			},
			wantErr: "must not exceed",
		},
		{
			name: "window exceeds minimum interval",
			mutate: func(eacj *greencostsv1alpha1.EnergyAwareCronJob) {
				eacj.Spec.CronJob.Schedule = "*/5 * * * *"
				eacj.Spec.EnergyStrategy.ScheduleWindow.Duration = 10 * time.Minute
				eacj.Spec.EnergyStrategy.EstimatedDuration.Duration = time.Minute
			},
			wantErr: "minimum interval",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eacj := validEnergyAwareCronJobForWebhook()
			tt.mutate(eacj)

			_, err := validateEnergyAwareCronJobSpec(eacj)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateEnergyAwareCronJobSpec() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateEnergyAwareCronJobSpec() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func validEnergyAwareCronJobForWebhook() *greencostsv1alpha1.EnergyAwareCronJob {
	return &greencostsv1alpha1.EnergyAwareCronJob{
		Spec: greencostsv1alpha1.EnergyAwareCronJobSpec{
			EnergyPriceSource: corev1.LocalObjectReference{Name: "prices"},
			EnergyStrategy: greencostsv1alpha1.EnergyStrategySpec{
				Strategy:          greencostsv1alpha1.LowestPrice,
				EstimatedDuration: metav1.Duration{Duration: 15 * time.Minute},
				ScheduleWindow:    metav1.Duration{Duration: 30 * time.Minute},
			},
			CronJob: batchv1.CronJobSpec{
				Schedule: "0 * * * *",
			},
		},
	}
}
