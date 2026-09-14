package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	ModeRWO = "RWO"
	ModeRWX = "RWX"

	ProtocolNFSv3 = "NFSv3"

	PhasePending  = "Pending"
	PhaseReady    = "Ready"
	PhaseDegraded = "Degraded"
	PhaseError    = "Error"

	ConditionPVCBound           = "PVCBound"
	ConditionHelperReady        = "HelperReady"
	ConditionEndpointReady      = "EndpointReady"
	ConditionProtocolCompatible = "ProtocolCompatible"
	ConditionCleanup            = "Cleanup"

	AnnotationNFSServer     = "storage.k8s-darwin.dev/nfs-server"
	AnnotationNFSExport     = "storage.k8s-darwin.dev/nfs-export"
	AnnotationNFSVersion    = "storage.k8s-darwin.dev/nfs-version"
	AnnotationNFSMountPort  = "storage.k8s-darwin.dev/nfs-mount-port"
	AnnotationNFSGeneration = "storage.k8s-darwin.dev/nfs-generation"

	Finalizer = "storage.k8s-darwin.dev/longhorn-nfs-export"
)

// LonghornNFSExportSpec describes one explicit PVC-to-NFS gateway.
type LonghornNFSExportSpec struct {
	PVCRef        PVCReference      `json:"pvcRef"`
	Mode          string            `json:"mode"`
	Protocol      string            `json:"protocol"`
	MountOptions  []string          `json:"mountOptions,omitempty"`
	Helper        HelperSpec        `json:"helper,omitempty"`
	Service       ServiceSpec       `json:"service,omitempty"`
	NetworkPolicy NetworkPolicySpec `json:"networkPolicy,omitempty"`
	Handoff       HandoffSpec       `json:"handoff,omitempty"`
}

type PVCReference struct {
	Name string `json:"name"`
}

type HelperSpec struct {
	Image              string            `json:"image,omitempty"`
	NFSGaneshaImage    string            `json:"nfsGaneshaImage,omitempty"`
	ProxyImage         string            `json:"proxyImage,omitempty"`
	NodeSelector       map[string]string `json:"nodeSelector,omitempty"`
	ServiceAccountName string            `json:"serviceAccountName,omitempty"`
}

type ServiceSpec struct {
	Name            string `json:"name,omitempty"`
	Type            string `json:"type,omitempty"`
	ExternalAddress string `json:"externalAddress,omitempty"`
	MountPort       int32  `json:"mountPort,omitempty"`
	NFSPort         int32  `json:"nfsPort,omitempty"`
	MountNodePort   int32  `json:"mountNodePort,omitempty"`
	NFSNodePort     int32  `json:"nfsNodePort,omitempty"`
}

type NetworkPolicySpec struct {
	Enabled      *bool    `json:"enabled,omitempty"`
	AllowedCIDRs []string `json:"allowedCIDRs,omitempty"`
}

type HandoffSpec struct {
	Enabled           *bool             `json:"enabled,omitempty"`
	NativePodSelector map[string]string `json:"nativePodSelector,omitempty"`
}

type LonghornNFSExportStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Phase              string             `json:"phase,omitempty"`
	Reason             string             `json:"reason,omitempty"`
	Message            string             `json:"message,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	Endpoint           *EndpointStatus    `json:"endpoint,omitempty"`
	Helper             *HelperStatus      `json:"helper,omitempty"`
}

type EndpointStatus struct {
	Server        string `json:"server,omitempty"`
	Export        string `json:"export,omitempty"`
	Version       int32  `json:"version,omitempty"`
	MountPort     int32  `json:"mountPort,omitempty"`
	NFSPort       int32  `json:"nfsPort,omitempty"`
	MountNodePort int32  `json:"mountNodePort,omitempty"`
	NFSNodePort   int32  `json:"nfsNodePort,omitempty"`
	ServiceType   string `json:"serviceType,omitempty"`
	Generation    string `json:"generation,omitempty"`
}

type HelperStatus struct {
	Name           string `json:"name,omitempty"`
	ServiceName    string `json:"serviceName,omitempty"`
	Ready          bool   `json:"ready,omitempty"`
	ObservedPVCUID string `json:"observedPVCUID,omitempty"`
	ObservedPVName string `json:"observedPVName,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=lhnfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="PVC",type="string",JSONPath=".spec.pvcRef.name"
// +kubebuilder:printcolumn:name="Mode",type="string",JSONPath=".spec.mode"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Endpoint",type="string",JSONPath=".status.endpoint.server"
type LonghornNFSExport struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              LonghornNFSExportSpec   `json:"spec"`
	Status            LonghornNFSExportStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type LonghornNFSExportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LonghornNFSExport `json:"items"`
}
