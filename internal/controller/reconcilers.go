// Package controller implements Phase 1 readiness, without renderer execution.
package controller

import (
	"context"
	"errors"
	"reflect"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/assignment"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	ConditionReady                = "Ready"
	ConditionAssignmentReady      = "AssignmentReady"
	ConditionTargetKnown          = "TargetKnown"
	ConditionIdentityUnique       = "IdentityUnique"
	ReasonRenderingNotImplemented = "RenderingNotImplemented"
	ReasonUnsupportedFeature      = "UnsupportedFeature"
	ReasonReferenceNotFound       = "ReferenceNotFound"
	ReasonAssignmentResolved      = "AssignmentResolved"
	ReasonTargetUnobserved        = "TargetUnobserved"
	ReasonDuplicateHardwareID     = "DuplicateHardwareID"
	ReasonIdentityUnique          = "IdentityUnique"
)

func set(conditions *[]metav1.Condition, generation int64, kind string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conditions, metav1.Condition{Type: kind, Status: status, Reason: reason, Message: message, ObservedGeneration: generation})
}

type ScreenReconciler struct{ Client client.Client }

func (r *ScreenReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var screen api.TrmnlScreen
		if err := r.Client.Get(ctx, req.NamespacedName, &screen); err != nil {
			return client.IgnoreNotFound(err)
		}
		before := screen.DeepCopy()
		reason, message := ReasonRenderingNotImplemented, "HTTP GET execution is implemented in Phase 2"
		if screen.Spec.Source.Job != nil {
			reason, message = ReasonUnsupportedFeature, "Job execution requires Phase 4"
		} else if screen.Spec.Source.HTTP != nil && screen.Spec.Source.HTTP.Method == "POST" {
			reason, message = ReasonUnsupportedFeature, "HTTP POST execution requires Phase 3"
		}
		screen.Status.ObservedGeneration = screen.Generation
		set(&screen.Status.Conditions, screen.Generation, ConditionReady, metav1.ConditionFalse, reason, message)
		if reflect.DeepEqual(before.Status, screen.Status) {
			return nil
		}
		return r.Client.Status().Patch(ctx, &screen, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
	return ctrl.Result{}, err
}

func (r *ScreenReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&api.TrmnlScreen{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).Complete(r)
}

type DeviceReconciler struct {
	Client   client.Client
	Resolver assignment.Resolver
}

func (r *DeviceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var device api.TrmnlDevice
		if err := r.Client.Get(ctx, req.NamespacedName, &device); err != nil {
			return client.IgnoreNotFound(err)
		}
		before := device.DeepCopy()
		var devices api.TrmnlDeviceList
		if err := r.Client.List(ctx, &devices, client.InNamespace(device.Namespace)); err != nil {
			return err
		}
		unique := true
		for _, other := range devices.Items {
			if other.UID != device.UID && other.Spec.HardwareID == device.Spec.HardwareID {
				unique = false
				break
			}
		}
		if unique {
			set(&device.Status.Conditions, device.Generation, ConditionIdentityUnique, metav1.ConditionTrue, ReasonIdentityUnique, "Hardware identity is unique in this namespace")
		} else {
			set(&device.Status.Conditions, device.Generation, ConditionIdentityUnique, metav1.ConditionFalse, ReasonDuplicateHardwareID, "Multiple registered devices use this hardware identity")
		}
		_, err := r.Resolver.Resolve(ctx, &device)
		switch {
		case err == nil:
			set(&device.Status.Conditions, device.Generation, ConditionAssignmentReady, metav1.ConditionTrue, ReasonAssignmentResolved, "Same-namespace screen reference resolved; rendering remains unavailable")
		case errors.Is(err, assignment.ErrPlaylistDeferred), errors.Is(err, assignment.ErrDiscoveryDeferred):
			set(&device.Status.Conditions, device.Generation, ConditionAssignmentReady, metav1.ConditionFalse, ReasonUnsupportedFeature, "Playlists and automatic discovery require Phase 5")
		case apierrors.IsNotFound(err):
			set(&device.Status.Conditions, device.Generation, ConditionAssignmentReady, metav1.ConditionFalse, ReasonReferenceNotFound, "Referenced screen does not exist in this namespace")
		default:
			return err
		}
		// Phase 1 has no authenticated protocol observations or capability resolver.
		set(&device.Status.Conditions, device.Generation, ConditionTargetKnown, metav1.ConditionUnknown, ReasonTargetUnobserved, "Authenticated capability observations require Phase 2")
		set(&device.Status.Conditions, device.Generation, ConditionReady, metav1.ConditionFalse, ReasonRenderingNotImplemented, "Device delivery requires Phase 2")
		device.Status.ObservedGeneration = device.Generation
		if reflect.DeepEqual(before.Status, device.Status) {
			return nil
		}
		return r.Client.Status().Patch(ctx, &device, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
	return ctrl.Result{}, err
}

func (r *DeviceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Any identity edit/delete can change duplicate-ID readiness for other devices.
	// Screen create/edit/delete can resolve or invalidate explicit assignments.
	enqueue := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		var devices api.TrmnlDeviceList
		if err := r.Client.List(ctx, &devices, client.InNamespace(obj.GetNamespace())); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "Unable to enqueue device assignments")
			return nil
		}
		requests := make([]reconcile.Request, 0, len(devices.Items))
		for _, d := range devices.Items {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&d)})
		}
		return requests
	})
	return ctrl.NewControllerManagedBy(mgr).Named("trmnldevice").
		Watches(&api.TrmnlDevice{}, enqueue, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&api.TrmnlScreen{}, enqueue, builder.WithPredicates(predicate.GenerationChangedPredicate{})).Complete(r)
}
