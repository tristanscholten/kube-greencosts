package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

func TestClientCPUAndPrometheusDisabledPaths(t *testing.T) {
	metricsClient := metricsfake.NewSimpleClientset()
	metricsClient.PrependReactor("list", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &metricsv1beta1.PodMetricsList{
			Items: []metricsv1beta1.PodMetrics{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "apps"},
					Containers: []metricsv1beta1.ContainerMetrics{
						{Usage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("125m")}},
						{Usage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0.25")}},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pod-b", Namespace: "apps"},
					Containers: []metricsv1beta1.ContainerMetrics{
						{Usage: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")}},
					},
				},
			},
		}, nil
	})

	client, err := NewClient(metricsClient, "")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	cpu, err := client.QueryNamespaceCPU(context.Background(), "apps")
	if err != nil {
		t.Fatalf("QueryNamespaceCPU() error = %v", err)
	}
	if got, want := cpu.MilliValue(), int64(375); got != want {
		t.Fatalf("QueryNamespaceCPU() = %dm, want %dm", got, want)
	}

	network, err := client.QueryNamespaceNetwork(context.Background(), "apps", 5*time.Minute)
	if err != nil {
		t.Fatalf("QueryNamespaceNetwork() error = %v", err)
	}
	if network.Sign() != 0 {
		t.Fatalf("QueryNamespaceNetwork() with no Prometheus = %s, want zero", network.String())
	}

	rps, err := client.QueryNamespaceIngressRPS(context.Background(), "apps", 5*time.Minute)
	if err != nil {
		t.Fatalf("QueryNamespaceIngressRPS() error = %v", err)
	}
	if rps != 0 {
		t.Fatalf("QueryNamespaceIngressRPS() with no Prometheus = %v, want zero", rps)
	}
}

func TestQueryNamespaceCPUWrapsListError(t *testing.T) {
	metricsClient := metricsfake.NewSimpleClientset()
	metricsClient.PrependReactor("list", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io", Resource: "podmetricses"}, "missing")
	})

	client, err := NewClient(metricsClient, "")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.QueryNamespaceCPU(context.Background(), "missing")
	if err == nil {
		t.Fatal("QueryNamespaceCPU() error = nil, want list error")
	}
}

func TestNewClientRejectsInvalidPrometheusURL(t *testing.T) {
	_, err := NewClient(metricsfake.NewSimpleClientset(), string([]byte{0x7f}))
	if err == nil {
		t.Fatal("NewClient() accepted invalid Prometheus URL")
	}
}

func TestPromDuration(t *testing.T) {
	if got := promDuration(90 * time.Second); got != "1m30s" {
		t.Fatalf("promDuration() = %q, want 1m30s", got)
	}
}

func TestAddPodCPUSkipsContainersWithoutCPU(t *testing.T) {
	total := resource.NewMilliQuantity(100, resource.DecimalSI)
	addPodCPU(total, &metricsv1beta1.PodMetrics{
		Containers: []metricsv1beta1.ContainerMetrics{
			{Usage: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")}},
		},
	})
	if got := total.MilliValue(); got != 100 {
		t.Fatalf("addPodCPU() total = %dm, want unchanged 100m", got)
	}
}

func TestScalarValue(t *testing.T) {
	tests := []struct {
		name string
		val  model.Value
		want float64
	}{
		{
			name: "scalar",
			val:  &model.Scalar{Value: 12.5},
			want: 12.5,
		},
		{
			name: "single vector sample",
			val: model.Vector{
				{Value: 7},
			},
			want: 7,
		},
		{
			name: "empty vector",
			val:  model.Vector{},
			want: 0,
		},
		{
			name: "multiple vector samples",
			val: model.Vector{
				{Value: 1},
				{Value: 2},
			},
			want: 0,
		},
		{
			name: "unexpected value type",
			val:  model.Matrix{},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scalarValue(tt.val); got != tt.want {
				t.Fatalf("scalarValue() = %v, want %v", got, tt.want)
			}
		})
	}
}
