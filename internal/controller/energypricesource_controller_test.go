package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
	"github.com/tristanscholten/kube-greencosts/internal/providers"
)

const (
	testCronEveryMinute = "* * * * *"
	testSecretKey       = "token"
	testSecretName      = "provider-token"
	testSecretValue     = "secret-value"
)

func TestEnergyPriceSourceReconcileFetchesAndStoresPrices(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	eps := energyPriceSourceForController("prices", greencostsv1alpha1.EnergyPriceSourceSpec{
		Provider:        "stub",
		BiddingZone:     "NL",
		RefreshSchedule: testCronEveryMinute,
		CacheTTL:        metav1.Duration{Duration: time.Hour},
	})
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&greencostsv1alpha1.EnergyPriceSource{}).
		WithObjects(eps).
		Build()
	priceTime := metav1.NewTime(time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC))
	provider := &reconcileStubProvider{
		prices: []greencostsv1alpha1.PricePoint{{At: priceTime, EurPerMWh: 12.5}},
	}
	r := &EnergyPriceSourceReconciler{
		Client:   c,
		Reader:   c,
		Registry: registryWithProvider("stub", provider),
	}

	result, err := r.Reconcile(ctx, requestFor(eps))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %s, want positive duration", result.RequeueAfter)
	}
	if provider.gotZone != "NL" {
		t.Fatalf("provider bidding zone = %q, want NL", provider.gotZone)
	}

	var got greencostsv1alpha1.EnergyPriceSource
	if err := c.Get(ctx, client.ObjectKeyFromObject(eps), &got); err != nil {
		t.Fatalf("getting EnergyPriceSource: %v", err)
	}
	if got.Status.LastUpdated == nil {
		t.Fatal("LastUpdated was not set")
	}
	if len(got.Status.Prices) != 1 || got.Status.Prices[0].EurPerMWh != 12.5 {
		t.Fatalf("stored prices = %#v, want one 12.5 price", got.Status.Prices)
	}
	condition := findCondition(got.Status.Conditions, conditionTypeReady)
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != conditionReasonReady {
		t.Fatalf("Ready condition = %#v, want true %s", condition, conditionReasonReady)
	}
}

func TestEnergyPriceSourceReconcileUsesCache(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	lastUpdated := metav1.Now()
	eps := energyPriceSourceForController("cached-prices", greencostsv1alpha1.EnergyPriceSourceSpec{
		Provider:    providerEnergyZero,
		BiddingZone: "NL",
		CacheTTL:    metav1.Duration{Duration: time.Hour},
	})
	eps.Status.LastUpdated = &lastUpdated
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&greencostsv1alpha1.EnergyPriceSource{}).
		WithObjects(eps).
		Build()
	r := &EnergyPriceSourceReconciler{Client: c, Reader: c, Registry: providers.NewRegistry()}

	result, err := r.Reconcile(ctx, requestFor(eps))
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > time.Hour {
		t.Fatalf("RequeueAfter = %s, want remaining cache TTL", result.RequeueAfter)
	}
}

