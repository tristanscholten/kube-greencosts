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

// Package webhook contains admission webhook handlers for the greencosts.hstr.nl API group.
package webhook

import (
	"context"
	"fmt"
	"strings"
	"time"

	cron "github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

// +kubebuilder:webhook:path=/validate-greencosts-hstr-nl-v1alpha1-energyawarecronjob,mutating=false,failurePolicy=fail,sideEffects=None,groups=greencosts.hstr.nl,resources=energyawarecronjobs,verbs=create;update,versions=v1alpha1,name=venergyawarecronjob.kb.io,admissionReviewVersions=v1

// EnergyAwareCronJobCustomValidator validates EnergyAwareCronJob resources.
type EnergyAwareCronJobCustomValidator struct{}

var _ webhook.CustomValidator = &EnergyAwareCronJobCustomValidator{}

// SetupEnergyAwareCronJobWebhookWithManager registers the validating webhook with the manager.
func SetupEnergyAwareCronJobWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&greencostsv1alpha1.EnergyAwareCronJob{}).
		WithValidator(&EnergyAwareCronJobCustomValidator{}).
		Complete()
}

func (v *EnergyAwareCronJobCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	eacj, ok := obj.(*greencostsv1alpha1.EnergyAwareCronJob)
	if !ok {
		return nil, fmt.Errorf("expected an EnergyAwareCronJob but got %T", obj)
	}
	return validateEnergyAwareCronJobSpec(eacj)
}

func (v *EnergyAwareCronJobCustomValidator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	eacj, ok := newObj.(*greencostsv1alpha1.EnergyAwareCronJob)
	if !ok {
		return nil, fmt.Errorf("expected an EnergyAwareCronJob but got %T", newObj)
	}
	return validateEnergyAwareCronJobSpec(eacj)
}

func (v *EnergyAwareCronJobCustomValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// validateEnergyAwareCronJobSpec checks that:
//  1. estimatedDuration ≤ scheduleWindow
//  2. scheduleWindow ≤ minimum interval between any two consecutive cron firings
func validateEnergyAwareCronJobSpec(eacj *greencostsv1alpha1.EnergyAwareCronJob) (admission.Warnings, error) {
	strategy := eacj.Spec.EnergyStrategy
	scheduleWindow := strategy.ScheduleWindow.Duration
	estimatedDuration := strategy.EstimatedDuration.Duration

	// 1. estimatedDuration must not exceed scheduleWindow.
	if estimatedDuration > scheduleWindow {
		return nil, fmt.Errorf(
			"energyStrategy.estimatedDuration (%s) must not exceed energyStrategy.scheduleWindow (%s)",
			estimatedDuration, scheduleWindow,
		)
	}

	// 2. scheduleWindow must not exceed the minimum interval between cron firings.
	//    A zero scheduleWindow means "fire at cron time with no optimisation window";
	//    no cron-interval check is needed in that case.
	if scheduleWindow == 0 {
		return nil, nil
	}

	scheduleStr := eacj.Spec.CronJob.Schedule
	if scheduleStr == "" {
		return nil, fmt.Errorf("spec.cronJob.schedule must not be empty")
	}

	// Mirror the controller's timezone handling: prepend TZ= unless the
	// schedule already carries its own timezone prefix.
	if !strings.HasPrefix(scheduleStr, "TZ=") && !strings.HasPrefix(scheduleStr, "CRON_TZ=") {
		tz := "UTC"
		if eacj.Spec.CronJob.TimeZone != nil && *eacj.Spec.CronJob.TimeZone != "" {
			tz = *eacj.Spec.CronJob.TimeZone
		}
		scheduleStr = fmt.Sprintf("TZ=%s %s", tz, scheduleStr)
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	sched, err := parser.Parse(scheduleStr)
	if err != nil {
		return nil, fmt.Errorf("spec.cronJob.schedule %q is invalid: %w", eacj.Spec.CronJob.Schedule, err)
	}

	minInterval := minimumCronInterval(sched)
	if scheduleWindow > minInterval {
		return nil, fmt.Errorf(
			"energyStrategy.scheduleWindow (%s) exceeds the minimum interval between schedule firings (%s) for schedule %q; "+
				"the window would overlap the next cron occurrence causing schedule occurrences to be missed",
			scheduleWindow, minInterval, eacj.Spec.CronJob.Schedule,
		)
	}

	return nil, nil
}

// minimumCronInterval samples 1000 consecutive occurrences of sched starting
// from a fixed reference time and returns the smallest gap between any two
// adjacent firings. The reference is fixed (not time.Now()) so results are
// deterministic and independent of when the webhook runs.
func minimumCronInterval(sched cron.Schedule) time.Duration {
	const samples = 1000
	// Fixed reference: 2026-01-01 00:00:00 UTC — a Monday, covers all weekday patterns.
	ref := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	min := time.Duration(1<<63 - 1) // math.MaxInt64 as Duration
	prev := sched.Next(ref)
	for i := 1; i < samples; i++ {
		next := sched.Next(prev)
		if d := next.Sub(prev); d < min {
			min = d
		}
		prev = next
	}
	return min
}
