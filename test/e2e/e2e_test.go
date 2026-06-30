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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/tristanscholten/kube-greencosts/test/utils"
)

// namespace where the project is deployed in
const namespace = "kube-greencosts-system"

// serviceAccountName created for the project
const serviceAccountName = "kube-greencosts-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "kube-greencosts-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "kube-greencosts-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("removing any previous manager namespace")
		cmd := exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found", "--wait=true")
		_, _ = utils.Run(cmd)

		By("creating manager namespace")
		cmd = exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Pod description:\n%s", podDescription)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to describe controller pod: %s", err)
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			ensureMetricsAccess()

			By("validating that the metrics service is available")
			cmd := exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := scrapeMetrics("curl-metrics")
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
		})

		It("should fetch custom prices and create an EnergyAwareCronJob Job", func() {
			const testNamespace = "kube-greencosts-e2e-prices"
			createNamespace(testNamespace)
			DeferCleanup(deleteNamespace, testNamespace)

			priceData := customPriceJSON(time.Now().UTC())
			applyYAML(testNamespace, fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: custom-price-api
data:
  index.html: '%s'
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: custom-price-api
spec:
  replicas: 1
  selector:
    matchLabels:
      app: custom-price-api
  template:
    metadata:
      labels:
        app: custom-price-api
    spec:
      containers:
      - name: httpd
        image: busybox:1.36
        command: ["sh", "-c", "mkdir -p /www && cp /config/index.html /www/index.html && httpd -f -p 8080 -h /www"]
        ports:
        - containerPort: 8080
        volumeMounts:
        - name: data
          mountPath: /config
      volumes:
      - name: data
        configMap:
          name: custom-price-api
---
apiVersion: v1
kind: Service
metadata:
  name: custom-price-api
spec:
  selector:
    app: custom-price-api
  ports:
  - name: http
    port: 8080
    targetPort: 8080
`, priceData))

			waitFor("custom price API", func(g Gomega) {
				output := kubectl("get", "deployment", "custom-price-api", "-n", testNamespace,
					"-o", "jsonpath={.status.availableReplicas}")
				g.Expect(output).To(Equal("1"))
			})

			applyYAML(testNamespace, `
apiVersion: greencosts.hstr.nl/v1alpha1
kind: EnergyPriceSource
metadata:
  name: custom-prices
spec:
  provider: customProvider
  biddingZone: TEST
  refreshSchedule: "* * * * *"
  cacheTTL: 0s
  providers:
    customProviderConfig:
      url: http://custom-price-api.kube-greencosts-e2e-prices.svc.cluster.local:8080
---
apiVersion: greencosts.hstr.nl/v1alpha1
kind: EnergyAwareCronJob
metadata:
  name: audit-immediate
spec:
  energyPriceSource:
    name: custom-prices
  energyStrategy:
    strategy: HighestPrice
    estimatedDuration: 0s
    scheduleWindow: 0s
  cronJob:
    schedule: "* * * * *"
    successfulJobsHistoryLimit: 2
    failedJobsHistoryLimit: 1
    jobTemplate:
      metadata:
        annotations:
          audit.greencosts.hstr.nl/template: preserved
      spec:
        template:
          spec:
            containers:
            - name: test
              image: busybox:1.36
              command: ["sh", "-c", "echo eacj-ok"]
            restartPolicy: Never
`)

			waitFor("custom EnergyPriceSource", func(g Gomega) {
				condition := kubectl("get", "eps", "custom-prices", "-n", testNamespace,
					"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
				prices := kubectl("get", "eps", "custom-prices", "-n", testNamespace,
					"-o", "jsonpath={.status.prices[*].eurPerMWh}")
				g.Expect(condition).To(Equal("True"))
				g.Expect(prices).To(ContainSubstring("120"))
			})

			metricsOutput := scrapeMetrics("curl-eps-metrics")
			Expect(metricsOutput).To(ContainSubstring("kube_greencosts_energy_price_source_info"))
			Expect(metricsOutput).To(ContainSubstring("kube_greencosts_energy_price_source_price_points"))
			Expect(metricsOutput).To(ContainSubstring("kube_greencosts_energy_price_source_current_price_eur_per_mwh"))
			Expect(metricsOutput).To(ContainSubstring(`namespace="kube-greencosts-e2e-prices"`))
			Expect(metricsOutput).To(ContainSubstring(`name="custom-prices"`))
			Expect(metricsOutput).To(ContainSubstring(`provider="customProvider"`))
			Expect(metricsOutput).To(ContainSubstring(`bidding_zone="TEST"`))

			var jobName string
			Eventually(func(g Gomega) {
				jobName = kubectl("get", "jobs", "-n", testNamespace,
					"-l", "greencosts.hstr.nl/owner=audit-immediate",
					"-o", "go-template={{ range .items }}{{ .metadata.name }}{{ end }}")
				g.Expect(jobName).NotTo(BeEmpty())
			}, 100*time.Second, time.Second).Should(Succeed())

			waitFor("EnergyAwareCronJob Job completion", func(g Gomega) {
				complete := kubectl("get", "job", jobName, "-n", testNamespace,
					"-o", `jsonpath={.status.conditions[?(@.type=="Complete")].status}`)
				annotation := kubectl("get", "job", jobName, "-n", testNamespace,
					"-o", `jsonpath={.metadata.annotations.audit\.greencosts\.hstr\.nl/template}`)
				g.Expect(complete).To(Equal("True"))
				g.Expect(annotation).To(Equal("preserved"))
			})
		})

		It("should hibernate and wake workloads with a zero-cap HPA", func() {
			const testNamespace = "kube-greencosts-e2e-hibernate"
			createNamespace(testNamespace)
			DeferCleanup(deleteNamespace, testNamespace)

			applyYAML(testNamespace, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: hp-deploy
spec:
  replicas: 3
  selector:
    matchLabels:
      app: hp-deploy
  template:
    metadata:
      labels:
        app: hp-deploy
    spec:
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.10
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: hp-deploy
spec:
  minReplicas: 2
  maxReplicas: 5
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: hp-deploy
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 50
---
apiVersion: greencosts.hstr.nl/v1alpha1
kind: HibernatePolicy
metadata:
  name: hp-all
spec:
  workloadTypes: [Deployment]
  action:
    maxReplicas: 0
`)

			waitFor("hibernate deployment and detach HPA", func(g Gomega) {
				replicas := kubectl("get", "deployment", "hp-deploy", "-n", testNamespace,
					"-o", `jsonpath={.spec.replicas}`)
				target := kubectl("get", "hpa", "hp-deploy", "-n", testNamespace,
					"-o", `jsonpath={.spec.scaleTargetRef.name}`)
				originalTarget := kubectl("get", "hpa", "hp-deploy", "-n", testNamespace,
					"-o", `jsonpath={.metadata.annotations.greencosts\.hstr\.nl/original-hpa-target-name}`)
				g.Expect(replicas).To(Equal("0"))
				g.Expect(target).To(HavePrefix("kube-greencosts-hibernated-"))
				g.Expect(target).NotTo(Equal("hp-deploy"))
				g.Expect(len(target)).To(BeNumerically("<=", 63))
				g.Expect(originalTarget).To(Equal("hp-deploy"))
			})

			kubectl("patch", "hibernatepolicy", "hp-all", "-n", testNamespace,
				"--type=merge", "-p", currentAvailabilityWindowPatch())

			waitFor("wake deployment and restore HPA", func(g Gomega) {
				replicas := kubectl("get", "deployment", "hp-deploy", "-n", testNamespace,
					"-o", `jsonpath={.spec.replicas}`)
				target := kubectl("get", "hpa", "hp-deploy", "-n", testNamespace,
					"-o", `jsonpath={.spec.scaleTargetRef.name}`)
				originalTarget := kubectl("get", "hpa", "hp-deploy", "-n", testNamespace,
					"-o", `jsonpath={.metadata.annotations.greencosts\.hstr\.nl/original-hpa-target-name}`)
				g.Expect(replicas).To(Equal("3"))
				g.Expect(target).To(Equal("hp-deploy"))
				g.Expect(originalTarget).To(BeEmpty())
			})
		})

		It("should hibernate and wake non-Deployment workloads", func() {
			const testNamespace = "kube-greencosts-e2e-workloads"
			createNamespace(testNamespace)
			DeferCleanup(deleteNamespace, testNamespace)

			applyYAML(testNamespace, `
apiVersion: v1
kind: Service
metadata:
  name: hp-stateful
spec:
  clusterIP: None
  selector:
    app: hp-stateful
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: hp-stateful
spec:
  serviceName: hp-stateful
  replicas: 2
  selector:
    matchLabels:
      app: hp-stateful
  template:
    metadata:
      labels:
        app: hp-stateful
    spec:
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.10
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: hp-daemon
spec:
  selector:
    matchLabels:
      app: hp-daemon
  template:
    metadata:
      labels:
        app: hp-daemon
    spec:
      nodeSelector:
        kubernetes.io/os: linux
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.10
---
apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: hp-replicaset
spec:
  replicas: 2
  selector:
    matchLabels:
      app: hp-replicaset
  template:
    metadata:
      labels:
        app: hp-replicaset
    spec:
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.10
---
apiVersion: greencosts.hstr.nl/v1alpha1
kind: HibernatePolicy
metadata:
  name: hp-workloads
spec:
  workloadTypes: [StatefulSet, DaemonSet, ReplicaSet]
  action:
    sleepDaemonSet: true
    maxReplicas: 0
`)

			waitFor("hibernate non-Deployment workloads", func(g Gomega) {
				statefulReplicas := kubectl("get", "statefulset", "hp-stateful", "-n", testNamespace,
					"-o", `jsonpath={.spec.replicas}`)
				replicaSetReplicas := kubectl("get", "replicaset", "hp-replicaset", "-n", testNamespace,
					"-o", `jsonpath={.spec.replicas}`)
				daemonSelector := kubectl("get", "daemonset", "hp-daemon", "-n", testNamespace,
					"-o", `jsonpath={.spec.template.spec.nodeSelector.greencosts\.hstr\.nl/hibernate}`)
				daemonOriginalSelector := kubectl("get", "daemonset", "hp-daemon", "-n", testNamespace,
					"-o", `jsonpath={.metadata.annotations.greencosts\.hstr\.nl/original-nodeselector}`)
				status := kubectl("get", "hibernatepolicy", "hp-workloads", "-n", testNamespace,
					"-o", `jsonpath={.status.hibernatedWorkloads}`)
				g.Expect(statefulReplicas).To(Equal("0"))
				g.Expect(replicaSetReplicas).To(Equal("0"))
				g.Expect(daemonSelector).To(Equal("true"))
				g.Expect(daemonOriginalSelector).To(ContainSubstring("kubernetes.io/os"))
				g.Expect(status).To(ContainSubstring("StatefulSet/hp-stateful"))
				g.Expect(status).To(ContainSubstring("DaemonSet/hp-daemon"))
				g.Expect(status).To(ContainSubstring("ReplicaSet/hp-replicaset"))
			})

			kubectl("patch", "hibernatepolicy", "hp-workloads", "-n", testNamespace,
				"--type=merge", "-p", currentAvailabilityWindowPatch())

			waitFor("wake non-Deployment workloads", func(g Gomega) {
				statefulReplicas := kubectl("get", "statefulset", "hp-stateful", "-n", testNamespace,
					"-o", `jsonpath={.spec.replicas}`)
				replicaSetReplicas := kubectl("get", "replicaset", "hp-replicaset", "-n", testNamespace,
					"-o", `jsonpath={.spec.replicas}`)
				daemonSelector := kubectl("get", "daemonset", "hp-daemon", "-n", testNamespace,
					"-o", `jsonpath={.spec.template.spec.nodeSelector.kubernetes\.io/os}`)
				daemonHibernateSelector := kubectl("get", "daemonset", "hp-daemon", "-n", testNamespace,
					"-o", `jsonpath={.spec.template.spec.nodeSelector.greencosts\.hstr\.nl/hibernate}`)
				status := kubectl("get", "hibernatepolicy", "hp-workloads", "-n", testNamespace,
					"-o", `jsonpath={.status.hibernatedWorkloads}`)
				g.Expect(statefulReplicas).To(Equal("2"))
				g.Expect(replicaSetReplicas).To(Equal("2"))
				g.Expect(daemonSelector).To(Equal("linux"))
				g.Expect(daemonHibernateSelector).To(BeEmpty())
				g.Expect(status).To(BeEmpty())
			})
		})

		It("should honor ClusterHibernatePolicy annotation precedence", func() {
			const testNamespace = "kube-greencosts-e2e-cluster"
			createNamespace(testNamespace)
			DeferCleanup(deleteNamespace, testNamespace)
			kubectl("annotate", "namespace", testNamespace,
				"greencosts.hstr.nl/clusterhibernatepolicy=cluster-sleep", "--overwrite")

			applyYAML(testNamespace, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ns-deploy
spec:
  replicas: 2
  selector:
    matchLabels:
      app: ns-deploy
  template:
    metadata:
      labels:
        app: ns-deploy
    spec:
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.10
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: own-deploy
  annotations:
    greencosts.hstr.nl/clusterhibernatepolicy: other-sleep
spec:
  replicas: 3
  selector:
    matchLabels:
      app: own-deploy
  template:
    metadata:
      labels:
        app: own-deploy
    spec:
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.10
`)
			applyYAML("", `
apiVersion: greencosts.hstr.nl/v1alpha1
kind: ClusterHibernatePolicy
metadata:
  name: cluster-sleep
spec:
  action:
    maxReplicas: 0
---
apiVersion: greencosts.hstr.nl/v1alpha1
kind: ClusterHibernatePolicy
metadata:
  name: other-sleep
spec:
  includedResources: [Deployment]
  action:
    maxReplicas: 1
`)
			DeferCleanup(func() {
				kubectl("delete", "chp", "cluster-sleep", "other-sleep", "--ignore-not-found")
			})

			waitFor("cluster hibernate annotation precedence", func(g Gomega) {
				nsReplicas := kubectl("get", "deployment", "ns-deploy", "-n", testNamespace,
					"-o", `jsonpath={.spec.replicas}`)
				ownReplicas := kubectl("get", "deployment", "own-deploy", "-n", testNamespace,
					"-o", `jsonpath={.spec.replicas}`)
				ownOriginal := kubectl("get", "deployment", "own-deploy", "-n", testNamespace,
					"-o", `jsonpath={.metadata.annotations.greencosts\.hstr\.nl/original-replicas}`)
				g.Expect(nsReplicas).To(Equal("0"))
				g.Expect(ownReplicas).To(Equal("1"))
				g.Expect(ownOriginal).To(Equal("3"))
			})
		})

		It("should not let HibernatePolicy wake or take over ClusterHibernatePolicy workloads", func() {
			const testNamespace = "kube-greencosts-e2e-overlap"
			createNamespace(testNamespace)
			DeferCleanup(deleteNamespace, testNamespace)
			DeferCleanup(func() {
				kubectl("delete", "chp", "cluster-owner", "--ignore-not-found")
			})

			applyYAML(testNamespace, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: overlap-deploy
  annotations:
    greencosts.hstr.nl/clusterhibernatepolicy: cluster-owner
spec:
  replicas: 3
  selector:
    matchLabels: {app: overlap-deploy}
  template:
    metadata:
      labels: {app: overlap-deploy}
    spec:
      containers:
      - name: pause
        image: registry.k8s.io/pause:3.10
---
apiVersion: greencosts.hstr.nl/v1alpha1
kind: ClusterHibernatePolicy
metadata:
  name: cluster-owner
spec:
  action:
    maxReplicas: 1
`)

			waitFor("cluster policy owns overlap deployment", func(g Gomega) {
				replicas := kubectl("get", "deployment", "overlap-deploy", "-n", testNamespace,
					"-o", `jsonpath={.spec.replicas}`)
				ownerKind := kubectl("get", "deployment", "overlap-deploy", "-n", testNamespace,
					"-o", `jsonpath={.metadata.annotations.greencosts\.hstr\.nl/hibernated-by-kind}`)
				ownerName := kubectl("get", "deployment", "overlap-deploy", "-n", testNamespace,
					"-o", `jsonpath={.metadata.annotations.greencosts\.hstr\.nl/hibernated-by-name}`)
				g.Expect(replicas).To(Equal("1"))
				g.Expect(ownerKind).To(Equal("ClusterHibernatePolicy"))
				g.Expect(ownerName).To(Equal("cluster-owner"))
			})

			applyYAML(testNamespace, `
apiVersion: greencosts.hstr.nl/v1alpha1
kind: HibernatePolicy
metadata:
  name: namespace-owner
spec:
  workloadTypes: [Deployment]
  action:
    maxReplicas: 0
`)

			waitFor("namespace policy does not take over cluster-owned deployment", func(g Gomega) {
				condition := kubectl("get", "hibernatepolicy", "namespace-owner", "-n", testNamespace,
					"-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`)
				replicas := kubectl("get", "deployment", "overlap-deploy", "-n", testNamespace,
					"-o", `jsonpath={.spec.replicas}`)
				ownerKind := kubectl("get", "deployment", "overlap-deploy", "-n", testNamespace,
					"-o", `jsonpath={.metadata.annotations.greencosts\.hstr\.nl/hibernated-by-kind}`)
				g.Expect(condition).To(Equal("True"))
				g.Expect(replicas).To(Equal("1"))
				g.Expect(ownerKind).To(Equal("ClusterHibernatePolicy"))
			})

			kubectl("patch", "hibernatepolicy", "namespace-owner", "-n", testNamespace,
				"--type=merge", "-p", currentAvailabilityWindowPatch())
			waitFor("namespace policy does not wake cluster-owned deployment", func(g Gomega) {
				replicas := kubectl("get", "deployment", "overlap-deploy", "-n", testNamespace,
					"-o", `jsonpath={.spec.replicas}`)
				ownerKind := kubectl("get", "deployment", "overlap-deploy", "-n", testNamespace,
					"-o", `jsonpath={.metadata.annotations.greencosts\.hstr\.nl/hibernated-by-kind}`)
				g.Expect(replicas).To(Equal("1"))
				g.Expect(ownerKind).To(Equal("ClusterHibernatePolicy"))
			})

			kubectl("patch", "chp", "cluster-owner", "--type=merge", "-p", currentAvailabilityWindowPatch())
			waitFor("cluster owner wakes its deployment", func(g Gomega) {
				replicas := kubectl("get", "deployment", "overlap-deploy", "-n", testNamespace,
					"-o", `jsonpath={.spec.replicas}`)
				ownerKind := kubectl("get", "deployment", "overlap-deploy", "-n", testNamespace,
					"-o", `jsonpath={.metadata.annotations.greencosts\.hstr\.nl/hibernated-by-kind}`)
				g.Expect(replicas).To(Equal("3"))
				g.Expect(ownerKind).To(BeEmpty())
			})
		})

	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

func createNamespace(name string) {
	kubectl("delete", "ns", name, "--ignore-not-found", "--wait=true")
	kubectl("create", "ns", name)
}

func deleteNamespace(name string) {
	kubectl("delete", "ns", name, "--ignore-not-found", "--wait=true")
}

func applyYAML(namespaceName, yaml string) {
	args := []string{"apply", "--server-side", "--force-conflicts"}
	if namespaceName != "" {
		args = append(args, "-n", namespaceName)
	}
	args = append(args, "-f", "-")
	cmd := exec.Command("kubectl", args...)
	cmd.Stdin = strings.NewReader(yaml)
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
}

func kubectl(args ...string) string {
	cmd := exec.Command("kubectl", args...)
	output, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return strings.TrimSpace(output)
}

func waitFor(name string, assertion func(Gomega)) {
	By("waiting for " + name)
	Eventually(assertion, 2*time.Minute, time.Second).Should(Succeed())
}

func customPriceJSON(now time.Time) string {
	base := now.Truncate(time.Minute).Add(time.Minute)
	points := make([]string, 0, 3)
	for i, price := range []float64{30, 120, 60} {
		at := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		points = append(points, fmt.Sprintf(`{"start":"%s","eurPerMWh":%.0f}`, at, price))
	}
	return strings.Join([]string{"[", strings.Join(points, ","), "]"}, "")
}

func currentAvailabilityWindowPatch() string {
	weekday := time.Now().UTC().Format("Mon")
	return fmt.Sprintf(
		`{"spec":{"availabilityWindows":[{"weekdays":["%s"],"from":"00:00","until":"23:59","timezone":"UTC"}]}}`,
		weekday,
	)
}

func ensureMetricsAccess() {
	By("removing any existing metrics ClusterRoleBinding")
	cmd := exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName, "--ignore-not-found")
	_, _ = utils.Run(cmd)

	By("creating a ClusterRoleBinding for the service account to allow access to metrics")
	cmd = exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
		"--clusterrole=kube-greencosts-metrics-reader",
		fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
	)
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")
}

