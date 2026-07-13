package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	cron "github.com/robfig/cron/v3"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	greencostsv1alpha1 "github.com/tristanscholten/kube-greencosts/api/v1alpha1"
)

func TestNextScheduleTimeAppliesStartingDeadline(t *testing.T) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse("* * * * *")
	if err != nil {
		t.Fatalf("parsing schedule: %v", err)
	}
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	lastRun := now.Add(-10 * time.Minute)
	deadlineSeconds := int64(120)

	got, err := nextScheduleTime(sched, lastRun, now, &deadlineSeconds)
	if err != nil {
		t.Fatalf("nextScheduleTime() error = %v", err)
	}
	want := now.Add(-2 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("nextScheduleTime() = %s, want %s", got, want)
	}
}

func TestNextScheduleTimeRejectsTooManyMissedSchedules(t *testing.T) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse("* * * * *")
	if err != nil {
		t.Fatalf("parsing schedule: %v", err)
	}
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	deadlineSeconds := int64(1)

	_, err = nextScheduleTime(sched, now.Add(-3*time.Hour), now, &deadlineSeconds)
	if err == nil || !strings.Contains(err.Error(), "too many missed schedules") {
		t.Fatalf("nextScheduleTime() error = %v, want too many missed schedules", err)
	}
}

func TestFilterPricesInWindowUsesInclusiveStartExclusiveEnd(t *testing.T) {
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	prices := []greencostsv1alpha1.PricePoint{
		{At: metav1.NewTime(base.Add(-time.Minute)), EurPerMWh: 1},
		{At: metav1.NewTime(base), EurPerMWh: 2},
		{At: metav1.NewTime(base.Add(30 * time.Minute)), EurPerMWh: 3},
		{At: metav1.NewTime(base.Add(time.Hour)), EurPerMWh: 4},
	}

	got := filterPricesInWindow(prices, base, base.Add(time.Hour))
	if len(got) != 2 {
		t.Fatalf("filtered prices length = %d, want 2", len(got))
	}
	if got[0].EurPerMWh != 2 || got[1].EurPerMWh != 3 {
		t.Fatalf("filtered prices = %#v, want start and middle only", got)
	}
}

func TestJobFinishedAndStartTime(t *testing.T) {
	start := metav1.NewTime(time.Date(2026, 7, 6, 11, 0, 0, 0, time.UTC))
	created := metav1.NewTime(time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC))
	tests := []struct {
		name     string
		job      *batchv1.Job
		finished bool
		condType batchv1.JobConditionType
		wantTime time.Time
	}{
		{
			name: "active job falls back to creation time",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: created},
			},
			wantTime: created.Time,
		},
		{
			name: "complete job reports complete condition and start time",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: created},
				Status: batchv1.JobStatus{
					StartTime:  &start,
					Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
				},
			},
			finished: true,
			condType: batchv1.JobComplete,
			wantTime: start.Time,
		},
		{
			name: "failed job reports failed condition",
			job: &batchv1.Job{
				Status: batchv1.JobStatus{
					Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}},
				},
			},
			finished: true,
			condType: batchv1.JobFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			finished, condType := jobFinished(tt.job)
			if finished != tt.finished || condType != tt.condType {
				t.Fatalf("jobFinished() = (%v, %q), want (%v, %q)", finished, condType, tt.finished, tt.condType)
			}
			if !tt.wantTime.IsZero() && !jobStartTime(tt.job).Equal(tt.wantTime) {
				t.Fatalf("jobStartTime() = %s, want %s", jobStartTime(tt.job), tt.wantTime)
			}
		})
	}
}

