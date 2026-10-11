package controller

import (
	"context"
	"errors"
	"reflect"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/assignment"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type PlaylistReconciler struct {
	Client client.Client
	Reader client.Reader
}

func (r *PlaylistReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var p api.ArprtPlaylist
		if err := r.Reader.Get(ctx, req.NamespacedName, &p); err != nil {
			return client.IgnoreNotFound(err)
		}
		before := p.DeepCopy()
		d := &api.ArprtDevice{ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace}, Spec: api.ArprtDeviceSpec{PlaylistRef: &api.LocalReference{Name: p.Name}}}
		eligible, err := (assignment.Explicit{Reader: r.Reader}).Eligible(ctx, d)
		ready, reason, message := metav1.ConditionTrue, "ReferencesResolved", "All same-namespace screen references exist"
		if errors.Is(err, assignment.ErrEmpty) || apierrors.IsNotFound(err) || eligible.Missing > 0 {
			ready, reason, message = metav1.ConditionFalse, "ReferenceNotFound", "Missing screens are skipped; check same-namespace references"
		} else if err != nil {
			return err
		}
		p.Status.ObservedGeneration = p.Generation
		set(&p.Status.Conditions, p.Generation, ConditionReady, ready, reason, message)
		if reflect.DeepEqual(p.Status, before.Status) {
			return nil
		}
		return r.Client.Status().Patch(ctx, &p, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
	return ctrl.Result{RequeueAfter: time.Minute}, err
}
func (r *PlaylistReconciler) SetupWithManager(mgr ctrl.Manager) error {
	enqueue := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		var list api.ArprtPlaylistList
		if err := r.Client.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
			return nil
		}
		var requests []reconcile.Request
		for _, p := range list.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&p)})
		}
		return requests
	})
	return ctrl.NewControllerManagedBy(mgr).For(&api.ArprtPlaylist{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).Watches(&api.ArprtScreen{}, enqueue, builder.WithPredicates(predicate.GenerationChangedPredicate{})).Complete(r)
}
