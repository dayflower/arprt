// Package controller reconciles declarations and preserves authenticated observations.
package controller

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"time"

	api "github.com/dayflower/arprt/api/v1alpha1"
	"github.com/dayflower/arprt/internal/assignment"
	corev1 "k8s.io/api/core/v1"
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
	Client client.Client
	Reader client.Reader
}

func (r *ScreenReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var screen api.TrmnlScreen
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
			reason, message = ReasonUnsupportedFeature, "Job execution requires Phase 4"
		}
		screen.Status.ObservedGeneration = screen.Generation
		existing := meta.FindStatusCondition(screen.Status.Conditions, ConditionReady)
		if reason != "AwaitingRender" || existing == nil || existing.ObservedGeneration != screen.Generation {
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
	return ctrl.NewControllerManagedBy(mgr).For(&api.TrmnlScreen{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).Complete(r)
}

type DeviceReconciler struct {
	Client     client.Client
	Reader     client.Reader
	AuthReader client.Reader
	Resolver   assignment.Resolver
}

func (r *DeviceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var device api.TrmnlDevice
		reader := r.Reader
		if reader == nil {
			reader = r.Client
		}
		if err := reader.Get(ctx, req.NamespacedName, &device); err != nil {
			return client.IgnoreNotFound(err)
		}
		before := device.DeepCopy()
		var devices api.TrmnlDeviceList
		if err := reader.List(ctx, &devices, client.InNamespace(device.Namespace)); err != nil {
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
		resolved, err := r.Resolver.Resolve(ctx, &device)
		switch {
		case err == nil:
			if resolved.Spec.Source.HTTP == nil || (resolved.Spec.Source.HTTP.Method != "" && resolved.Spec.Source.HTTP.Method != "GET" && resolved.Spec.Source.HTTP.Method != "POST") {
				set(&device.Status.Conditions, device.Generation, ConditionAssignmentReady, metav1.ConditionFalse, ReasonUnsupportedFeature, "Assign HTTP GET or POST; Job execution requires Phase 4")
			} else {
				set(&device.Status.Conditions, device.Generation, ConditionAssignmentReady, metav1.ConditionTrue, ReasonAssignmentResolved, "Same-namespace HTTP screen resolved")
			}
		case errors.Is(err, assignment.ErrPlaylistDeferred), errors.Is(err, assignment.ErrDiscoveryDeferred):
			set(&device.Status.Conditions, device.Generation, ConditionAssignmentReady, metav1.ConditionFalse, ReasonUnsupportedFeature, "Playlists and automatic discovery require Phase 5")
		case apierrors.IsNotFound(err):
			set(&device.Status.Conditions, device.Generation, ConditionAssignmentReady, metav1.ConditionFalse, ReasonReferenceNotFound, "Referenced screen does not exist in this namespace")
		default:
			return err
		}
		if meta.FindStatusCondition(device.Status.Conditions, ConditionTargetKnown) == nil {
			set(&device.Status.Conditions, device.Generation, ConditionTargetKnown, metav1.ConditionUnknown, ReasonTargetUnobserved, "Waiting for authenticated capability observations")
		}
		if r.AuthReader != nil {
			var secret corev1.Secret
			authErr := r.AuthReader.Get(ctx, client.ObjectKey{Namespace: device.Namespace, Name: device.Spec.AuthSecretRef.Name}, &secret)
			key := string(secret.Data["apiKey"])
			raw, decodeErr := base64.RawURLEncoding.DecodeString(key)
			if decodeErr != nil {
				raw, decodeErr = base64.URLEncoding.DecodeString(key)
			}
			valid := authErr == nil && decodeErr == nil && len(raw) >= 32 && len(key) <= 512 && !strings.ContainsAny(key, "\r\n")
			authState, authReason, authMessage := metav1.ConditionTrue, "CredentialsConfigured", "Authentication Secret is usable"
			if !valid {
				authState, authReason, authMessage = metav1.ConditionFalse, "InvalidCredentials", "Provide a same-namespace Secret with apiKey encoding at least 32 random bytes as URL-safe base64"
			}
			set(&device.Status.Conditions, device.Generation, "AuthenticationReady", authState, authReason, authMessage)
		}
		failure := ""
		for _, kind := range []string{ConditionIdentityUnique, ConditionAssignmentReady, "AuthenticationReady"} {
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
