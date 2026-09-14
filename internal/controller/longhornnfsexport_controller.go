package controller

import (
	"context"
	"fmt"
	"hash/fnv"
	"reflect"
	"strconv"
	"strings"

	storagev1alpha1 "github.com/initialed85/longhorn-nfs-gateway/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	longhornDriver               = "driver.longhorn.io"
	defaultNFSGaneshaImage       = "ghcr.io/initialed85/longhorn-nfs-gateway-ganesha:4.3"
	defaultMountPort       int32 = 20048
	defaultNFSPort         int32 = 2049
	defaultExportPath            = "/export"
	requeueAfterSeconds          = 30

	labelManagedBy = "storage.k8s-darwin.dev/managed-by"
	labelExport    = "storage.k8s-darwin.dev/export"
	managedByValue = "longhorn-nfs-gateway"
)

type LonghornNFSExportReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func (r *LonghornNFSExportReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.LonghornNFSExport{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Watches(&corev1.PersistentVolumeClaim{}, handler.EnqueueRequestsFromMapFunc(r.mapPVC)).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.mapPod)).
		Complete(r)
}

func (r *LonghornNFSExportReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var export storagev1alpha1.LonghornNFSExport
	if err := r.Get(ctx, req.NamespacedName, &export); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !export.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &export)
	}
	if !controllerutil.ContainsFinalizer(&export, storagev1alpha1.Finalizer) {
		controllerutil.AddFinalizer(&export, storagev1alpha1.Finalizer)
		return ctrl.Result{}, r.Update(ctx, &export)
	}

	if export.Spec.Protocol != storagev1alpha1.ProtocolNFSv3 ||
		(export.Spec.Mode != storagev1alpha1.ModeRWO && export.Spec.Mode != storagev1alpha1.ModeRWX) {
		return ctrl.Result{}, r.setStatus(ctx, &export, storagev1alpha1.PhaseError, "InvalidSpec", "mode must be RWO or RWX and protocol must be NFSv3", conditionValues{
			storagev1alpha1.ConditionProtocolCompatible: conditionValue{metav1.ConditionFalse, "InvalidSpec", "only NFSv3 is supported"},
			storagev1alpha1.ConditionPVCBound:           conditionValue{metav1.ConditionUnknown, "InvalidSpec", "PVC validation has not started"},
			storagev1alpha1.ConditionHelperReady:        conditionValue{metav1.ConditionFalse, "InvalidSpec", "helper is not created"},
			storagev1alpha1.ConditionEndpointReady:      conditionValue{metav1.ConditionFalse, "InvalidSpec", "endpoint is not created"},
			storagev1alpha1.ConditionCleanup:            conditionValue{metav1.ConditionTrue, "NotRequired", "cleanup is not required"},
		}, nil, nil)
	}

	pvc, pv, reason, message, err := r.getBoundLonghornPVC(ctx, &export)
	if err != nil {
		return ctrl.Result{}, err
	}
	if pvc == nil {
		return ctrl.Result{RequeueAfter: requeueAfterSeconds}, r.setStatus(ctx, &export, storagev1alpha1.PhasePending, reason, message, pendingConditions(reason, message), nil, nil)
	}

	if export.Spec.Mode == storagev1alpha1.ModeRWO {
		inUse, err := r.hasOtherPVCConsumers(ctx, &export, pvc.Name)
		if err != nil {
			return ctrl.Result{}, err
		}
		if inUse {
			message := fmt.Sprintf("PVC %s has an active consumer other than this gateway", pvc.Name)
			return ctrl.Result{RequeueAfter: requeueAfterSeconds}, r.setStatus(ctx, &export, storagev1alpha1.PhaseDegraded, "PVCInUse", message, pendingConditions("PVCInUse", message), nil, nil)
		}
	}

	if pv == nil {
		message := "bound PVC has no resolved Longhorn PV"
		return ctrl.Result{RequeueAfter: requeueAfterSeconds}, r.setStatus(ctx, &export, storagev1alpha1.PhasePending, "PVNotFound", message, pendingConditions("PVNotFound", message), nil, nil)
	}

	resources, err := r.ensureResources(ctx, &export)
	if err != nil {
		message := fmt.Sprintf("ensuring gateway resources: %v", err)
		return ctrl.Result{RequeueAfter: requeueAfterSeconds}, r.setStatus(ctx, &export, storagev1alpha1.PhaseDegraded, "ResourceError", message, pendingConditions("ResourceError", message), nil, nil)
	}

	helperReady := resources.deployment.Status.ReadyReplicas >= 1 && resources.deployment.Status.AvailableReplicas >= 1
	endpointReady := helperReady && serviceEndpointReady(resources.service)
	helperStatus := &storagev1alpha1.HelperStatus{
		Name:           resources.deployment.Name,
		ServiceName:    resources.service.Name,
		Ready:          helperReady,
		ObservedPVCUID: string(pvc.UID),
		ObservedPVName: pv.Name,
	}
	var endpoint *storagev1alpha1.EndpointStatus
	if endpointReady {
		endpoint = &storagev1alpha1.EndpointStatus{
			Server:     resources.service.Spec.ClusterIP,
			Export:     defaultExportPath,
			Version:    3,
			MountPort:  resources.mountPort,
			NFSPort:    resources.nfsPort,
			Generation: strconv.FormatInt(export.Generation, 10),
		}
	}

	if err := r.updateHandoff(ctx, &export, endpoint); err != nil {
		message := fmt.Sprintf("updating native Pod handoff: %v", err)
		return ctrl.Result{RequeueAfter: requeueAfterSeconds}, r.setStatus(ctx, &export, storagev1alpha1.PhaseDegraded, "HandoffError", message, pendingConditions("HandoffError", message), endpoint, helperStatus)
	}

	if endpointReady {
		conditions := conditionValues{
			storagev1alpha1.ConditionPVCBound:           conditionValue{metav1.ConditionTrue, "Bound", "PVC is Bound to a Longhorn volume"},
			storagev1alpha1.ConditionProtocolCompatible: conditionValue{metav1.ConditionTrue, "Supported", "NFSv3 is supported"},
			storagev1alpha1.ConditionHelperReady:        conditionValue{metav1.ConditionTrue, "Ready", "NFS helper Deployment has a Ready replica"},
			storagev1alpha1.ConditionEndpointReady:      conditionValue{metav1.ConditionTrue, "Ready", "Service has a ClusterIP and the helper is Ready"},
			storagev1alpha1.ConditionCleanup:            conditionValue{metav1.ConditionTrue, "Active", "controller-owned resources are tracked by the finalizer"},
		}
		return ctrl.Result{RequeueAfter: requeueAfterSeconds}, r.setStatus(ctx, &export, storagev1alpha1.PhaseReady, "Ready", "NFSv3 endpoint is ready", conditions, endpoint, helperStatus)
	}

	message = "waiting for the helper Pod and Service endpoint to become ready"
	conditions := conditionValues{
		storagev1alpha1.ConditionPVCBound:           conditionValue{metav1.ConditionTrue, "Bound", "PVC is Bound to a Longhorn volume"},
		storagev1alpha1.ConditionProtocolCompatible: conditionValue{metav1.ConditionTrue, "Supported", "NFSv3 is supported"},
		storagev1alpha1.ConditionHelperReady:        conditionValue{boolCondition(helperReady), helperReason(helperReady), helperMessage(helperReady)},
		storagev1alpha1.ConditionEndpointReady:      conditionValue{metav1.ConditionFalse, "WaitingForHelper", message},
		storagev1alpha1.ConditionCleanup:            conditionValue{metav1.ConditionTrue, "Active", "controller-owned resources are tracked by the finalizer"},
	}
	return ctrl.Result{RequeueAfter: requeueAfterSeconds}, r.setStatus(ctx, &export, storagev1alpha1.PhasePending, "WaitingForReady", message, conditions, endpoint, helperStatus)
}