func TestSyncActiveJobsUpdatesStatusAndPrunesHistory(t *testing.T) {
	ctx := context.Background()
	s := newEnergyAwareCronJobTestScheme(t)
	completion := metav1.NewTime(time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC))
	oldSuccess := energyAwareJob("success-old", metav1.NewTime(completion.Add(-3*time.Hour)))
	oldSuccess.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	oldSuccess.Status.CompletionTime = &metav1.Time{Time: completion.Add(-2 * time.Hour)}
	newSuccess := energyAwareJob("success-new", metav1.NewTime(completion.Add(-2*time.Hour)))
	newSuccess.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	newSuccess.Status.CompletionTime = &completion
	oldFailed := energyAwareJob("failed-old", metav1.NewTime(completion.Add(-4*time.Hour)))
	oldFailed.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	newFailed := energyAwareJob("failed-new", metav1.NewTime(completion.Add(-time.Hour)))
	newFailed.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	active := energyAwareJob("active", metav1.NewTime(completion.Add(-30*time.Minute)))
	successLimit := int32(1)
	failedLimit := int32(1)
	eacj := energyAwareCronJobForController("nightly")
	eacj.Spec.CronJob.SuccessfulJobsHistoryLimit = &successLimit
	eacj.Spec.CronJob.FailedJobsHistoryLimit = &failedLimit
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(oldSuccess, newSuccess, oldFailed, newFailed, active).Build()
	r := &EnergyAwareCronJobReconciler{Client: c, Scheme: s}

	if err := r.syncActiveJobs(ctx, eacj); err != nil {
		t.Fatalf("syncActiveJobs() error = %v", err)
	}
	if len(eacj.Status.Active) != 1 || eacj.Status.Active[0].Name != "active" {
		t.Fatalf("active refs = %#v, want active job only", eacj.Status.Active)
	}
	if eacj.Status.LastSuccessfulTime == nil || !eacj.Status.LastSuccessfulTime.Equal(&completion) {
		t.Fatalf("LastSuccessfulTime = %#v, want %s", eacj.Status.LastSuccessfulTime, completion.Time)
	}
	assertJobDeleted(t, ctx, c, "success-old")
	assertJobDeleted(t, ctx, c, "failed-old")
	assertJobExists(t, ctx, c, "success-new")
	assertJobExists(t, ctx, c, "failed-new")
}

func TestDispatchJobReplacesActiveJobsAndUpdatesStatus(t *testing.T) {
	ctx := context.Background()
	s := newEnergyAwareCronJobTestScheme(t)
	existing := energyAwareJob("existing", metav1.Now())
	eacj := energyAwareCronJobForController("nightly")
	eacj.Spec.CronJob.ConcurrencyPolicy = batchv1.ReplaceConcurrent
	eacj.Status.Active = []corev1.ObjectReference{{Kind: jobKind, Namespace: testDefaultNamespace, Name: existing.Name}}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&greencostsv1alpha1.EnergyAwareCronJob{}).
		WithObjects(eacj, existing).
		Build()
	r := &EnergyAwareCronJobReconciler{Client: c, Scheme: s}
	scheduledAt := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

	_, err := r.dispatchJob(ctx, eacj.DeepCopy(), eacj, scheduledAt)
	if err != nil {
		t.Fatalf("dispatchJob() error = %v", err)
	}
	assertJobDeleted(t, ctx, c, existing.Name)
	createdName := energyAwareCronJobName(eacj.Name, scheduledAt)
	assertJobExists(t, ctx, c, createdName)

	var got greencostsv1alpha1.EnergyAwareCronJob
	if err := c.Get(ctx, client.ObjectKeyFromObject(eacj), &got); err != nil {
		t.Fatalf("getting EnergyAwareCronJob: %v", err)
	}
	if got.Status.LastScheduleTime == nil || !got.Status.LastScheduleTime.Time.Equal(scheduledAt) {
		t.Fatalf("LastScheduleTime = %#v, want %s", got.Status.LastScheduleTime, scheduledAt)
	}
	condition := findCondition(got.Status.Conditions, conditionTypeReady)
	if condition == nil || condition.Reason != "JobDispatched" {
		t.Fatalf("Ready condition = %#v, want JobDispatched", condition)
	}
}