func TestEnergyPriceSourceReconcileRecordsConfigErrors(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	tests := []struct {
		name    string
		spec    greencostsv1alpha1.EnergyPriceSourceSpec
		wantMsg string
	}{
		{
			name: "bad schedule",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Provider:        providerEnergyZero,
				BiddingZone:     "NL",
				RefreshSchedule: "not a cron",
			},
			wantMsg: "invalid refreshSchedule",
		},
		{
			name: "missing entsoe token config",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Provider:        providerEntsoe,
				BiddingZone:     "NL",
				RefreshSchedule: testCronEveryMinute,
			},
			wantMsg: "entsoeConfig is required",
		},
		{
			name: "unknown provider",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Provider:        "missing-provider",
				BiddingZone:     "NL",
				RefreshSchedule: testCronEveryMinute,
			},
			wantMsg: `unknown energy provider "missing-provider"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eps := energyPriceSourceForController(strings.ReplaceAll(tt.name, " ", "-"), tt.spec)
			c := fake.NewClientBuilder().
				WithScheme(s).
				WithStatusSubresource(&greencostsv1alpha1.EnergyPriceSource{}).
				WithObjects(eps).
				Build()
			r := &EnergyPriceSourceReconciler{Client: c, Reader: c, Registry: providers.NewRegistry()}

			result, err := r.Reconcile(ctx, requestFor(eps))
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if result.RequeueAfter != 5*time.Minute {
				t.Fatalf("RequeueAfter = %s, want 5m", result.RequeueAfter)
			}

			var got greencostsv1alpha1.EnergyPriceSource
			if err := c.Get(ctx, client.ObjectKeyFromObject(eps), &got); err != nil {
				t.Fatalf("getting EnergyPriceSource: %v", err)
			}
			condition := findCondition(got.Status.Conditions, conditionTypeReady)
			if condition == nil || condition.Status != metav1.ConditionFalse {
				t.Fatalf("Ready condition = %#v, want false", condition)
			}
			if !strings.Contains(condition.Message, tt.wantMsg) {
				t.Fatalf("Ready message = %q, want containing %q", condition.Message, tt.wantMsg)
			}
		})
	}
}

func TestEnergyPriceSourceResolveToken(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testSecretName, Namespace: testDefaultNamespace},
		Data:       map[string][]byte{testSecretKey: []byte(testSecretValue)},
	}
	r := &EnergyPriceSourceReconciler{
		Reader: fake.NewClientBuilder().WithScheme(s).WithObjects(secret).Build(),
	}

	tests := []struct {
		name    string
		spec    greencostsv1alpha1.EnergyPriceSourceSpec
		want    string
		wantErr string
	}{
		{
			name: "custom provider without secret is public",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Provider: providerCustomProvider,
			},
		},
		{
			name: "custom provider reads optional secret",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Provider: providerCustomProvider,
				Providers: greencostsv1alpha1.ProviderConfig{
					CustomProviderConfig: &greencostsv1alpha1.CustomProviderConfig{
						SecretRef: secretKeyRef(testSecretName, testSecretKey),
					},
				},
			},
			want: testSecretValue,
		},
		{
			name: "entsoe requires config",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Provider: providerEntsoe,
			},
			wantErr: `entsoeConfig is required`,
		},
		{
			name: "entsoe reads configured secret",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Provider: providerEntsoe,
				Providers: greencostsv1alpha1.ProviderConfig{
					EntsoeConfig: &greencostsv1alpha1.EntsoeConfig{
						SecretRef: *secretKeyRef(testSecretName, testSecretKey),
					},
				},
			},
			want: testSecretValue,
		},
		{
			name: "enever requires config",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Provider: providerEnever,
			},
			wantErr: `eneverConfig is required`,
		},
		{
			name: "energyzero is public",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Provider: providerEnergyZero,
			},
		},
		{
			name: "unknown provider defers to registry validation",
			spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Provider: "future-provider",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eps := &greencostsv1alpha1.EnergyPriceSource{
				ObjectMeta: metav1.ObjectMeta{Name: "prices", Namespace: testDefaultNamespace},
				Spec:       tt.spec,
			}

			got, err := r.resolveToken(ctx, eps)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveToken() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveToken() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolveToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnergyPriceSourceReadSecretKey(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	r := &EnergyPriceSourceReconciler{
		Reader: fake.NewClientBuilder().WithScheme(s).WithObjects(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: testSecretName, Namespace: testDefaultNamespace},
			Data:       map[string][]byte{testSecretKey: []byte(testSecretValue)},
		}).Build(),
	}

	tests := []struct {
		name    string
		ref     corev1.SecretKeySelector
		want    string
		wantErr string
	}{
		{
			name: "existing key",
			ref:  *secretKeyRef(testSecretName, testSecretKey),
			want: testSecretValue,
		},
		{
			name:    "missing key names secret",
			ref:     *secretKeyRef(testSecretName, "missing"),
			wantErr: `key "missing" not found in secret default/provider-token`,
		},
		{
			name:    "missing secret names object",
			ref:     *secretKeyRef("missing-secret", testSecretKey),
			wantErr: "reading secret default/missing-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.readSecretKey(ctx, testDefaultNamespace, tt.ref)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("readSecretKey() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("readSecretKey() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("readSecretKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEnergyPriceSourceSetErrorCondition(t *testing.T) {
	ctx := context.Background()
	s := newControllerTestScheme(t)
	eps := &greencostsv1alpha1.EnergyPriceSource{
		ObjectMeta: metav1.ObjectMeta{Name: "prices", Namespace: testDefaultNamespace},
		Spec: greencostsv1alpha1.EnergyPriceSourceSpec{
			Provider:    providerEnergyZero,
			BiddingZone: "NL",
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&greencostsv1alpha1.EnergyPriceSource{}).
		WithObjects(eps).
		Build()
	r := &EnergyPriceSourceReconciler{Client: c}

	result, err := r.setErrorCondition(ctx, eps.DeepCopy(), eps, errForTest("provider down"))
	if err != nil {
		t.Fatalf("setErrorCondition() error = %v", err)
	}
	if result.RequeueAfter != 5*time.Minute {
		t.Fatalf("RequeueAfter = %s, want 5m", result.RequeueAfter)
	}

	var got greencostsv1alpha1.EnergyPriceSource
	if err := c.Get(ctx, client.ObjectKeyFromObject(eps), &got); err != nil {
		t.Fatalf("getting EnergyPriceSource: %v", err)
	}
	condition := findCondition(got.Status.Conditions, conditionTypeReady)
	if condition == nil {
		t.Fatal("Ready condition missing")
	}
	if condition.Status != metav1.ConditionFalse {
		t.Fatalf("Ready status = %s, want False", condition.Status)
	}
	if condition.Reason != conditionReasonError {
		t.Fatalf("Ready reason = %s, want %s", condition.Reason, conditionReasonError)
	}
	if condition.Message != "provider down" {
		t.Fatalf("Ready message = %q, want provider down", condition.Message)
	}
}

func TestSetCondition(t *testing.T) {
	oldReady := metav1.Condition{Type: conditionTypeReady, Status: metav1.ConditionFalse, Reason: "Old"}
	healthy := metav1.Condition{Type: "Healthy", Status: metav1.ConditionTrue, Reason: "ProbeOK"}
	newReady := metav1.Condition{Type: conditionTypeReady, Status: metav1.ConditionTrue, Reason: conditionReasonReady}

	conditions := setCondition([]metav1.Condition{oldReady, healthy}, newReady)
	if len(conditions) != 2 {
		t.Fatalf("condition count = %d, want 2", len(conditions))
	}
	if conditions[0].Reason != conditionReasonReady {
		t.Fatalf("Ready reason = %s, want %s", conditions[0].Reason, conditionReasonReady)
	}
	if conditions[1].Reason != "ProbeOK" {
		t.Fatalf("unrelated condition was replaced: %#v", conditions[1])
	}

	conditions = setCondition(conditions, metav1.Condition{Type: "Synced", Status: metav1.ConditionTrue})
	if len(conditions) != 3 {
		t.Fatalf("condition count after append = %d, want 3", len(conditions))
	}
}

func newControllerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatalf("adding Kubernetes scheme: %v", err)
	}
	if err := greencostsv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("adding greencosts scheme: %v", err)
	}
	return s
}

func secretKeyRef(name string, key string) *corev1.SecretKeySelector {
	return &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: name},
		Key:                  key,
	}
}

type errForTest string

func (e errForTest) Error() string { return string(e) }

func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

func energyPriceSourceForController(name string, spec greencostsv1alpha1.EnergyPriceSourceSpec) *greencostsv1alpha1.EnergyPriceSource {
	return &greencostsv1alpha1.EnergyPriceSource{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testDefaultNamespace},
		Spec:       spec,
	}
}

func requestFor(eps *greencostsv1alpha1.EnergyPriceSource) ctrl.Request {
	return ctrl.Request{NamespacedName: client.ObjectKeyFromObject(eps)}
}

func registryWithProvider(name string, provider providers.EnergyProvider) *providers.Registry {
	registry := providers.NewRegistry()
	registry.Register(name, func(greencostsv1alpha1.EnergyPriceSourceSpec, string) (providers.EnergyProvider, error) {
		return provider, nil
	})
	return registry
}

type reconcileStubProvider struct {
	prices  []greencostsv1alpha1.PricePoint
	gotZone string
}

func (p *reconcileStubProvider) FetchPrices(_ context.Context, req providers.FetchPricesRequest) ([]greencostsv1alpha1.PricePoint, error) {
	p.gotZone = req.BiddingZone
	if len(p.prices) == 0 {
		return nil, fmt.Errorf("no prices configured")
	}
	return p.prices, nil
}