type managedResources struct {
	deployment *appsv1.Deployment
	service    *corev1.Service
	mountPort  int32
	nfsPort    int32
}

func (r *LonghornNFSExportReconciler) ensureResources(ctx context.Context, export *storagev1alpha1.LonghornNFSExport) (managedResources, error) {
	mountPort, nfsPort := portsFor(export)
	labelsFor := resourceLabels(export)
	owner := func(obj client.Object) error { return controllerutil.SetControllerReference(export, obj, r.Scheme) }

	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: helperConfigName(export), Namespace: export.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, config, func() error {
		config.Labels = labelsFor
		config.Data = map[string]string{"ganesha.conf": ganeshaConfig(mountPort, nfsPort)}
		return owner(config)
	})
	if err != nil {
		return managedResources{}, err
	}

	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: helperName(export), Namespace: export.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		want := desiredDeployment(export, labelsFor, mountPort, nfsPort)
		deployment.Labels = want.Labels
		deployment.Spec = want.Spec
		return owner(deployment)
	})
	if err != nil {
		return managedResources{}, err
	}

	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: serviceName(export), Namespace: export.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, service, func() error {
		clusterIP, clusterIPs, ipFamilies, ipFamilyPolicy := service.Spec.ClusterIP, service.Spec.ClusterIPs, service.Spec.IPFamilies, service.Spec.IPFamilyPolicy
		want := desiredService(export, labelsFor, mountPort, nfsPort)
		service.Labels = want.Labels
		service.Spec = want.Spec
		service.Spec.ClusterIP, service.Spec.ClusterIPs, service.Spec.IPFamilies, service.Spec.IPFamilyPolicy = clusterIP, clusterIPs, ipFamilies, ipFamilyPolicy
		return owner(service)
	})
	if err != nil {
		return managedResources{}, err
	}

	if networkPolicyEnabled(export) {
		policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: policyName(export), Namespace: export.Namespace}}
		_, err = controllerutil.CreateOrUpdate(ctx, r.Client, policy, func() error {
			want := desiredNetworkPolicy(export, labelsFor, mountPort, nfsPort)
			policy.Labels = want.Labels
			policy.Spec = want.Spec
			return owner(policy)
		})
		if err != nil {
			return managedResources{}, err
		}
	}

	return managedResources{deployment: deployment, service: service, mountPort: mountPort, nfsPort: nfsPort}, nil
}

