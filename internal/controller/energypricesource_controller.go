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
	"fmt"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/samber/oops"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
	"github.com/tristanscholten/kube-greencosts/internal/providers"
)

const (
	conditionTypeReady   = "Ready"
	conditionReasonReady = "PricesFetched"
	conditionReasonError = "FetchFailed"
)

// EnergyPriceSourceReconciler reconciles a EnergyPriceSource object.
type EnergyPriceSourceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Registry *providers.Registry
}

// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=energypricesources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=energypricesources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=energypricesources/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

func (r *EnergyPriceSourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var eps greencostsv1alpha1.EnergyPriceSource
	if err := r.Get(ctx, req.NamespacedName, &eps); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching EnergyPriceSource %s: %w", req.NamespacedName, err)
	}

	// ── Cache validity check ──────────────────────────────────────────────────
	if eps.Status.LastUpdated != nil {
		expires := eps.Status.LastUpdated.Add(eps.Spec.CacheTTL.Duration)
		if time.Now().Before(expires) {
			nextRefresh := time.Until(expires)
			log.V(1).Info("prices still cached", "expiresIn", nextRefresh)
			return ctrl.Result{RequeueAfter: nextRefresh}, nil
		}
	}

	// ── Cron schedule check ───────────────────────────────────────────────────
	schedule, err := cron.ParseStandard(eps.Spec.RefreshSchedule)
	if err != nil {
		return r.setErrorCondition(ctx, &eps, fmt.Errorf("invalid refreshSchedule %q: %w", eps.Spec.RefreshSchedule, err))
	}

	nextFire := schedule.Next(time.Now().Add(-time.Second))
	if time.Until(nextFire) > time.Minute {
		log.V(1).Info("next cron fire is in the future", "in", time.Until(nextFire))
		return ctrl.Result{RequeueAfter: time.Until(nextFire)}, nil
	}

	// ── Resolve provider token from Secret ───────────────────────────────────
	token, err := r.resolveToken(ctx, &eps)
	if err != nil {
		return r.setErrorCondition(ctx, &eps, err)
	}

	// ── Fetch prices ──────────────────────────────────────────────────────────
	provider, err := r.Registry.Get(eps.Spec.Provider, eps.Spec, token)
	if err != nil {
		return r.setErrorCondition(ctx, &eps, fmt.Errorf("getting provider %q: %w", eps.Spec.Provider, err))
	}

	prices, err := provider.FetchPrices(ctx, providers.FetchPricesRequest{
		BiddingZone: eps.Spec.BiddingZone,
		Currency:    eps.Spec.Currency,
		Date:        time.Now(),
	})
	if err != nil {
		slog.Error("fetching energy prices", "source", req.NamespacedName, "error", err)
		return r.setErrorCondition(ctx, &eps,
			oops.Wrapf(err, "fetching prices from provider %q", eps.Spec.Provider))
	}

	// ── Update status ─────────────────────────────────────────────────────────
	now := metav1.Now()
	eps.Status.LastUpdated = &now
	eps.Status.Prices = prices
	eps.Status.Conditions = setCondition(eps.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             conditionReasonReady,
		Message:            fmt.Sprintf("fetched %d price intervals", len(prices)),
		LastTransitionTime: now,
	})

	if err := r.Status().Update(ctx, &eps); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating EnergyPriceSource status: %w", err)
	}

	log.Info("price data refreshed", "intervals", len(prices))

	nextCronFire := schedule.Next(time.Now())
	return ctrl.Result{RequeueAfter: time.Until(nextCronFire)}, nil
}

// resolveToken reads the provider-specific token from the referenced Secret.
// Returns an empty string when no authentication is configured (customProvider
// without an authSecretRef).
func (r *EnergyPriceSourceReconciler) resolveToken(ctx context.Context, eps *greencostsv1alpha1.EnergyPriceSource) (string, error) {
	switch eps.Spec.Provider {
	case "customProvider":
		cfg := eps.Spec.CustomProviderConfig
		if cfg == nil || cfg.AuthSecretRef == nil {
			return "", nil
		}
		return r.readSecretKey(ctx, eps.Namespace, *cfg.AuthSecretRef)
	case "entsoe":
		if eps.Spec.EntsoeConfig == nil {
			return "", fmt.Errorf("entsoeConfig is required for provider \"entsoe\"")
		}
		return r.readSecretKey(ctx, eps.Namespace, eps.Spec.EntsoeConfig.SecurityTokenRef)
	case "enever":
		if eps.Spec.EneverConfig == nil {
			return "", fmt.Errorf("eneverConfig is required for provider \"enever\"")
		}
		return r.readSecretKey(ctx, eps.Namespace, eps.Spec.EneverConfig.TokenRef)
	default:
		return "", nil
	}
}

// readSecretKey retrieves a single key from a Kubernetes Secret.
func (r *EnergyPriceSourceReconciler) readSecretKey(ctx context.Context, namespace string, ref corev1.SecretKeySelector) (string, error) {
	var secret corev1.Secret
	key := types.NamespacedName{Namespace: namespace, Name: ref.Name}

	if err := r.Get(ctx, key, &secret); err != nil {
		return "", fmt.Errorf("reading secret %s: %w", key, err)
	}

	value, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s", ref.Key, key)
	}

	return string(value), nil
}

func (r *EnergyPriceSourceReconciler) setErrorCondition(ctx context.Context, eps *greencostsv1alpha1.EnergyPriceSource, err error) (ctrl.Result, error) {
	eps.Status.Conditions = setCondition(eps.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             conditionReasonError,
		Message:            err.Error(),
		LastTransitionTime: metav1.Now(),
	})

	if updateErr := r.Status().Update(ctx, eps); updateErr != nil {
		return ctrl.Result{}, fmt.Errorf("updating error condition (original: %w): %v", err, updateErr)
	}

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// SetupWithManager registers the controller with the Manager.
func (r *EnergyPriceSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&greencostsv1alpha1.EnergyPriceSource{}).
		Named("energypricesource").
		Complete(r)
}

// setCondition upserts a condition into the slice by Type.
func setCondition(conditions []metav1.Condition, c metav1.Condition) []metav1.Condition {
	for i, existing := range conditions {
		if existing.Type == c.Type {
			conditions[i] = c
			return conditions
		}
	}
	return append(conditions, c)
}