func TestFailWithStoresReadyErrorCondition(t *testing.T) {
	ctx := context.Background()
	s := newEnergyAwareCronJobTestScheme(t)
	eacj := energyAwareCronJobForController("nightly")
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&greencostsv1alpha1.EnergyAwareCronJob{}).
		WithObjects(eacj).
		Build()
	r := &EnergyAwareCronJobReconciler{Client: c, Scheme: s}

	result, err := r.failWith(ctx, eacj.DeepCopy(), eacj, errForTest("price source missing"))
	if err != nil {
		t.Fatalf("failWith() error = %v", err)
	}
	if result.RequeueAfter != retryShort {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, retryShort)
	}

	var got greencostsv1alpha1.EnergyAwareCronJob
	if err := c.Get(ctx, client.ObjectKeyFromObject(eacj), &got); err != nil {
		t.Fatalf("getting EnergyAwareCronJob: %v", err)
	}
	condition := findCondition(got.Status.Conditions, conditionTypeReady)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != conditionReasonError {
		t.Fatalf("Ready condition = %#v, want false %s", condition, conditionReasonError)
	}
	if condition.Message != "price source missing" {
		t.Fatalf("Ready message = %q, want price source missing", condition.Message)
	}
}

func TestEnergyAwareCronJobReconcileSuspendClearsNextScheduledTime(t *testing.T) {
	ctx := context.Background()
	s := newEnergyAwareCronJobTestScheme(t)
	suspend := true
	next := metav1.NewTime(time.Now().Add(time.Hour))
	eacj := energyAwareCronJobForController("nightly")
	eacj.Spec.CronJob.Suspend = &suspend
	eacj.Status.NextScheduledTime = &next
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&greencostsv1alpha1.EnergyAwareCronJob{}).
		WithObjects(eacj).
		Build()
	r := &EnergyAwareCronJobReconciler{Client: c, Scheme: s}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(eacj)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !result.IsZero() {
		t.Fatalf("Reconcile() result = %#v, want zero", result)
	}

	got := getEnergyAwareCronJob(t, ctx, c, eacj.Name)
	if got.Status.NextScheduledTime != nil {
		t.Fatalf("NextScheduledTime = %s, want nil", got.Status.NextScheduledTime.Time)
	}
}

func TestEnergyAwareCronJobReconcileZeroDurationDispatchesImmediately(t *testing.T) {
	ctx := context.Background()
	s := newEnergyAwareCronJobTestScheme(t)
	eacj := energyAwareCronJobForController("nightly")
	eacj.CreationTimestamp = metav1.NewTime(time.Now().Add(-2 * time.Minute).Truncate(time.Minute))
	eacj.Spec.EnergyStrategy.ScheduleWindow.Duration = 0
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&greencostsv1alpha1.EnergyAwareCronJob{}).
		WithObjects(eacj).
		Build()
	r := &EnergyAwareCronJobReconciler{Client: c, Scheme: s}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(eacj)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("Reconcile() result = %#v, want no delayed requeue after dispatch", result)
	}

	got := getEnergyAwareCronJob(t, ctx, c, eacj.Name)
	if got.Status.LastScheduleTime == nil {
		t.Fatal("LastScheduleTime = nil, want dispatch timestamp")
	}
	if len(got.Status.Active) != 1 {
		t.Fatalf("active refs = %#v, want one dispatched job", got.Status.Active)
	}
	assertJobExists(t, ctx, c, got.Status.Active[0].Name)
}

func TestEnergyAwareCronJobReconcileForbidConcurrentSkipsActiveJob(t *testing.T) {
	ctx := context.Background()
	s := newEnergyAwareCronJobTestScheme(t)
	eacj := energyAwareCronJobForController("nightly")
	eacj.CreationTimestamp = metav1.NewTime(time.Now().Add(-2 * time.Minute).Truncate(time.Minute))
	eacj.Spec.CronJob.ConcurrencyPolicy = batchv1.ForbidConcurrent
	active := energyAwareJob("active", metav1.Now())
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&greencostsv1alpha1.EnergyAwareCronJob{}).
		WithObjects(eacj, active).
		Build()
	r := &EnergyAwareCronJobReconciler{Client: c, Scheme: s}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(eacj)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if !result.IsZero() {
		t.Fatalf("Reconcile() result = %#v, want zero", result)
	}

	got := getEnergyAwareCronJob(t, ctx, c, eacj.Name)
	if got.Status.LastScheduleTime == nil {
		t.Fatal("LastScheduleTime = nil, want skipped schedule timestamp")
	}
	if len(got.Status.Active) != 1 || got.Status.Active[0].Name != active.Name {
		t.Fatalf("active refs = %#v, want existing active job", got.Status.Active)
	}
}