func getStringHash(value string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return fmt.Sprintf("%08x", h.Sum32())
}

func boundedName(base, suffix string) string {
	name := strings.ToLower(strings.TrimSuffix(base, "-")) + suffix
	if len(name) <= 63 {
		return name
	}
	return strings.TrimSuffix(name[:54], "-") + "-" + getStringHash(name)[:8]
}

func helperName(e *storagev1alpha1.LonghornNFSExport) string { return boundedName(e.Name, "-helper") }
func helperConfigName(e *storagev1alpha1.LonghornNFSExport) string {
	return boundedName(e.Name, "-ganesha")
}
func policyName(e *storagev1alpha1.LonghornNFSExport) string { return boundedName(e.Name, "-nfs") }
func serviceName(e *storagev1alpha1.LonghornNFSExport) string {
	if e.Spec.Service.Name != "" {
		return e.Spec.Service.Name
	}
	return boundedName(e.Name, "-nfs")
}

func resourceLabels(e *storagev1alpha1.LonghornNFSExport) map[string]string {
	return map[string]string{labelManagedBy: managedByValue, labelExport: e.Name}
}

func portsFor(e *storagev1alpha1.LonghornNFSExport) (int32, int32) {
	mountPort, nfsPort := e.Spec.Service.MountPort, e.Spec.Service.NFSPort
	if mountPort == 0 {
		mountPort = defaultMountPort
	}
	if nfsPort == 0 {
		nfsPort = defaultNFSPort
	}
	return mountPort, nfsPort
}

