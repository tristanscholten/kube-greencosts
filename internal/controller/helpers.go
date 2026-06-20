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
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

const (
	annotationOriginalHPAMin = "greencosts.hstr.nl/original-hpa-min"
	annotationOriginalHPAMax = "greencosts.hstr.nl/original-hpa-max"

	// controllerTracer is the OpenTelemetry instrumentation scope used by all
	// controllers and helpers in this package.
	controllerTracer = "greencosts.hstr.nl/controller"
)

// parseHHMM parses an "HH:MM" string and returns a time.Time on the given date in loc.
func parseHHMM(hhmm string, date time.Time, loc *time.Location) (time.Time, error) {
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil {
		return time.Time{}, fmt.Errorf("parsing %q as HH:MM: %w", hhmm, err)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("%q is not a valid HH:MM time", hhmm)
	}
	return time.Date(date.Year(), date.Month(), date.Day(), h, m, 0, 0, loc), nil
}

// computeTargetReplicas returns the target replica count and whether a scale-down
// should be applied for Deployments, StatefulSets and ReplicaSets.
// DaemonSets are handled separately via SleepDaemonSet and are never passed here.
//
//   - MaxReplicas set, current > max → (max, true)
//   - MaxReplicas set, current <= max → (0, false) — workload is at or below cap, no-op
//   - MaxReplicas not set → (0, false) — nothing to do
func computeTargetReplicas(action greencostsv1alpha1.HibernateAction, current int32) (int32, bool) {
	if action.MaxReplicas != nil {
		if current > *action.MaxReplicas {
			return *action.MaxReplicas, true
		}
		return 0, false
	}
	return 0, false
}

// suspendHPA finds an HPA targeting the given workload (by kind+name in namespace),
// stores its original minReplicas and maxReplicas in annotations on the HPA itself,
// then clamps both to target to prevent autoscaling from reversing a scale-down.
//
// If no matching HPA exists, the function is a no-op.
func suspendHPA(ctx context.Context, c client.Client, namespace, kind, name string, target int32) (retErr error) {
	_, span := otel.Tracer(controllerTracer).Start(ctx, "suspendHPA",
		trace.WithAttributes(
			attribute.String("k8s.namespace.name", namespace),
			attribute.String("workload.kind", kind),
			attribute.String("workload.name", name),
		))
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	hpa, found, err := findHPA(ctx, c, namespace, kind, name)
	if err != nil || !found {
		return err
	}
	if hpa.Annotations[annotationOriginalHPAMin] != "" {
		return nil // already suspended
	}

	origMin := int32(1)
	if hpa.Spec.MinReplicas != nil {
		origMin = *hpa.Spec.MinReplicas
	}
	origMax := hpa.Spec.MaxReplicas

	if hpa.Annotations == nil {
		hpa.Annotations = map[string]string{}
	}
	hpa.Annotations[annotationOriginalHPAMin] = strconv.Itoa(int(origMin))
	hpa.Annotations[annotationOriginalHPAMax] = strconv.Itoa(int(origMax))
	hpa.Spec.MinReplicas = &target
	hpa.Spec.MaxReplicas = target

	if err := c.Update(ctx, hpa); err != nil {
		return fmt.Errorf("suspending HPA %s/%s: %w", namespace, hpa.Name, err)
	}
	return nil
}

// restoreHPA reverses the effect of suspendHPA: reads the stored original min/max
// from annotations, restores them, and removes the annotations.
//
// If no matching HPA exists, or none was previously suspended, the function is a no-op.
func restoreHPA(ctx context.Context, c client.Client, namespace, kind, name string) (retErr error) {
	_, span := otel.Tracer(controllerTracer).Start(ctx, "restoreHPA",
		trace.WithAttributes(
			attribute.String("k8s.namespace.name", namespace),
			attribute.String("workload.kind", kind),
			attribute.String("workload.name", name),
		))
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()

	hpa, found, err := findHPA(ctx, c, namespace, kind, name)
	if err != nil || !found {
		return err
	}
	if hpa.Annotations[annotationOriginalHPAMin] == "" {
		return nil // not suspended by us
	}

	origMin := int32(parseOriginalReplicas(hpa.Annotations[annotationOriginalHPAMin]))
	origMax := int32(parseOriginalReplicas(hpa.Annotations[annotationOriginalHPAMax]))

	hpa.Spec.MinReplicas = &origMin
	hpa.Spec.MaxReplicas = origMax
	delete(hpa.Annotations, annotationOriginalHPAMin)
	delete(hpa.Annotations, annotationOriginalHPAMax)

	if err := c.Update(ctx, hpa); err != nil {
		return fmt.Errorf("restoring HPA %s/%s: %w", namespace, hpa.Name, err)
	}
	return nil
}

// findHPA returns the HPA in namespace whose scaleTargetRef matches kind and name.
func findHPA(ctx context.Context, c client.Client, namespace, kind, name string) (*autoscalingv2.HorizontalPodAutoscaler, bool, error) {
	var list autoscalingv2.HorizontalPodAutoscalerList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, false, fmt.Errorf("listing HPAs in %q: %w", namespace, err)
	}
	for i := range list.Items {
		ref := list.Items[i].Spec.ScaleTargetRef
		if ref.Kind == kind && ref.Name == name {
			return &list.Items[i], true, nil
		}
	}
	return nil, false, nil
}
