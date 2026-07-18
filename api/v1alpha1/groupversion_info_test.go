package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestAddToSchemeRegistersAPIObjects(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}

	for _, tt := range []struct {
		name string
		obj  runtime.Object
	}{
		{name: "EnergyPriceSource", obj: &EnergyPriceSource{}},
		{name: "EnergyPriceSourceList", obj: &EnergyPriceSourceList{}},
		{name: "EnergyAwareCronJob", obj: &EnergyAwareCronJob{}},
		{name: "EnergyAwareCronJobList", obj: &EnergyAwareCronJobList{}},
		{name: "HibernatePolicy", obj: &HibernatePolicy{}},
		{name: "HibernatePolicyList", obj: &HibernatePolicyList{}},
		{name: "ClusterHibernatePolicy", obj: &ClusterHibernatePolicy{}},
		{name: "ClusterHibernatePolicyList", obj: &ClusterHibernatePolicyList{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gvks, _, err := s.ObjectKinds(tt.obj)
			if err != nil {
				t.Fatalf("ObjectKinds() error = %v", err)
			}
			if len(gvks) != 1 {
				t.Fatalf("ObjectKinds() = %#v, want one GVK", gvks)
			}
			if gvks[0].GroupVersion() != GroupVersion {
				t.Fatalf("GroupVersion = %s, want %s", gvks[0].GroupVersion(), GroupVersion)
			}
			if gvks[0].Kind != tt.name {
				t.Fatalf("Kind = %s, want %s", gvks[0].Kind, tt.name)
			}
		})
	}
}
