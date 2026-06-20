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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

var _ = Describe("EnergyAwareCronJob Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: testDefaultNamespace, // TODO(user):Modify as needed
		}
		energyawarecronjob := &greencostsv1alpha1.EnergyAwareCronJob{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind EnergyAwareCronJob")
			err := k8sClient.Get(ctx, typeNamespacedName, energyawarecronjob)
			if err != nil && errors.IsNotFound(err) {
				resource := &greencostsv1alpha1.EnergyAwareCronJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: testDefaultNamespace,
					},
					Spec: greencostsv1alpha1.EnergyAwareCronJobSpec{
						EnergyPriceSource: corev1.LocalObjectReference{Name: "prices"},
						EnergyStrategy: greencostsv1alpha1.EnergyStrategySpec{
							Strategy: greencostsv1alpha1.LowestPrice,
						},
						CronJob: batchv1.CronJobSpec{
							Schedule: "0 0 1 1 *",
							JobTemplate: batchv1.JobTemplateSpec{
								Spec: batchv1.JobSpec{
									Template: corev1.PodTemplateSpec{
										Spec: corev1.PodSpec{
											Containers: []corev1.Container{
												{
													Name:  "worker",
													Image: "busybox",
												},
											},
											RestartPolicy: corev1.RestartPolicyOnFailure,
										},
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &greencostsv1alpha1.EnergyAwareCronJob{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance EnergyAwareCronJob")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &EnergyAwareCronJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
