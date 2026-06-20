package webhook

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

// +kubebuilder:webhook:path=/validate-greencosts-hstr-nl-v1alpha1-hibernatepolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=greencosts.hstr.nl,resources=hibernatepolicies,verbs=create;update,versions=v1alpha1,name=vhibernatepolicy.kb.io,admissionReviewVersions=v1

type HibernatePolicyCustomValidator struct{}

var _ webhook.CustomValidator = &HibernatePolicyCustomValidator{}

func SetupHibernatePolicyWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&greencostsv1alpha1.HibernatePolicy{}).
		WithValidator(&HibernatePolicyCustomValidator{}).
		Complete()
}

func (v *HibernatePolicyCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	hp, ok := obj.(*greencostsv1alpha1.HibernatePolicy)
	if !ok {
		return nil, fmt.Errorf("expected a HibernatePolicy but got %T", obj)
	}
	return nil, validateAvailabilityWindows(hp.Spec.AvailabilityWindows)
}

func (v *HibernatePolicyCustomValidator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	hp, ok := newObj.(*greencostsv1alpha1.HibernatePolicy)
	if !ok {
		return nil, fmt.Errorf("expected a HibernatePolicy but got %T", newObj)
	}
	return nil, validateAvailabilityWindows(hp.Spec.AvailabilityWindows)
}

func (v *HibernatePolicyCustomValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// +kubebuilder:webhook:path=/validate-greencosts-hstr-nl-v1alpha1-clusterhibernatepolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=greencosts.hstr.nl,resources=clusterhibernatepolicies,verbs=create;update,versions=v1alpha1,name=vclusterhibernatepolicy.kb.io,admissionReviewVersions=v1

type ClusterHibernatePolicyCustomValidator struct{}

var _ webhook.CustomValidator = &ClusterHibernatePolicyCustomValidator{}

func SetupClusterHibernatePolicyWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&greencostsv1alpha1.ClusterHibernatePolicy{}).
		WithValidator(&ClusterHibernatePolicyCustomValidator{}).
		Complete()
}

func (v *ClusterHibernatePolicyCustomValidator) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	chp, ok := obj.(*greencostsv1alpha1.ClusterHibernatePolicy)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterHibernatePolicy but got %T", obj)
	}
	return nil, validateAvailabilityWindows(chp.Spec.AvailabilityWindows)
}

func (v *ClusterHibernatePolicyCustomValidator) ValidateUpdate(_ context.Context, _ runtime.Object, newObj runtime.Object) (admission.Warnings, error) {
	chp, ok := newObj.(*greencostsv1alpha1.ClusterHibernatePolicy)
	if !ok {
		return nil, fmt.Errorf("expected a ClusterHibernatePolicy but got %T", newObj)
	}
	return nil, validateAvailabilityWindows(chp.Spec.AvailabilityWindows)
}

func (v *ClusterHibernatePolicyCustomValidator) ValidateDelete(_ context.Context, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateAvailabilityWindows(windows []greencostsv1alpha1.AvailabilityWindow) error {
	for i, window := range windows {
		tz := window.Timezone
		if tz == "" {
			tz = defaultTimezone
		}
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return fmt.Errorf("availabilityWindows[%d].timezone %q is invalid: %w", i, window.Timezone, err)
		}
		from, err := parseWindowTime(window.From, loc)
		if err != nil {
			return fmt.Errorf("availabilityWindows[%d].from %q is invalid: %w", i, window.From, err)
		}
		until, err := parseWindowTime(window.Until, loc)
		if err != nil {
			return fmt.Errorf("availabilityWindows[%d].until %q is invalid: %w", i, window.Until, err)
		}
		if !from.Before(until) {
			return fmt.Errorf("availabilityWindows[%d] must have from before until; overnight windows are not supported", i)
		}
	}
	return nil
}

func parseWindowTime(hhmm string, loc *time.Location) (time.Time, error) {
	t, err := time.ParseInLocation("15:04", hhmm, loc)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}
