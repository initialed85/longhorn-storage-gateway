package controller

import (
	"context"
	"testing"
	"time"

	storagev1alpha1 "github.com/initialed85/longhorn-nfs-gateway/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme, storagev1alpha1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func testExport() *storagev1alpha1.LonghornNFSExport {
	return &storagev1alpha1.LonghornNFSExport{
		TypeMeta:   metav1.TypeMeta{APIVersion: storagev1alpha1.GroupVersion.String(), Kind: "LonghornNFSExport"},
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "storage", UID: "export-uid", Generation: 1},
		Spec: storagev1alpha1.LonghornNFSExportSpec{
			PVCRef: storagev1alpha1.PVCReference{Name: "demo-pvc"}, Mode: storagev1alpha1.ModeRWO, Protocol: storagev1alpha1.ProtocolNFSv3,
			Handoff: storagev1alpha1.HandoffSpec{NativePodSelector: map[string]string{"storage.k8s-darwin.dev/native": "true"}},
		},
	}
}

func testPVCAndPV() (*corev1.PersistentVolumeClaim, *corev1.PersistentVolume) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-pvc", Namespace: "storage", UID: "pvc-uid"},
		Spec:       corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, VolumeName: "demo-pv"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "demo-pv"},
		Spec:       corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: longhornDriver}}},
	}
	return pvc, pv
}

func requestFor(e *storagev1alpha1.LonghornNFSExport) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: e.Namespace, Name: e.Name}}
}

func newReconciler(t *testing.T, objects ...client.Object) (*LonghornNFSExportReconciler, client.Client) {
	t.Helper()
	scheme := testScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&storagev1alpha1.LonghornNFSExport{}).WithObjects(objects...).Build()
	return &LonghornNFSExportReconciler{Client: c, Scheme: scheme}, c
}

func TestReconcileCreatesDeterministicResourcesAndHandoff(t *testing.T) {
	export := testExport()
	pvc, pv := testPVCAndPV()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "native", Namespace: "storage", Labels: map[string]string{"storage.k8s-darwin.dev/native": "true"}, Annotations: map[string]string{"example.com/keep": "yes"}}}
	r, c := newReconciler(t, export, pvc, pv, pod)
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, requestFor(export)); err != nil {
		t.Fatal(err)
	}
	var stored storagev1alpha1.LonghornNFSExport
	if err := c.Get(ctx, client.ObjectKeyFromObject(export), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Finalizers) != 1 || stored.Finalizers[0] != storagev1alpha1.Finalizer {
		t.Fatalf("finalizer not installed: %#v", stored.Finalizers)
	}
	if _, err := r.Reconcile(ctx, requestFor(export)); err != nil {
		t.Fatal(err)
	}

	var deployment appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Namespace: "storage", Name: helperName(export)}, &deployment); err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Fatalf("unexpected deployment strategy: %#v", deployment.Spec)
	}
	var service corev1.Service
	if err := c.Get(ctx, types.NamespacedName{Namespace: "storage", Name: serviceName(export)}, &service); err != nil {
		t.Fatal(err)
	}
	service.Spec.ClusterIP = "10.43.7.7"
	if err := c.Update(ctx, &service); err != nil {
		t.Fatal(err)
	}
	deployment.Status.ReadyReplicas = 1
	deployment.Status.AvailableReplicas = 1
	if err := c.Status().Update(ctx, &deployment); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, requestFor(export)); err != nil {
		t.Fatal(err)
	}

	if err := c.Get(ctx, client.ObjectKeyFromObject(&stored), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != storagev1alpha1.PhaseReady || stored.Status.Endpoint == nil || stored.Status.Endpoint.Server != "10.43.7.7" {
		t.Fatalf("unexpected status: %#v", stored.Status)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
		t.Fatal(err)
	}
	if pod.Annotations["example.com/keep"] != "yes" || pod.Annotations[storagev1alpha1.AnnotationNFSServer] != "10.43.7.7" || pod.Annotations[storagev1alpha1.AnnotationNFSVersion] != "3" {
		t.Fatalf("handoff annotations were not published safely: %#v", pod.Annotations)
	}
}

func TestRWORejectsAnotherConsumer(t *testing.T) {
	export := testExport()
	pvc, pv := testPVCAndPV()
	consumer := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "consumer", Namespace: "storage"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvc.Name}}}}}}
	r, c := newReconciler(t, export, pvc, pv, consumer)
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, requestFor(export)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, requestFor(export)); err != nil {
		t.Fatal(err)
	}
	var stored storagev1alpha1.LonghornNFSExport
	if err := c.Get(ctx, client.ObjectKeyFromObject(export), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Reason != "PVCInUse" || stored.Status.Phase != storagev1alpha1.PhaseDegraded {
		t.Fatalf("expected PVCInUse status, got %#v", stored.Status)
	}
	var deployment appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Namespace: export.Namespace, Name: helperName(export)}, &deployment); err == nil {
		t.Fatal("helper should not be created while RWO PVC has another consumer")
	}
}

func TestFinalizeDeletesOnlyOwnedHelpers(t *testing.T) {
	export := testExport()
	export.Finalizers = []string{storagev1alpha1.Finalizer}
	now := metav1.NewTime(time.Now())
	export.DeletionTimestamp = &now
	pvc, pv := testPVCAndPV()
	labelsFor := resourceLabels(export)
	owner := *metav1.NewControllerRef(export, storagev1alpha1.GroupVersion.WithKind("LonghornNFSExport"))
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: helperName(export), Namespace: export.Namespace, Labels: labelsFor, OwnerReferences: []metav1.OwnerReference{owner}}}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: serviceName(export), Namespace: export.Namespace, Labels: labelsFor, OwnerReferences: []metav1.OwnerReference{owner}}}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: helperConfigName(export), Namespace: export.Namespace, Labels: labelsFor, OwnerReferences: []metav1.OwnerReference{owner}}}
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: policyName(export), Namespace: export.Namespace, Labels: labelsFor, OwnerReferences: []metav1.OwnerReference{owner}}}
	r, c := newReconciler(t, export, pvc, pv, deployment, service, config, policy)
	if _, err := r.Reconcile(context.Background(), requestFor(export)); err != nil {
		t.Fatal(err)
	}
	for _, obj := range []client.Object{deployment, service, config, policy} {
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(obj), obj); err == nil {
			t.Errorf("owned helper %T still exists", obj)
		}
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pvc), pvc); err != nil {
		t.Fatalf("PVC was unexpectedly deleted: %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(pv), pv); err != nil {
		t.Fatalf("PV was unexpectedly deleted: %v", err)
	}
	var remaining storagev1alpha1.LonghornNFSExport
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(export), &remaining); err == nil && len(remaining.Finalizers) > 0 {
		t.Fatal("finalizer was not removed")
	}
}