func desiredDeployment(e *storagev1alpha1.LonghornNFSExport, labelsFor map[string]string, mountPort, nfsPort int32) *appsv1.Deployment {
	image := e.Spec.Helper.NFSGaneshaImage
	if image == "" {
		image = e.Spec.Helper.Image
	}
	if image == "" {
		image = defaultNFSGaneshaImage
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: helperName(e), Namespace: e.Namespace, Labels: labelsFor,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer(int32(1)),
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: labelsFor},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labelsFor},
				Spec: corev1.PodSpec{
					ServiceAccountName:            e.Spec.Helper.ServiceAccountName,
					NodeSelector:                  e.Spec.Helper.NodeSelector,
					TerminationGracePeriodSeconds: pointer(int64(30)),
					Containers: []corev1.Container{{
						Name: "nfs-ganesha", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
						Command: []string{"ganesha.nfsd"}, Args: []string{"-F", "-L", "STDOUT", "-f", "/etc/ganesha/ganesha.conf"},
						SecurityContext: &corev1.SecurityContext{
							Privileged: pointer(true), AllowPrivilegeEscalation: pointer(true), RunAsUser: pointer(int64(0)),
						},
						Ports: []corev1.ContainerPort{
							{Name: "nfs", ContainerPort: nfsPort, Protocol: corev1.ProtocolTCP},
							{Name: "mountd", ContainerPort: mountPort, Protocol: corev1.ProtocolTCP},
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString("nfs")}}, PeriodSeconds: 5,
						},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString("nfs")}}, InitialDelaySeconds: 10, PeriodSeconds: 10,
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "export", MountPath: defaultExportPath},
							{Name: "ganesha-config", MountPath: "/etc/ganesha/ganesha.conf", SubPath: "ganesha.conf", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "export", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: e.Spec.PVCRef.Name}}},
						{Name: "ganesha-config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: helperConfigName(e)}}}},
					},
				},
			},
		},
	}
}

func desiredService(e *storagev1alpha1.LonghornNFSExport, labelsFor map[string]string, mountPort, nfsPort int32) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: serviceName(e), Namespace: e.Namespace, Labels: labelsFor}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: labelsFor, Ports: []corev1.ServicePort{{Name: "nfs", Protocol: corev1.ProtocolTCP, Port: nfsPort, TargetPort: intstr.FromString("nfs")}, {Name: "mountd", Protocol: corev1.ProtocolTCP, Port: mountPort, TargetPort: intstr.FromString("mountd")}}}}
}

func desiredNetworkPolicy(e *storagev1alpha1.LonghornNFSExport, labelsFor map[string]string, mountPort, nfsPort int32) *networkingv1.NetworkPolicy {
	ports := []networkingv1.NetworkPolicyPort{{Protocol: pointer(corev1.ProtocolTCP), Port: pointer(intstr.FromInt(int(nfsPort)))}, {Protocol: pointer(corev1.ProtocolTCP), Port: pointer(intstr.FromInt(int(mountPort)))}}
	from := []networkingv1.NetworkPolicyPeer(nil)
	for _, cidr := range e.Spec.NetworkPolicy.AllowedCIDRs {
		from = append(from, networkingv1.NetworkPolicyPeer{IPBlock: &networkingv1.IPBlock{CIDR: cidr}})
	}
	return &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: policyName(e), Namespace: e.Namespace, Labels: labelsFor}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: labelsFor}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: from, Ports: ports}}}}
}

func ganeshaConfig(mountPort, nfsPort int32) string {
	return fmt.Sprintf(`NFS_CORE_PARAM {
  Protocols = 3;
  NFS_Port = %d;
  MNT_Port = %d;
  Bind_Addr = 0.0.0.0;
}
EXPORT_DEFAULTS {
  Access_Type = RW;
  Squash = No_Root_Squash;
  Protocols = 3;
  Transports = TCP;
}
EXPORT {
  Export_Id = 1;
  Path = /export;
  Pseudo = /export;
  Access_Type = RW;
  Squash = No_Root_Squash;
  Protocols = 3;
  Transports = TCP;
  FSAL { Name = VFS; }
}
`, nfsPort, mountPort)
}