func TestEnergyAwareCronJobReconcileMissingEnergyPriceSourceStoresError(t *testing.T) {
	ctx := context.Background()
	s := newEnergyAwareCronJobTestScheme(t)
	eacj := energyAwareCronJobForController("nightly")
	eacj.CreationTimestamp = metav1.NewTime(time.Now().Add(-2 * time.Minute).Truncate(time.Minute))
	eacj.Spec.EnergyStrategy.ScheduleWindow.Duration = time.Hour
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&greencostsv1alpha1.EnergyAwareCronJob{}).
		WithObjects(eacj).
		Build()
	r := &EnergyAwareCronJobReconciler{Client: c, Scheme: s}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(eacj)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != retryShort {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, retryShort)
	}

	got := getEnergyAwareCronJob(t, ctx, c, eacj.Name)
	condition := findCondition(got.Status.Conditions, conditionTypeReady)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != conditionReasonError {
		t.Fatalf("Ready condition = %#v, want missing EnergyPriceSource error", condition)
	}
	if !strings.Contains(condition.Message, `EnergyPriceSource "prices" not found`) {
		t.Fatalf("Ready message = %q, want missing EnergyPriceSource", condition.Message)
	}
}

func TestEnergyAwareCronJobReconcileNoPriceDataRetries(t *testing.T) {
	ctx := context.Background()
	s := newEnergyAwareCronJobTestScheme(t)
	eacj := energyAwareCronJobForController("nightly")
	eacj.CreationTimestamp = metav1.NewTime(time.Now().Add(-2 * time.Minute).Truncate(time.Minute))
	eacj.Spec.EnergyStrategy.ScheduleWindow.Duration = time.Hour
	eps := &greencostsv1alpha1.EnergyPriceSource{
		ObjectMeta: metav1.ObjectMeta{Name: testEnergyPriceSourceName, Namespace: testDefaultNamespace},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&greencostsv1alpha1.EnergyAwareCronJob{}).
		WithObjects(eacj, eps).
		Build()
	r := &EnergyAwareCronJobReconciler{Client: c, Scheme: s}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(eacj)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != retryShort {
		t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, retryShort)
	}

	got := getEnergyAwareCronJob(t, ctx, c, eacj.Name)
	if got.Status.NextScheduledTime != nil {
		t.Fatalf("NextScheduledTime = %s, want nil until price data exists", got.Status.NextScheduledTime.Time)
	}
}

