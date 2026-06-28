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
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
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

	providerCustomProvider = "customProvider"
	providerEntsoe         = "entsoe"
	providerEnever         = "enever"
)

// EnergyPriceSourceReconciler reconciles a EnergyPriceSource object.
type EnergyPriceSourceReconciler struct {
	client.Client
	// Reader is a non-cached API reader used for Secret lookups.
	// Using the direct reader avoids requiring cluster-wide list/watch on Secrets.
	Reader   client.Reader
	Scheme   *runtime.Scheme
	Registry *providers.Registry
}

// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=energypricesources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=energypricesources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=greencosts.hstr.nl,resources=energypricesources/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

func (r *EnergyPriceSourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, retErr error) {
	ctx, span := otel.Tracer(controllerTracer).Start(ctx, "EnergyPriceSource.Reconcile",
		trace.WithAttributes(
			attribute.String("k8s.resource.name", req.Name),
			attribute.String("k8s.resource.namespace", req.Namespace),
		))
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	log := logf.FromContext(ctx)

	var eps greencostsv1alpha1.EnergyPriceSource
	if err := r.Get(ctx, req.NamespacedName, &eps); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching EnergyPriceSource %s: %w", req.NamespacedName, err)
	}
	base := eps.DeepCopy()

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
	cronExpr := eps.Spec.RefreshSchedule
	if cronExpr == "" {
		cronExpr = "0 0,6,12,18 * * *"
	}
	schedule, err := cron.ParseStandard(cronExpr)
	if err != nil {
		return r.setErrorCondition(ctx, base, &eps, fmt.Errorf("invalid refreshSchedule %q: %w", cronExpr, err))
	}

	nextFire := schedule.Next(time.Now().Add(-time.Second))
	if time.Until(nextFire) > time.Minute {
		log.V(1).Info("next cron fire is in the future", "in", time.Until(nextFire))
		return ctrl.Result{RequeueAfter: time.Until(nextFire)}, nil
	}

	// ── Resolve provider token from Secret ───────────────────────────────────
	_, tokenSpan := otel.Tracer(controllerTracer).Start(ctx, "EnergyPriceSource.resolveToken")
	token, err := r.resolveToken(ctx, &eps)
	if err != nil {
		tokenSpan.RecordError(err)
		tokenSpan.SetStatus(codes.Error, err.Error())
		tokenSpan.End()
		return r.setErrorCondition(ctx, base, &eps, err)
	}
	tokenSpan.End()

	// ── Fetch prices ──────────────────────────────────────────────────────────
	provider, err := r.Registry.Get(eps.Spec.Provider, eps.Spec, token)
	if err != nil {
		return r.setErrorCondition(ctx, base, &eps, fmt.Errorf("getting provider %q: %w", eps.Spec.Provider, err))
	}

	_, fetchSpan := otel.Tracer(controllerTracer).Start(ctx, "EnergyPriceSource.fetchPrices",
		trace.WithAttributes(attribute.String("provider", eps.Spec.Provider)))
	prices, err := provider.FetchPrices(ctx, providers.FetchPricesRequest{
		BiddingZone: eps.Spec.BiddingZone,
		Date:        time.Now(),
	})
	if err != nil {
		slog.Error("fetching energy prices", "source", req.NamespacedName, "error", err)
		fetchSpan.RecordError(err)
		fetchSpan.SetStatus(codes.Error, err.Error())
		fetchSpan.End()
		return r.setErrorCondition(ctx, base, &eps,
			oops.Wrapf(err, "fetching prices from provider %q", eps.Spec.Provider))
	}
	fetchSpan.End()

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

	_, patchSpan := otel.Tracer(controllerTracer).Start(ctx, "EnergyPriceSource.patchStatus")
	if err := r.Status().Patch(ctx, &eps, client.MergeFrom(base)); err != nil {
		patchSpan.RecordError(err)
		patchSpan.SetStatus(codes.Error, err.Error())
		patchSpan.End()
		return ctrl.Result{}, fmt.Errorf("updating EnergyPriceSource status: %w", err)
	}
	patchSpan.End()

	log.Info("price data refreshed", "intervals", len(prices))

	nextCronFire := schedule.Next(time.Now())
	return ctrl.Result{RequeueAfter: time.Until(nextCronFire)}, nil
}

// resolveToken reads the provider-specific token from the referenced Secret.
// Returns an empty string when no authentication is configured (customProvider
// without an authSecretRef).
func (r *EnergyPriceSourceReconciler) resolveToken(ctx context.Context, eps *greencostsv1alpha1.EnergyPriceSource) (string, error) {
	switch eps.Spec.Provider {
	case providerCustomProvider:
		cfg := eps.Spec.Providers.CustomProviderConfig
		if cfg == nil || cfg.SecretRef == nil {
			return "", nil
		}
		return r.readSecretKey(ctx, eps.Namespace, *cfg.SecretRef)
	case providerEntsoe:
		if eps.Spec.Providers.EntsoeConfig == nil {
			return "", fmt.Errorf("entsoeConfig is required for provider \"entsoe\"")
		}
		return r.readSecretKey(ctx, eps.Namespace, eps.Spec.Providers.EntsoeConfig.SecretRef)
	case providerEnever:
		if eps.Spec.Providers.EneverConfig == nil {
			return "", fmt.Errorf("eneverConfig is required for provider \"enever\"")
		}
		return r.readSecretKey(ctx, eps.Namespace, eps.Spec.Providers.EneverConfig.SecretRef)
	default:
		return "", nil
	}
}

// readSecretKey retrieves a single key from a Kubernetes Secret.
func (r *EnergyPriceSourceReconciler) readSecretKey(ctx context.Context, namespace string, ref corev1.SecretKeySelector) (string, error) {
	var secret corev1.Secret
	key := types.NamespacedName{Namespace: namespace, Name: ref.Name}

	if err := r.Reader.Get(ctx, key, &secret); err != nil {
		return "", fmt.Errorf("reading secret %s: %w", key, err)
	}

	value, ok := secret.Data[ref.Key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s", ref.Key, key)
	}

	return string(value), nil
}

func (r *EnergyPriceSourceReconciler) setErrorCondition(ctx context.Context, base *greencostsv1alpha1.EnergyPriceSource, eps *greencostsv1alpha1.EnergyPriceSource, err error) (ctrl.Result, error) {
	eps.Status.Conditions = setCondition(eps.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             conditionReasonError,
		Message:            err.Error(),
		LastTransitionTime: metav1.Now(),
	})

	if updateErr := r.Status().Patch(ctx, eps, client.MergeFrom(base)); updateErr != nil {
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