func (r *LonghornNFSExportReconciler) getBoundLonghornPVC(ctx context.Context, e *storagev1alpha1.LonghornNFSExport) (*corev1.PersistentVolumeClaim, *corev1.PersistentVolume, string, string, error) {
	var pvc corev1.PersistentVolumeClaim
	key := types.NamespacedName{Namespace: e.Namespace, Name: e.Spec.PVCRef.Name}
	if err := r.Get(ctx, key, &pvc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, "PVCNotFound", fmt.Sprintf("PVC %s/%s does not exist", e.Namespace, e.Spec.PVCRef.Name), nil
		}
		return nil, nil, "PVCReadError", err.Error(), err
	}
	if pvc.Status.Phase != corev1.ClaimBound {
		return nil, nil, "PVCNotBound", fmt.Sprintf("PVC %s is %s", pvc.Name, pvc.Status.Phase), nil
	}
	if e.Spec.Mode == storagev1alpha1.ModeRWO && !hasAccessMode(pvc.Spec.AccessModes, corev1.ReadWriteOnce) && !hasAccessMode(pvc.Spec.AccessModes, corev1.ReadWriteOncePod) {
		return nil, nil, "AccessModeMismatch", "RWO export requires ReadWriteOnce or ReadWriteOncePod PVC", nil
	}
	if e.Spec.Mode == storagev1alpha1.ModeRWX && !hasAccessMode(pvc.Spec.AccessModes, corev1.ReadWriteMany) {
		return nil, nil, "AccessModeMismatch", "RWX export requires a ReadWriteMany PVC", nil
	}
	if pvc.Spec.VolumeName == "" {
		return nil, nil, "PVNotFound", "bound PVC has no volumeName", nil
	}
	var pv corev1.PersistentVolume
	if err := r.Get(ctx, types.NamespacedName{Name: pvc.Spec.VolumeName}, &pv); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, "PVNotFound", fmt.Sprintf("PV %s does not exist", pvc.Spec.VolumeName), nil
		}
		return nil, nil, "PVReadError", err.Error(), err
	}
	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != longhornDriver {
		return nil, nil, "NotLonghorn", fmt.Sprintf("PV %s is not backed by %s", pv.Name, longhornDriver), nil
	}
	return &pvc, &pv, "", "", nil
}

func hasAccessMode(modes []corev1.PersistentVolumeAccessMode, wanted corev1.PersistentVolumeAccessMode) bool {
	for _, mode := range modes {
		if mode == wanted {
			return true
		}
	}
	return false
}

func (r *LonghornNFSExportReconciler) mapPVC(ctx context.Context, obj client.Object) []reconcile.Request {
	var exports storagev1alpha1.LonghornNFSExportList
	if err := r.List(ctx, &exports, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for i := range exports.Items {
		if exports.Items[i].Spec.PVCRef.Name == obj.GetName() {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: exports.Items[i].Namespace, Name: exports.Items[i].Name}})
		}
	}
	return requests
}

func (r *LonghornNFSExportReconciler) mapPod(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	var exports storagev1alpha1.LonghornNFSExportList
	if err := r.List(ctx, &exports, client.InNamespace(pod.Namespace)); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0)
	for i := range exports.Items {
		if exports.Items[i].Spec.Mode == storagev1alpha1.ModeRWO && podHasPVC(pod, exports.Items[i].Spec.PVCRef.Name) {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: exports.Items[i].Namespace, Name: exports.Items[i].Name}})
		}
	}
	return requests
}

func (r *LonghornNFSExportReconciler) hasOtherPVCConsumers(ctx context.Context, e *storagev1alpha1.LonghornNFSExport, pvcName string) (bool, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(e.Namespace)); err != nil {
		return false, err
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		if pod.DeletionTimestamp != nil || podHasPVC(&pod, pvcName) {
			if !podHasPVC(&pod, pvcName) {
				continue
			}
			if ownedByExport(&pod, e) {
				continue
			}
			return true, nil
		}
	}
	return false, nil
}

func podHasPVC(pod *corev1.Pod, pvcName string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == pvcName {
			return true
		}
	}
	return false
}