func TestEnergyAwareCronJobReconcileStoresFutureOptimalTime(t *testing.T) {
	ctx := context.Background()
	s := newEnergyAwareCronJobTestScheme(t)
	now := time.Now().Truncate(time.Minute)
	eacj := energyAwareCronJobForController("nightly")
	eacj.CreationTimestamp = metav1.NewTime(now.Add(-2 * time.Minute))
	eacj.Spec.EnergyStrategy.ScheduleWindow.Duration = 3 * time.Minute
	eps := &greencostsv1alpha1.EnergyPriceSource{
		ObjectMeta: metav1.ObjectMeta{Name: testEnergyPriceSourceName, Namespace: testDefaultNamespace},
		Status: greencostsv1alpha1.EnergyPriceSourceStatus{
			Prices: []greencostsv1alpha1.PricePoint{
				{At: metav1.NewTime(now.Add(-time.Minute)), EurPerMWh: 100},
				{At: metav1.NewTime(now), EurPerMWh: 80},
				{At: metav1.NewTime(now.Add(time.Minute)), EurPerMWh: 10},
			},
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithStatusSubresource(&greencostsv1alpha1.EnergyAwareCronJob{}).
		WithObjects(eacj, eps).
		Build()
	r := &EnergyAwareCronJobReconciler{Client: c, Scheme: s}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(eacj)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter <= 0 || result.RequeueAfter > 2*time.Minute {
		t.Fatalf("RequeueAfter = %s, want sleep until future optimal time", result.RequeueAfter)
	}

	got := getEnergyAwareCronJob(t, ctx, c, eacj.Name)
	want := now.Add(time.Minute)
	if got.Status.NextScheduledTime == nil || !got.Status.NextScheduledTime.Time.Equal(want) {
		t.Fatalf("NextScheduledTime = %#v, want %s", got.Status.NextScheduledTime, want)
	}
	if got.Status.NextCronWindow == nil || !got.Status.NextCronWindow.Time.Equal(now.Add(-time.Minute)) {
		t.Fatalf("NextCronWindow = %#v, want %s", got.Status.NextCronWindow, now.Add(-time.Minute))
	}
	condition := findCondition(got.Status.Conditions, conditionTypeReady)
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != "Scheduled" {
		t.Fatalf("Ready condition = %#v, want Scheduled", condition)
	}
}

func TestEnergyPriceSourceToEACJsMapsOnlyReferencesInNamespace(t *testing.T) {
	ctx := context.Background()
	s := newEnergyAwareCronJobTestScheme(t)
	matching := energyAwareCronJobForController("match")
	matching.Spec.EnergyPriceSource.Name = testEnergyPriceSourceName
	otherSource := energyAwareCronJobForController("other-source")
	otherSource.Spec.EnergyPriceSource.Name = "backup-prices"
	otherNamespace := energyAwareCronJobForController("other-namespace")
	otherNamespace.Namespace = testOtherName
	otherNamespace.Spec.EnergyPriceSource.Name = testEnergyPriceSourceName
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(matching, otherSource, otherNamespace).Build()
	r := &EnergyAwareCronJobReconciler{Client: c}
	eps := &greencostsv1alpha1.EnergyPriceSource{ObjectMeta: metav1.ObjectMeta{Name: testEnergyPriceSourceName, Namespace: testDefaultNamespace}}

	got := r.energyPriceSourceToEACJs(ctx, eps)
	want := []ctrl.Request{{NamespacedName: types.NamespacedName{Namespace: testDefaultNamespace, Name: "match"}}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("energyPriceSourceToEACJs() = %#v, want %#v", got, want)
	}
}

func newEnergyAwareCronJobTestScheme(t *testing.T) *runtime.Scheme {
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

func energyAwareCronJobForController(name string) *greencostsv1alpha1.EnergyAwareCronJob {
	return &greencostsv1alpha1.EnergyAwareCronJob{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "greencosts.hstr.nl/v1alpha1",
			Kind:       "EnergyAwareCronJob",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testDefaultNamespace,
		},
		Spec: greencostsv1alpha1.EnergyAwareCronJobSpec{
			EnergyPriceSource: corev1.LocalObjectReference{Name: testEnergyPriceSourceName},
			CronJob: batchv1.CronJobSpec{
				Schedule: testCronEveryMinute,
				JobTemplate: batchv1.JobTemplateSpec{
					Spec: batchv1.JobSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{{
									Name:  "worker",
									Image: "busybox",
								}},
							},
						},
					},
				},
			},
		},
	}
}

func energyAwareJob(name string, start metav1.Time) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         testDefaultNamespace,
			CreationTimestamp: start,
			Labels: map[string]string{
				ownerLabel: energyAwareCronJobOwnerLabelValue("nightly"),
			},
		},
		Status: batchv1.JobStatus{StartTime: &start},
	}
}

func getEnergyAwareCronJob(
	t *testing.T,
	ctx context.Context,
	c client.Client,
	name string,
) greencostsv1alpha1.EnergyAwareCronJob {
	t.Helper()
	var got greencostsv1alpha1.EnergyAwareCronJob
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: name}, &got); err != nil {
		t.Fatalf("getting EnergyAwareCronJob %s: %v", name, err)
	}
	return got
}

func assertJobDeleted(t *testing.T, ctx context.Context, c client.Client, name string) {
	t.Helper()
	var job batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: name}, &job); client.IgnoreNotFound(err) != nil {
		t.Fatalf("getting job %s: %v", name, err)
	} else if err == nil {
		t.Fatalf("job %s still exists", name)
	}
}

func assertJobExists(t *testing.T, ctx context.Context, c client.Client, name string) {
	t.Helper()
	var job batchv1.Job
	if err := c.Get(ctx, client.ObjectKey{Namespace: testDefaultNamespace, Name: name}, &job); err != nil {
		t.Fatalf("job %s does not exist: %v", name, err)
	}
}
