// Package controller reconciles declarations and preserves authenticated observations.
package controller

import (
	"context"
	"errors"
	"reflect"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/assignment"
	"github.com/dayflower/arprt/internal/credentials"
	"github.com/dayflower/arprt/internal/deviceidentity"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	ConditionReady            = "Ready"
	ConditionAssignmentReady  = "AssignmentReady"
	ConditionTargetKnown      = "TargetKnown"
	ConditionIdentityUnique   = "IdentityUnique"
	ReasonUnsupportedFeature  = "UnsupportedFeature"
	ReasonReferenceNotFound   = "ReferenceNotFound"
	ReasonAssignmentResolved  = "AssignmentResolved"
	ReasonTargetUnobserved    = "TargetUnobserved"
	ReasonDuplicateHardwareID = "DuplicateHardwareID"
	ReasonIdentityUnique      = "IdentityUnique"
)

func set(conditions *[]metav1.Condition, generation int64, kind string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conditions, metav1.Condition{Type: kind, Status: status, Reason: reason, Message: message, ObservedGeneration: generation})
}

type ScreenReconciler struct {
	Client     client.Client
	Reader     client.Reader
	JobEnabled bool
}

func (r *ScreenReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var screen api.ArprtScreen
		reader := r.Reader
		if reader == nil {
			reader = r.Client
		}
		if err := reader.Get(ctx, req.NamespacedName, &screen); err != nil {
			return client.IgnoreNotFound(err)
		}
		before := screen.DeepCopy()
		reason, message := "AwaitingRender", "HTTP renders on the next authenticated display request"
		if screen.Spec.Source.Job != nil {
			reason, message = "JobPending", "Waiting for an assigned device with a verified target"
			if !r.JobEnabled {
				reason, message = "JobSchedulingDisabled", "Configure --job-pvc and --controller-pod to enable Job rendering"
			}
		}
		screen.Status.ObservedGeneration = screen.Generation
		existing := meta.FindStatusCondition(screen.Status.Conditions, ConditionReady)
		if existing == nil || existing.ObservedGeneration != screen.Generation || reason == "JobSchedulingDisabled" {
			set(&screen.Status.Conditions, screen.Generation, ConditionReady, metav1.ConditionFalse, reason, message)
		}
		if reflect.DeepEqual(before.Status, screen.Status) {
			return nil
		}
		return r.Client.Status().Patch(ctx, &screen, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
	return ctrl.Result{}, err
}

func (r *ScreenReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&api.ArprtScreen{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).Complete(r)
}

type DeviceReconciler struct {
	Refresh    time.Duration
	Client     client.Client
	Reader     client.Reader
	AuthReader client.Reader
	Resolver   assignment.Resolver
	Identity   *deviceidentity.Service
}

func (r *DeviceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var device api.ArprtDevice
		reader := r.Reader
		if reader == nil {
			reader = r.Client
		}
		if err := reader.Get(ctx, req.NamespacedName, &device); err != nil {
			return client.IgnoreNotFound(err)
		}
		before := device.DeepCopy()
		identity := r.Identity
		if identity == nil {
			identity = &deviceidentity.Service{Reader: reader}
		}
		bound, boundErr := identity.Binding(ctx, &device)
		if boundErr == nil && bound != "" {
			device.Status.HardwareID = bound
		}
		id, identityErr := identity.Resolve(ctx, &device)
		if identityErr != nil || boundErr != nil {
			reason := "IdentityUnavailable"
			if errors.Is(identityErr, deviceidentity.ErrMismatch) {
				reason = "HardwareIdentityChanged"
			}
			set(&device.Status.Conditions, device.Generation, "IdentityReady", metav1.ConditionFalse, reason, "Check the MAC source; changing registered hardware requires replacing the device")
			set(&device.Status.Conditions, device.Generation, ConditionIdentityUnique, metav1.ConditionUnknown, "IdentityUnavailable", "Hardware identity cannot be resolved")
		} else if id == "" {
			set(&device.Status.Conditions, device.Generation, "IdentityReady", metav1.ConditionFalse, "AwaitingSetup", "Complete setup to register this device")
			set(&device.Status.Conditions, device.Generation, ConditionIdentityUnique, metav1.ConditionUnknown, "AwaitingSetup", "Hardware identity has not been registered")
		} else {
			reason := "IdentityConfigured"
			message := "Hardware identity is configured"
			if bound != "" {
				reason = "IdentityRegistered"
				message = "Hardware identity is registered"
			}
			set(&device.Status.Conditions, device.Generation, "IdentityReady", metav1.ConditionTrue, reason, message)
			unique, err := identity.Unique(ctx, &device, id)
			if err != nil {
				return err
			}
			if unique {
				set(&device.Status.Conditions, device.Generation, ConditionIdentityUnique, metav1.ConditionTrue, ReasonIdentityUnique, "Hardware identity is unique in this namespace")
			} else {
				set(&device.Status.Conditions, device.Generation, ConditionIdentityUnique, metav1.ConditionFalse, ReasonDuplicateHardwareID, "Multiple registered devices use this hardware identity")
			}
		}
		resolved, err := r.Resolver.Resolve(ctx, &device)
		switch {
		case err == nil:
			if resolved.Spec.Source.HTTP == nil && resolved.Spec.Source.Job == nil {
				set(&device.Status.Conditions, device.Generation, ConditionAssignmentReady, metav1.ConditionFalse, ReasonUnsupportedFeature, "Assign an HTTP or Job screen")
			} else {
				set(&device.Status.Conditions, device.Generation, ConditionAssignmentReady, metav1.ConditionTrue, ReasonAssignmentResolved, "Same-namespace screen resolved")
			}
		case errors.Is(err, assignment.ErrEmpty):
			set(&device.Status.Conditions, device.Generation, ConditionAssignmentReady, metav1.ConditionFalse, "NoEligibleScreens", "Assignment contains no existing screens in this namespace")
		case apierrors.IsNotFound(err):
			set(&device.Status.Conditions, device.Generation, ConditionAssignmentReady, metav1.ConditionFalse, ReasonReferenceNotFound, "Referenced resource does not exist in this namespace")
		default:
			return err
		}
		if meta.FindStatusCondition(device.Status.Conditions, ConditionTargetKnown) == nil {
			set(&device.Status.Conditions, device.Generation, ConditionTargetKnown, metav1.ConditionUnknown, ReasonTargetUnobserved, "Waiting for authenticated capability observations")
		}
		if r.AuthReader != nil {
			_, authErr := credentials.Read(ctx, r.AuthReader, &device)
			valid := authErr == nil
			authState, authReason, authMessage := metav1.ConditionTrue, "CredentialsConfigured", "Authentication Secret is usable"
			if !valid {
				authState, authReason, authMessage = metav1.ConditionFalse, "InvalidCredentials", "Provide a same-namespace Secret with apiKey encoding at least 32 random bytes as URL-safe base64"
				if errors.Is(authErr, credentials.ErrNotProvisioned) {
					authReason, authMessage = "AwaitingSetup", "Complete setup to provision the authentication key"
				}
			}
			set(&device.Status.Conditions, device.Generation, "AuthenticationReady", authState, authReason, authMessage)
		}
		failure := ""
		for _, kind := range []string{"IdentityReady", ConditionIdentityUnique, ConditionAssignmentReady, "AuthenticationReady"} {
			c := meta.FindStatusCondition(device.Status.Conditions, kind)
			if c != nil && c.Status == metav1.ConditionFalse {
				failure = c.Reason
				break
			}
		}
		existing := meta.FindStatusCondition(device.Status.Conditions, ConditionReady)
		if failure != "" {
			set(&device.Status.Conditions, device.Generation, ConditionReady, metav1.ConditionFalse, failure, "Resolve device identity, credentials and assignment conditions")
		} else if existing == nil || existing.ObservedGeneration != device.Generation || device.Status.Display == nil {
			set(&device.Status.Conditions, device.Generation, ConditionReady, metav1.ConditionFalse, "AwaitingDisplay", "Waiting for an authenticated display request with a verified target")
		}
		refresh := r.Refresh
		if refresh <= 0 {
			refresh = 5 * time.Minute
		}
		seenStatus, seenReason, seenMessage := metav1.ConditionTrue, "DeviceSeen", "Authenticated display request observed recently"
		if device.Status.LastSeenAt == nil {
			seenStatus, seenReason, seenMessage = metav1.ConditionUnknown, "NeverSeen", "Waiting for an authenticated display request"
		} else if time.Since(device.Status.LastSeenAt.Time) > max(3*refresh, 2*time.Minute) {
			seenStatus, seenReason, seenMessage = metav1.ConditionFalse, "DeviceSilent", "No display request within three polling intervals; check power and network"
		}
		set(&device.Status.Conditions, device.Generation, "DeviceSeen", seenStatus, seenReason, seenMessage)
		device.Status.ObservedGeneration = device.Generation
		if reflect.DeepEqual(before.Status, device.Status) {
			return nil
		}
		return r.Client.Status().Patch(ctx, &device, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
	return ctrl.Result{RequeueAfter: time.Minute}, err
}

func (r *DeviceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Any identity edit/delete can change duplicate-ID readiness for other devices.
	// Screen create/edit/delete can resolve or invalidate explicit assignments.
	enqueue := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		var devices api.ArprtDeviceList
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
	return ctrl.NewControllerManagedBy(mgr).Named("arprtdevice").
		Watches(&api.ArprtDevice{}, enqueue, builder.WithPredicates(predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				oldDevice, oldOK := e.ObjectOld.(*api.ArprtDevice)
				newDevice, newOK := e.ObjectNew.(*api.ArprtDevice)
				return oldOK && newOK && (oldDevice.Generation != newDevice.Generation || oldDevice.Status.HardwareID != newDevice.Status.HardwareID)
			},
		})).
		Watches(&api.ArprtScreen{}, enqueue, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&api.ArprtPlaylist{}, enqueue, builder.WithPredicates(predicate.GenerationChangedPredicate{})).Complete(r)
}