func ownedByExport(obj metav1.Object, e *storagev1alpha1.LonghornNFSExport) bool {
	if ownedResourceByExport(obj, e) {
		return true
	}
	// Helper Pods are owned by the generated Deployment rather than directly by
	// the export. Labels are used only to recognize those Pods as this gateway's
	// consumer; finalizer deletion always requires the direct owner reference.
	return obj.GetLabels()[labelManagedBy] == managedByValue && obj.GetLabels()[labelExport] == e.Name
}

func ownedResourceByExport(obj metav1.Object, e *storagev1alpha1.LonghornNFSExport) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID != "" && ref.UID == e.UID && ref.Controller != nil && *ref.Controller {
			return true
		}
	}
	return false
}

func serviceEndpointReady(service *corev1.Service) bool {
	return service != nil && service.Spec.ClusterIP != "" && service.Spec.ClusterIP != corev1.ClusterIPNone && len(service.Spec.Ports) >= 2
}

func networkPolicyEnabled(e *storagev1alpha1.LonghornNFSExport) bool {
	return e.Spec.NetworkPolicy.Enabled == nil || *e.Spec.NetworkPolicy.Enabled
}
func handoffEnabled(e *storagev1alpha1.LonghornNFSExport) bool {
	return e.Spec.Handoff.Enabled == nil || *e.Spec.Handoff.Enabled
}

func (r *LonghornNFSExportReconciler) updateHandoff(ctx context.Context, e *storagev1alpha1.LonghornNFSExport, endpoint *storagev1alpha1.EndpointStatus) error {
	if !handoffEnabled(e) || len(e.Spec.Handoff.NativePodSelector) == 0 {
		return nil
	}
	selector := labels.SelectorFromSet(labels.Set(e.Spec.Handoff.NativePodSelector))
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(e.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		before := pod.DeepCopy()
		if endpoint == nil {
			removeHandoffAnnotations(pod)
		} else {
			if pod.Annotations == nil {
				pod.Annotations = map[string]string{}
			}
			pod.Annotations[storagev1alpha1.AnnotationNFSServer] = endpoint.Server
			pod.Annotations[storagev1alpha1.AnnotationNFSExport] = endpoint.Export
			pod.Annotations[storagev1alpha1.AnnotationNFSVersion] = strconv.Itoa(int(endpoint.Version))
			pod.Annotations[storagev1alpha1.AnnotationNFSMountPort] = strconv.Itoa(int(endpoint.MountPort))
			pod.Annotations[storagev1alpha1.AnnotationNFSGeneration] = endpoint.Generation
		}
		if !reflect.DeepEqual(before.Annotations, pod.Annotations) {
			if err := r.Update(ctx, pod); err != nil {
				return err
			}
		}
	}
	return nil
}

func removeHandoffAnnotations(pod *corev1.Pod) {
	if pod.Annotations == nil {
		return
	}
	for _, key := range []string{storagev1alpha1.AnnotationNFSServer, storagev1alpha1.AnnotationNFSExport, storagev1alpha1.AnnotationNFSVersion, storagev1alpha1.AnnotationNFSMountPort, storagev1alpha1.AnnotationNFSGeneration} {
		delete(pod.Annotations, key)
	}
}

func (r *LonghornNFSExportReconciler) finalize(ctx context.Context, e *storagev1alpha1.LonghornNFSExport) (ctrl.Result, error) {
	if err := r.updateHandoff(ctx, e, nil); err != nil {
		return ctrl.Result{}, r.setStatus(ctx, e, storagev1alpha1.PhaseDegraded, "CleanupFailed", err.Error(), pendingConditions("CleanupFailed", err.Error()), nil, nil)
	}
	resources := []client.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: helperName(e), Namespace: e.Namespace}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: serviceName(e), Namespace: e.Namespace}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: helperConfigName(e), Namespace: e.Namespace}},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: policyName(e), Namespace: e.Namespace}},
	}
	for _, obj := range resources {
		if err := r.deleteOwned(ctx, e, obj); err != nil {
			return ctrl.Result{}, r.setStatus(ctx, e, storagev1alpha1.PhaseDegraded, "CleanupFailed", err.Error(), pendingConditions("CleanupFailed", err.Error()), nil, nil)
		}
	}
	controllerutil.RemoveFinalizer(e, storagev1alpha1.Finalizer)
	return ctrl.Result{}, r.Update(ctx, e)
}

