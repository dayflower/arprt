package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LocalReference always refers to a resource in the object's namespace.
type LocalReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name"`
}

type HTTPSource struct {
	// URL is operator-controlled; credentials and redirects are not supported.
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:[0-9]{1,5})?([/?]([^%#\x00-\x20\x7F]|%[0-9A-Fa-f]{2})*)?$`
	// +kubebuilder:validation:XValidation:rule="isURL(self)",message="renderer URL must be a valid absolute HTTP URL"
	// +kubebuilder:validation:XValidation:rule="!self.matches('[?].*;')",message="query semicolons must be percent-encoded"
	URL string `json:"url"`
	// +kubebuilder:default=GET
	// +kubebuilder:validation:Enum=GET;POST
	Method string `json:"method,omitempty"`
}

// JobVolume uses Kubernetes field names and rejects unsupported standard sources.
// +kubebuilder:validation:XValidation:rule="has(self.configMap) != has(self.secret)",message="exactly one of configMap or secret is required"
// +kubebuilder:validation:XValidation:rule="!has(self.hostPath) && !has(self.emptyDir) && !has(self.gcePersistentDisk) && !has(self.awsElasticBlockStore) && !has(self.gitRepo) && !has(self.nfs) && !has(self.iscsi) && !has(self.glusterfs) && !has(self.persistentVolumeClaim) && !has(self.rbd) && !has(self.flexVolume) && !has(self.cinder) && !has(self.cephfs) && !has(self.flocker) && !has(self.downwardAPI) && !has(self.fc) && !has(self.azureFile) && !has(self.vsphereVolume) && !has(self.quobyte) && !has(self.azureDisk) && !has(self.photonPersistentDisk) && !has(self.projected) && !has(self.portworxVolume) && !has(self.scaleIO) && !has(self.storageos) && !has(self.csi) && !has(self.ephemeral) && !has(self.image)",message="only configMap and secret volume sources are supported"
// +kubebuilder:validation:XValidation:rule="self.name != 'data'",message="data is reserved for ARPRT"
// +kubebuilder:validation:XValidation:rule="!has(self.configMap) || (has(self.configMap.name) && size(self.configMap.name) > 0)",message="configMap.name is required"
// +kubebuilder:validation:XValidation:rule="!has(self.secret) || (has(self.secret.secretName) && size(self.secret.secretName) > 0)",message="secret.secretName is required"
type JobVolume struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
	// Unsupported sources remain in the schema so even non-strict writes reject them.
	HostPath              *corev1.HostPathVolumeSource              `json:"hostPath,omitempty"`
	EmptyDir              *corev1.EmptyDirVolumeSource              `json:"emptyDir,omitempty"`
	GCEPersistentDisk     *corev1.GCEPersistentDiskVolumeSource     `json:"gcePersistentDisk,omitempty"`
	AWSElasticBlockStore  *corev1.AWSElasticBlockStoreVolumeSource  `json:"awsElasticBlockStore,omitempty"`
	GitRepo               *corev1.GitRepoVolumeSource               `json:"gitRepo,omitempty"`
	Secret                *JobSecretVolumeSource                    `json:"secret,omitempty"`
	NFS                   *corev1.NFSVolumeSource                   `json:"nfs,omitempty"`
	ISCSI                 *corev1.ISCSIVolumeSource                 `json:"iscsi,omitempty"`
	Glusterfs             *corev1.GlusterfsVolumeSource             `json:"glusterfs,omitempty"`
	PersistentVolumeClaim *corev1.PersistentVolumeClaimVolumeSource `json:"persistentVolumeClaim,omitempty"`
	RBD                   *corev1.RBDVolumeSource                   `json:"rbd,omitempty"`
	FlexVolume            *corev1.FlexVolumeSource                  `json:"flexVolume,omitempty"`
	Cinder                *corev1.CinderVolumeSource                `json:"cinder,omitempty"`
	CephFS                *corev1.CephFSVolumeSource                `json:"cephfs,omitempty"`
	Flocker               *corev1.FlockerVolumeSource               `json:"flocker,omitempty"`
	DownwardAPI           *corev1.DownwardAPIVolumeSource           `json:"downwardAPI,omitempty"`
	FC                    *corev1.FCVolumeSource                    `json:"fc,omitempty"`
	AzureFile             *corev1.AzureFileVolumeSource             `json:"azureFile,omitempty"`
	ConfigMap             *JobConfigMapVolumeSource                 `json:"configMap,omitempty"`
	VsphereVolume         *corev1.VsphereVirtualDiskVolumeSource    `json:"vsphereVolume,omitempty"`
	Quobyte               *corev1.QuobyteVolumeSource               `json:"quobyte,omitempty"`
	AzureDisk             *corev1.AzureDiskVolumeSource             `json:"azureDisk,omitempty"`
	PhotonPersistentDisk  *corev1.PhotonPersistentDiskVolumeSource  `json:"photonPersistentDisk,omitempty"`
	Projected             *corev1.ProjectedVolumeSource             `json:"projected,omitempty"`
	PortworxVolume        *corev1.PortworxVolumeSource              `json:"portworxVolume,omitempty"`
	ScaleIO               *corev1.ScaleIOVolumeSource               `json:"scaleIO,omitempty"`
	StorageOS             *corev1.StorageOSVolumeSource             `json:"storageos,omitempty"`
	CSI                   *corev1.CSIVolumeSource                   `json:"csi,omitempty"`
	Ephemeral             *corev1.EphemeralVolumeSource             `json:"ephemeral,omitempty"`
	Image                 *corev1.ImageVolumeSource                 `json:"image,omitempty"`
}

// JobVolumeMount retains the standard mount shape with a read-only subset.
// +kubebuilder:validation:XValidation:rule="has(self.readOnly) && self.readOnly",message="input mounts require readOnly true"
// +kubebuilder:validation:XValidation:rule="!has(self.subPath) && !has(self.subPathExpr) && !has(self.mountPropagation) && !has(self.recursiveReadOnly)",message="subPath, subPathExpr, mountPropagation and recursiveReadOnly are unsupported"
type JobVolumeMount struct {
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^/([^/:\x00]+/)*[^/:\x00]+$`
	// +kubebuilder:validation:XValidation:rule="!self.split('/').exists(p, p == '.' || p == '..')",message="mountPath must be a clean absolute POSIX path"
	MountPath         string                        `json:"mountPath"`
	ReadOnly          bool                          `json:"readOnly"`
	SubPath           *string                       `json:"subPath,omitempty"`
	SubPathExpr       *string                       `json:"subPathExpr,omitempty"`
	MountPropagation  *corev1.MountPropagationMode  `json:"mountPropagation,omitempty"`
	RecursiveReadOnly *corev1.RecursiveReadOnlyMode `json:"recursiveReadOnly,omitempty"`
}

// JobKeyToPath uses the Kubernetes key-to-file mapping and mode semantics.
type JobKeyToPath struct {
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z0-9]+$`
	Key string `json:"key"`
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[^\x00]+$`
	// +kubebuilder:validation:XValidation:rule="!self.startsWith('/') && !self.matches('(^|/)[.][.](/|$)') && self != '.' && !self.startsWith('..')",message="path must be relative and must not escape or use a reserved dot prefix"
	Path string `json:"path"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=511
	Mode *int32 `json:"mode,omitempty"`
}

// JobConfigMapVolumeSource follows Kubernetes ConfigMap volume semantics.
type JobConfigMapVolumeSource struct {
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name"`
	// +kubebuilder:validation:MaxItems=128
	Items []JobKeyToPath `json:"items,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=511
	DefaultMode *int32 `json:"defaultMode,omitempty"`
	Optional    *bool  `json:"optional,omitempty"`
}

// JobSecretVolumeSource follows Kubernetes Secret volume semantics.
type JobSecretVolumeSource struct {
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	SecretName string `json:"secretName"`
	// +kubebuilder:validation:MaxItems=128
	Items []JobKeyToPath `json:"items,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=511
	DefaultMode *int32 `json:"defaultMode,omitempty"`
	Optional    *bool  `json:"optional,omitempty"`
}

// RendererContainer deliberately exposes only the reusable container interface.
type RendererContainer struct {
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	VolumeMounts []JobVolumeMount `json:"volumeMounts,omitempty"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^\S+$`
	Image string `json:"image"`
	// +kubebuilder:validation:MaxItems=128
	Command []string `json:"command,omitempty"`
	// +kubebuilder:validation:MaxItems=128
	Args []string `json:"args,omitempty"`
	// +kubebuilder:validation:MaxItems=128
	Env []corev1.EnvVar `json:"env,omitempty"`
	// +kubebuilder:validation:MaxItems=128
	EnvFrom   []corev1.EnvFromSource      `json:"envFrom,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// JobState opts a Job into renderer-owned JSON state.
type JobState struct {
	// ResultPath defaults internally to result.json beside imagePath.
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^/([^/\x00]+/)*[^/\x00]+$`
	// +kubebuilder:validation:XValidation:rule="!self.split('/').exists(p, p == '.' || p == '..')",message="path must be a clean absolute POSIX file path"
	ResultPath string `json:"resultPath,omitempty"`
	// ResetToken changes invalidate previously generated state and candidates.
	// +kubebuilder:validation:MaxLength=256
	ResetToken string `json:"resetToken,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.renderer.volumeMounts) || self.renderer.volumeMounts.all(m, has(self.volumes) && self.volumes.exists(v, v.name == m.name))",message="mounts must reference a defined input volume"
// +kubebuilder:validation:XValidation:rule="!has(self.renderer.volumeMounts) || self.renderer.volumeMounts.all(m, self.renderer.volumeMounts.all(n, m.mountPath == n.mountPath || !(m.mountPath.startsWith(n.mountPath + '/') || n.mountPath.startsWith(m.mountPath + '/'))))",message="input mounts must not overlap"
// +kubebuilder:validation:XValidation:rule="!has(self.renderer.volumeMounts) || self.renderer.volumeMounts.all(m, m.mountPath != self.contextPath && !m.mountPath.startsWith(self.contextPath + '/') && !self.contextPath.startsWith(m.mountPath + '/') && m.mountPath != self.imagePath.substring(0, self.imagePath.lastIndexOf('/')) && !m.mountPath.startsWith(self.imagePath.substring(0, self.imagePath.lastIndexOf('/')) + '/') && !self.imagePath.substring(0, self.imagePath.lastIndexOf('/')).startsWith(m.mountPath + '/'))",message="input mounts must not overlap context or output mounts"
// +kubebuilder:validation:XValidation:rule="!has(self.renderer.volumeMounts) || self.renderer.volumeMounts.all(m, self.renderer.volumeMounts.filter(n, n.mountPath == m.mountPath).size() == 1)",message="mountPath must be unique"
// JobSource paths designate files. Output's parent is a dedicated writable mount.
// +kubebuilder:validation:XValidation:rule="self.contextPath != self.imagePath && !self.contextPath.startsWith(self.imagePath + '/') && !self.imagePath.startsWith(self.contextPath + '/')",message="context and image must be distinct files and neither may be an ancestor of the other"
// +kubebuilder:validation:XValidation:rule="!self.imagePath.matches('^/[^/]+$')",message="output parent must not be the filesystem root"
// +kubebuilder:validation:XValidation:rule="!self.imagePath.matches('^/(proc|sys|dev|etc|bin|sbin|usr)(/|$).*')",message="output mount must not hide a system directory"
// +kubebuilder:validation:XValidation:rule="!self.contextPath.matches('^/var/lib/arprt(/|$).*') && !self.imagePath.matches('^/var/lib/arprt(/|$).*') && !self.imagePath.matches('^/var/[^/]+$') && !self.imagePath.matches('^/var/lib/[^/]+$')",message="paths must not overlap the reserved artifact root /var/lib/arprt"
// +kubebuilder:validation:XValidation:rule="!self.imagePath.matches('^/(var/run|run)/secrets(/|$).*') && !self.imagePath.matches('^/(var/run|run)/[^/]+$')",message="output mount must not hide the ServiceAccount secret tree"
// +kubebuilder:validation:XValidation:rule="!has(self.timeout) || (has(self.executionMode) && self.executionMode == 'OnDemand')",message="timeout is only supported for OnDemand Jobs"
// +kubebuilder:validation:XValidation:rule="!has(self.state) || !has(self.state.resultPath) || self.state.resultPath.substring(0, self.state.resultPath.lastIndexOf('/')) == self.imagePath.substring(0, self.imagePath.lastIndexOf('/'))",message="result and image must have the same parent directory"
// +kubebuilder:validation:XValidation:rule="!has(self.state) || !has(self.state.resultPath) || (self.state.resultPath != self.imagePath && self.state.resultPath != self.contextPath && !self.contextPath.startsWith(self.state.resultPath + '/') && !self.state.resultPath.startsWith(self.contextPath + '/'))",message="result must be distinct from context and image files"
// +kubebuilder:validation:XValidation:rule="!has(self.state) || has(self.state.resultPath) || (!self.imagePath.endsWith('/result.json') && self.contextPath != self.imagePath.substring(0, self.imagePath.lastIndexOf('/')) + '/result.json' && !self.contextPath.startsWith(self.imagePath.substring(0, self.imagePath.lastIndexOf('/')) + '/result.json/'))",message="default result path must be distinct from context and image files"
// +kubebuilder:validation:XValidation:rule="!has(self.state) || has(self.state.resultPath) || size(self.imagePath.substring(0, self.imagePath.lastIndexOf('/')) + '/result.json') <= 512",message="default result path exceeds the path length limit"
type JobSource struct {
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	Volumes []JobVolume `json:"volumes,omitempty"`
	State   *JobState   `json:"state,omitempty"`
	// ExecutionMode omitted selects Scheduled. Defaults are normalized internally.
	// +kubebuilder:validation:Enum=Scheduled;OnDemand
	ExecutionMode string `json:"executionMode,omitempty"`
	// Timeout includes slot waiting, startup, collection and base-image storage.
	// Omission selects 5s for OnDemand; Scheduled has no request timeout.
	// +kubebuilder:validation:Pattern=`^([1-9]|10)s$`
	// +kubebuilder:validation:MaxLength=3
	Timeout  string            `json:"timeout,omitempty"`
	Renderer RendererContainer `json:"renderer"`
	// +kubebuilder:default="/arprt/context.json"
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^/([^/\x00]+/)*[^/\x00]+$`
	// +kubebuilder:validation:XValidation:rule="!self.split('/').exists(p, p == '.' || p == '..')",message="path must be a clean absolute POSIX file path"
	ContextPath string `json:"contextPath,omitempty"`
	// +kubebuilder:default="/arprt/output.png"
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^/([^/\x00]+/)*[^/\x00]+$`
	// +kubebuilder:validation:XValidation:rule="!self.split('/').exists(p, p == '.' || p == '..')",message="path must be a clean absolute POSIX file path"
	ImagePath string `json:"imagePath,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.http) != has(self.job)",message="exactly one of http or job is required"
type ScreenSource struct {
	HTTP *HTTPSource `json:"http,omitempty"`
	Job  *JobSource  `json:"job,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!has(self.refreshInterval) || !has(self.source.http) || self.source.http.method != 'POST'",message="refreshInterval must be omitted for HTTP POST"
// +kubebuilder:validation:XValidation:rule="!has(self.refreshInterval) || !has(self.source.job) || !has(self.source.job.executionMode) || self.source.job.executionMode != 'OnDemand'",message="refreshInterval must be omitted for OnDemand Jobs"
type ArprtScreenSpec struct {
	// ImageProcessing controls normalization of renderer PNG inputs.
	ImageProcessing *ImageProcessing `json:"imageProcessing,omitempty"`
	// Overlay controls optional controller bitmap drawing on new deliveries.
	Overlay *ScreenOverlay `json:"overlay,omitempty"`
	Source  ScreenSource   `json:"source"`
	// RefreshInterval controls artifact freshness, not device polling. Effective
	// defaults are 5m for GET and 30m for Scheduled Job; POST/OnDemand have no interval.
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^([0-9]+[hms])+$`
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('1s') && duration(self) <= duration('24h')",message="interval must be between 1s and 24h in whole seconds"
	RefreshInterval string `json:"refreshInterval,omitempty"`
}

// ImageProcessing describes resizing and palette quantization intent.
type ImageProcessing struct {
	// +kubebuilder:default=contain
	// +kubebuilder:validation:Enum=contain;stretch
	ResizeMode string `json:"resizeMode,omitempty"`
	// +kubebuilder:default=atkinson
	// +kubebuilder:validation:Enum=atkinson;floyd-steinberg;bayer;none
	Dithering string `json:"dithering,omitempty"`
}

// OverlayItem selects a display item; layout order is independent of array order.
// +kubebuilder:validation:Enum=timestamp;battery;wifi
type OverlayItem string

// OverlayMargin independently insets the padded content row from each logical viewing edge.
// Values are unscaled bitmap pixels; omitted components mean zero.
type OverlayMargin struct {
	// Top is the inset when a top corner is selected.
	// +kubebuilder:validation:Minimum=0
	Top int32 `json:"top,omitempty"`
	// Bottom is the inset when a bottom corner is selected.
	// +kubebuilder:validation:Minimum=0
	Bottom int32 `json:"bottom,omitempty"`
	// Left is the inset when a left corner is selected.
	// +kubebuilder:validation:Minimum=0
	Left int32 `json:"left,omitempty"`
	// Right is the inset when a right corner is selected.
	// +kubebuilder:validation:Minimum=0
	Right int32 `json:"right,omitempty"`
}

// ScreenOverlay describes display intent, independently of renderer execution.
// +kubebuilder:validation:XValidation:rule="!self.cycleCorners || !has(self.position)",message="position must be omitted when cycleCorners is true"
type ScreenOverlay struct {
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`
	// Items omitted selects all items; an explicit empty array draws nothing.
	// A pointer preserves empty arrays through JSON and Kubernetes round trips.
	// +kubebuilder:validation:MaxItems=3
	// +listType=set
	Items *[]OverlayItem `json:"items,omitempty"`
	// Position is defaulted to top-right internally, never by admission.
	// +kubebuilder:validation:Enum=top-right;bottom-right;bottom-left;top-left
	Position string `json:"position,omitempty"`
	// +kubebuilder:default=false
	CycleCorners bool `json:"cycleCorners,omitempty"`
	// Scale omitted selects automatic scaling using physical dimensions.
	// +kubebuilder:validation:Minimum=1
	Scale *int32 `json:"scale,omitempty"`
	// Margin insets the padded content row from the two edges adjoining the selected corner.
	// The opaque background extends through each inset to the display edges.
	Margin *OverlayMargin `json:"margin,omitempty"`
}

type ArprtScreenStatus struct {
	// +kubebuilder:validation:Minimum=0
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=ascreen
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type ArprtScreen struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ArprtScreenSpec   `json:"spec"`
	Status            ArprtScreenStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ArprtScreenList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ArprtScreen `json:"items"`
}

// ArprtPlaylistSpec defines a bounded, explicitly ordered screen cycle.
// +kubebuilder:validation:XValidation:rule="!has(self.renderTimeout) || (has(self.failurePolicy) && self.failurePolicy == 'Skip')",message="renderTimeout is only supported for Skip playlists"
type ArprtPlaylistSpec struct {
	// FailurePolicy omitted selects Hold, preserving stale fallback and turn holding.
	// +kubebuilder:validation:Enum=Hold;Skip
	FailurePolicy string `json:"failurePolicy,omitempty"`
	// RenderTimeout bounds all candidates from request admission. Skip defaults to 10s.
	// +kubebuilder:validation:Pattern=`^([1-9]|10)s$`
	// +kubebuilder:validation:MaxLength=3
	RenderTimeout string `json:"renderTimeout,omitempty"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	Screens []LocalReference `json:"screens"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=aplaylist
type ArprtPlaylist struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ArprtPlaylistSpec `json:"spec"`
	Status            ArprtScreenStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ArprtPlaylistList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ArprtPlaylist `json:"items"`
}

// ScreenCycleStatus persists the last successful screen-cycle display response.
type ScreenCycleStatus struct {
	// EntryIndex is the original zero-based assignment position.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=63
	EntryIndex int32 `json:"entryIndex"`
	// CompatibilityFingerprint binds replay to output constraints, rotation and
	// processing semantics. Omission denotes a legacy turn, never current replay.
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	CompatibilityFingerprint string            `json:"compatibilityFingerprint,omitempty"`
	Assignment               string            `json:"assignment"`
	ScreenUID                string            `json:"screenUID"`
	InputRevision            string            `json:"inputRevision"`
	Target                   CycleTargetStatus `json:"target"`
	ImageDigest              string            `json:"imageDigest"`
	Filename                 string            `json:"filename"`
	ServedAt                 metav1.MicroTime  `json:"servedAt"`
}

// HardwareIDSecretReference selects a required key in a same-namespace Secret.
type HardwareIDSecretReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z0-9]+$`
	Key string `json:"key"`
}

// DisplayOverride supplies optional operator-provided output capabilities.
// Dimensions are a complete pair and bounded before any image allocation.
// +kubebuilder:validation:XValidation:rule="has(self.width) == has(self.height)",message="width and height must be specified together"
// +kubebuilder:validation:XValidation:rule="!has(self.width) || !has(self.height) || self.width * self.height <= 4194304",message="display exceeds the output pixel limit"
type DisplayOverride struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4096
	Width *int32 `json:"width,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4096
	Height *int32 `json:"height,omitempty"`
	// +kubebuilder:validation:Enum=gray4;gray16;spectra6
	Palette string `json:"palette,omitempty"`
	// PNGByteLimit is exclusive: the complete encoded PNG must be smaller.
	// +kubebuilder:validation:Minimum=1
	PNGByteLimit *int32 `json:"pngByteLimit,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="!(has(self.screenRef) && has(self.playlistRef))",message="at most one of screenRef and playlistRef is permitted"
// +kubebuilder:validation:XValidation:rule="!(has(self.hardwareID) && has(self.hardwareIDSecretRef))",message="at most one hardware identity source is permitted"
// +kubebuilder:validation:XValidation:rule="has(self.hardwareID) == has(oldSelf.hardwareID) && (!has(self.hardwareID) || self.hardwareID == oldSelf.hardwareID)",message="hardwareID source is immutable; replace the device to change identity"
// +kubebuilder:validation:XValidation:rule="has(self.hardwareIDSecretRef) == has(oldSelf.hardwareIDSecretRef) && (!has(self.hardwareIDSecretRef) || self.hardwareIDSecretRef == oldSelf.hardwareIDSecretRef)",message="hardwareIDSecretRef source is immutable; replace the device to change identity"
type ArprtDeviceSpec struct {
	Display *DisplayOverride `json:"display,omitempty"`
	// Rotation requests clockwise content rotation in degrees. Renderers apply it
	// while preserving the physical display dimensions of the output PNG.
	// +kubebuilder:default=0
	// +kubebuilder:validation:Enum=0;90;180;270
	Rotation int32 `json:"rotation,omitempty"`
	// HardwareID optionally restricts registration to this canonical uppercase MAC.
	// +kubebuilder:validation:MaxLength=17
	// +kubebuilder:validation:Pattern=`^([0-9A-F]{2}:){5}[0-9A-F]{2}$`
	HardwareID string `json:"hardwareID,omitempty"`
	// HardwareIDSecretRef optionally restricts registration using a Secret value.
	HardwareIDSecretRef *HardwareIDSecretReference `json:"hardwareIDSecretRef,omitempty"`
	AuthSecretRef       LocalReference             `json:"authSecretRef"`
	ScreenRef           *LocalReference            `json:"screenRef,omitempty"`
	// PlaylistRef selects an ordered same-namespace screen cycle.
	PlaylistRef *LocalReference `json:"playlistRef,omitempty"`
}

// CycleTargetStatus preserves the compact persisted turn shape, independently
// of effective display provenance and output compatibility fingerprints.
type CycleTargetStatus struct {
	// +kubebuilder:validation:Minimum=1
	Width int32 `json:"width"`
	// +kubebuilder:validation:Minimum=1
	Height int32 `json:"height"`
	// +kubebuilder:validation:Enum=bw;gray4;gray16;bwry;spectra6
	Palette string `json:"palette"`
}

type DisplaySources struct {
	// +kubebuilder:validation:Enum=explicit;reported;profile
	Dimensions string `json:"dimensions"`
	// +kubebuilder:validation:Enum=explicit;profile
	Palette string `json:"palette"`
	// +kubebuilder:validation:Enum=explicit;profile
	PNGByteLimit string `json:"pngByteLimit"`
}

type DisplayStatus struct {
	// +kubebuilder:validation:Minimum=1
	Width int32 `json:"width"`
	// +kubebuilder:validation:Minimum=1
	Height int32 `json:"height"`
	// +kubebuilder:validation:Enum=bw;gray4;gray16;bwry;spectra6
	Palette string `json:"palette"`
	// Missing fields are readable legacy status, not capability observations.
	// +kubebuilder:validation:Minimum=1
	PNGByteLimit int32           `json:"pngByteLimit,omitempty"`
	Sources      *DisplaySources `json:"sources,omitempty"`
}

// ReportedDisplayStatus stores individually valid parsed observations, even
// when the pair is incomplete or exceeds safe output allocation bounds.
type ReportedDisplayStatus struct {
	// +kubebuilder:validation:Minimum=1
	Width *int32 `json:"width,omitempty"`
	// +kubebuilder:validation:Minimum=1
	Height *int32 `json:"height,omitempty"`
}

type DeviceInfoStatus struct {
	ReportedDisplay *ReportedDisplayStatus `json:"reportedDisplay,omitempty"`
	Model           string                 `json:"model,omitempty"`
	FirmwareVersion string                 `json:"firmwareVersion,omitempty"`
}

type BatteryStatus struct {
	// +kubebuilder:validation:Minimum=0
	Voltage *float64 `json:"voltage,omitempty"`
	// Percent is present only when measured, never inferred from OG voltage.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Percent *float64 `json:"percent,omitempty"`
}

type NetworkStatus struct {
	RSSI *int32 `json:"rssi,omitempty"`
}

type ArprtDeviceStatus struct {
	ScreenCycle *ScreenCycleStatus `json:"screenCycle,omitempty"`
	// HardwareID mirrors the durable registration for this device UID.
	// +kubebuilder:validation:MaxLength=17
	// +kubebuilder:validation:Pattern=`^([0-9A-F]{2}:){5}[0-9A-F]{2}$`
	HardwareID string `json:"hardwareID,omitempty"`
	// +kubebuilder:validation:Minimum=0
	ObservedGeneration int64             `json:"observedGeneration,omitempty"`
	Display            *DisplayStatus    `json:"display,omitempty"`
	Device             *DeviceInfoStatus `json:"device,omitempty"`
	Battery            *BatteryStatus    `json:"battery,omitempty"`
	Network            *NetworkStatus    `json:"network,omitempty"`
	LastSeenAt         *metav1.Time      `json:"lastSeenAt,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=adevice
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
type ArprtDevice struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ArprtDeviceSpec   `json:"spec"`
	Status            ArprtDeviceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ArprtDeviceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ArprtDevice `json:"items"`
}