func scrapeMetrics(podName string) string {
	ensureMetricsAccess()

	By("getting the service account token")
	token, err := serviceAccountToken()
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	ExpectWithOffset(1, token).NotTo(BeEmpty())

	cmd := exec.Command("kubectl", "delete", "pod", podName, "-n", namespace, "--ignore-not-found")
	_, _ = utils.Run(cmd)

	By("creating the " + podName + " pod to access the metrics endpoint")
	cmd = exec.Command("kubectl", "run", podName, "--restart=Never",
		"--namespace", namespace,
		"--image=curlimages/curl:latest",
		"--overrides",
		fmt.Sprintf(`{
			"spec": {
				"containers": [{
					"name": "curl",
					"image": "curlimages/curl:latest",
					"command": ["/bin/sh", "-c"],
					"args": ["curl -sSk -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
					"securityContext": {
						"allowPrivilegeEscalation": false,
						"capabilities": {"drop": ["ALL"]},
						"runAsNonRoot": true,
						"runAsUser": 1000,
						"seccompProfile": {"type": "RuntimeDefault"}
					}
				}],
				"serviceAccount": "%s"
			}
		}`, token, metricsServiceName, namespace, serviceAccountName))
	_, err = utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to create metrics curl pod")

	By("waiting for the " + podName + " pod to complete")
	EventuallyWithOffset(1, func(g Gomega) {
		phase := kubectl("get", "pods", podName, "-o", "jsonpath={.status.phase}", "-n", namespace)
		g.Expect(phase).To(Equal("Succeeded"), "curl pod in wrong status")
	}, 5*time.Minute).Should(Succeed())

	By("getting the " + podName + " logs")
	cmd = exec.Command("kubectl", "logs", podName, "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
