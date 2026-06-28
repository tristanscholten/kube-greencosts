package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

var _ = Describe("CRD validation", func() {
	It("rejects EnergyPriceSource provider configs that do not match spec.provider", func() {
		ctx := context.Background()
		eps := &greencostsv1alpha1.EnergyPriceSource{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "invalid-provider-config-",
				Namespace:    testDefaultNamespace,
			},
			Spec: greencostsv1alpha1.EnergyPriceSourceSpec{
				Provider:    providerEntsoe,
				BiddingZone: "NL",
				CacheTTL:    metav1.Duration{Duration: time.Hour},
				Providers: greencostsv1alpha1.ProviderConfig{
					EneverConfig: &greencostsv1alpha1.EneverConfig{
						SecretRef: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "enever-token"},
							Key:                  "token",
						},
					},
				},
			},
		}

		Expect(k8sClient.Create(ctx, eps)).To(MatchError(ContainSubstring(
			"provider entsoe requires exactly providers.entsoeConfig",
		)))
	})
})