func (r *LonghornNFSExportReconciler) deleteOwned(ctx context.Context, e *storagev1alpha1.LonghornNFSExport, obj client.Object) error {
	if err := r.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if !ownedResourceByExport(obj, e) {
		return fmt.Errorf("refusing to delete unowned %T %s/%s", obj, obj.GetNamespace(), obj.GetName())
	}
	return client.IgnoreNotFound(r.Delete(ctx, obj))
}

type conditionValue struct {
	status          metav1.ConditionStatus
	reason, message string
}
type conditionValues map[string]conditionValue

func pendingConditions(reason, message string) conditionValues {
	return conditionValues{
		storagev1alpha1.ConditionPVCBound:           conditionValue{metav1.ConditionFalse, reason, message},
		storagev1alpha1.ConditionProtocolCompatible: conditionValue{metav1.ConditionTrue, "Supported", "NFSv3 is supported"},
		storagev1alpha1.ConditionHelperReady:        conditionValue{metav1.ConditionFalse, "Waiting", message},
		storagev1alpha1.ConditionEndpointReady:      conditionValue{metav1.ConditionFalse, "Waiting", message},
		storagev1alpha1.ConditionCleanup:            conditionValue{metav1.ConditionTrue, "Active", "controller-owned resources are tracked by the finalizer"},
	}
}

func (r *LonghornNFSExportReconciler) setStatus(ctx context.Context, e *storagev1alpha1.LonghornNFSExport, phase, reason, message string, values conditionValues, endpoint *storagev1alpha1.EndpointStatus, helper *storagev1alpha1.HelperStatus) error {
	status := e.Status
	status.ObservedGeneration = e.Generation
	status.Phase, status.Reason, status.Message = phase, reason, message
	if endpoint == nil {
		status.Endpoint = nil
	} else {
		copied := *endpoint
		status.Endpoint = &copied
	}
	if helper == nil {
		status.Helper = nil
	} else {
		copied := *helper
		status.Helper = &copied
	}
	for _, typ := range []string{storagev1alpha1.ConditionPVCBound, storagev1alpha1.ConditionHelperReady, storagev1alpha1.ConditionEndpointReady, storagev1alpha1.ConditionProtocolCompatible, storagev1alpha1.ConditionCleanup} {
		value, ok := values[typ]
		if !ok {
			value = conditionValue{metav1.ConditionUnknown, "Reconciling", "controller is reconciling"}
		}
		upsertCondition(&status.Conditions, typ, value, e.Generation)
	}
	if reflect.DeepEqual(e.Status, status) {
		return nil
	}
	e.Status = status
	return r.Status().Update(ctx, e)
}

func upsertCondition(conditions *[]metav1.Condition, typ string, value conditionValue, generation int64) {
	for i := range *conditions {
		if (*conditions)[i].Type != typ {
			continue
		}
		old := (*conditions)[i]
		transition := metav1.Now()
		if old.Status == value.status && old.Reason == value.reason && old.Message == value.message {
			transition = old.LastTransitionTime
		}
		(*conditions)[i] = metav1.Condition{Type: typ, Status: value.status, ObservedGeneration: generation, LastTransitionTime: transition, Reason: value.reason, Message: value.message}
		return
	}
	*conditions = append(*conditions, metav1.Condition{Type: typ, Status: value.status, ObservedGeneration: generation, LastTransitionTime: metav1.Now(), Reason: value.reason, Message: value.message})
}

func boolCondition(value bool) metav1.ConditionStatus {
	if value {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}
func helperReason(value bool) string {
	if value {
		return "Ready"
	}
	return "WaitingForPod"
}
func helperMessage(value bool) string {
	if value {
		return "NFS helper Deployment has a Ready replica"
	}
	return "waiting for a Ready helper Pod"
}
func pointer[T any](value T) *T { return &value }
