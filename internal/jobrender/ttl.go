package jobrender

import (
	"context"
	"errors"
	"strconv"
	"strings"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/screen"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const ttlAnnotation = "arprt.dayflower.github.io/ttl-seconds-after-finished"

// validateManagedJob checks identity even when the original scheduler pair has
// been removed. Label selection alone does not establish ownership.
func validateManagedJob(job *batchv1.Job) error {
	owner := metav1.GetControllerOf(job)
	id := strings.TrimPrefix(job.Name, "arprt-render-")
	if !validID(id) || owner == nil || owner.APIVersion != api.GroupVersion.String() || owner.Kind != "ArprtDevice" || owner.Name == "" || owner.UID == "" || job.Labels[managedLabel] != "true" || job.Labels[deviceLabel] != hash([]byte(owner.UID))[:32] {
		return errors.New("render Job identity conflict")
	}
	return nil
}

// applyJobTTL is called only after the durable scheduler records no longer need
// this execution. The creation annotation survives record replacement and crash
// recovery. Legacy Jobs without an annotation use the default retention.
func (r *Reconciler) applyJobTTL(ctx context.Context, job *batchv1.Job) error {
	if err := validateManagedJob(job); err != nil {
		return err
	}
	complete, failed := terminal(job)
	if (!complete && !failed) || job.Spec.TTLSecondsAfterFinished != nil || job.DeletionTimestamp != nil {
		return nil
	}
	ttl := screen.JobTTL(api.JobSource{})
	if value, ok := job.Annotations[ttlAnnotation]; ok {
		seconds, err := strconv.ParseInt(value, 10, 32)
		if err != nil || seconds < 0 {
			return errors.New("invalid render Job TTL annotation")
		}
		ttl = int32(seconds)
	}
	before := job.DeepCopy()
	job.Spec.TTLSecondsAfterFinished = ptr.To(ttl)
	// Resource-version preconditions prevent applying TTL to a replaced Job or
	// overwriting another writer's TTL. Maintenance retries conflicts.
	return client.IgnoreNotFound(r.Client.Patch(ctx, job, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})))
}

func (r *Reconciler) finishJobTTL(ctx context.Context, st *state, finished *attempt) error {
	if finished == nil {
		return nil
	}
	var job batchv1.Job
	if err := r.Reader.Get(ctx, client.ObjectKey{Namespace: st.Namespace, Name: "arprt-render-" + finished.ID}, &job); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if err := validateJobOwner(st, &job); err != nil {
		return err
	}
	return r.applyJobTTL(ctx, &job)
}
