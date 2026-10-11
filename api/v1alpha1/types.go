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

// RendererContainer deliberately exposes only the reusable container interface.
type RendererContainer struct {
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

// JobSource paths designate files. Output's parent is a dedicated writable mount.
// +kubebuilder:validation:XValidation:rule="self.contextPath != self.imagePath && !self.contextPath.startsWith(self.imagePath + '/') && !self.imagePath.startsWith(self.contextPath + '/')",message="context and image must be distinct files and neither may be an ancestor of the other"
// +kubebuilder:validation:XValidation:rule="!self.imagePath.matches('^/[^/]+$')",message="output parent must not be the filesystem root"
// +kubebuilder:validation:XValidation:rule="!self.imagePath.matches('^/(proc|sys|dev|etc|bin|sbin|usr)(/|$).*')",message="output mount must not hide a system directory"
// +kubebuilder:validation:XValidation:rule="!self.contextPath.matches('^/var/lib/arprt(/|$).*') && !self.imagePath.matches('^/var/lib/arprt(/|$).*') && !self.imagePath.matches('^/var/[^/]+$') && !self.imagePath.matches('^/var/lib/[^/]+$')",message="paths must not overlap the reserved artifact root /var/lib/arprt"
// +kubebuilder:validation:XValidation:rule="!self.imagePath.matches('^/(var/run|run)/secrets(/|$).*') && !self.imagePath.matches('^/(var/run|run)/[^/]+$')",message="output mount must not hide the ServiceAccount secret tree"
type JobSource struct {
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
type ArprtScreenSpec struct {
	// Overlay controls optional controller bitmap drawing on new deliveries.
	Overlay *ScreenOverlay `json:"overlay,omitempty"`
	Source  ScreenSource   `json:"source"`
	// RefreshInterval controls artifact freshness, not device polling. Effective
	// defaults are 5m for GET and 30m for Job; POST has no interval.
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^([0-9]+[hms])+$`
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('1s') && duration(self) <= duration('24h')",message="interval must be between 1s and 24h in whole seconds"
	RefreshInterval string `json:"refreshInterval,omitempty"`
}

// OverlayItem selects a display item; layout order is independent of array order.
// +kubebuilder:validation:Enum=timestamp;battery;wifi
type OverlayItem string

// OverlayMargin independently insets foreground along the logical viewing axes.
// Values are unscaled bitmap pixels; omitted components mean zero.
type OverlayMargin struct {
	// Horizontal is the inset from the selected left or right edge.
	// +kubebuilder:validation:Minimum=0
	Horizontal int32 `json:"horizontal,omitempty"`
	// Vertical is the inset from the selected top or bottom edge.
	// +kubebuilder:validation:Minimum=0
	Vertical int32 `json:"vertical,omitempty"`
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
	// Margin insets foreground independently from the horizontal and vertical edges.
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
type ArprtPlaylistSpec struct {
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	// +kubebuilder:validation:XValidation:rule="self.all(s, self.filter(other, other.name == s.name).size() == 1)",message="playlist screen names must be distinct"
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
	Assignment    string           `json:"assignment"`
	ScreenUID     string           `json:"screenUID"`
	InputRevision string           `json:"inputRevision"`
	Target        DisplayStatus    `json:"target"`
	ImageDigest   string           `json:"imageDigest"`
	Filename      string           `json:"filename"`
	ServedAt      metav1.MicroTime `json:"servedAt"`
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

// +kubebuilder:validation:XValidation:rule="!(has(self.screenRef) && has(self.playlistRef))",message="at most one of screenRef and playlistRef is permitted"
// +kubebuilder:validation:XValidation:rule="!(has(self.hardwareID) && has(self.hardwareIDSecretRef))",message="at most one hardware identity source is permitted"
// +kubebuilder:validation:XValidation:rule="has(self.hardwareID) == has(oldSelf.hardwareID) && (!has(self.hardwareID) || self.hardwareID == oldSelf.hardwareID)",message="hardwareID source is immutable; replace the device to change identity"
// +kubebuilder:validation:XValidation:rule="has(self.hardwareIDSecretRef) == has(oldSelf.hardwareIDSecretRef) && (!has(self.hardwareIDSecretRef) || self.hardwareIDSecretRef == oldSelf.hardwareIDSecretRef)",message="hardwareIDSecretRef source is immutable; replace the device to change identity"
type ArprtDeviceSpec struct {
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

type DisplayStatus struct {
	// +kubebuilder:validation:Minimum=1
	Width int32 `json:"width"`
	// +kubebuilder:validation:Minimum=1
	Height int32 `json:"height"`
	// +kubebuilder:validation:Enum=bw;gray4;gray16;bwry
	Palette string `json:"palette"`
}

type DeviceInfoStatus struct {
	Model           string `json:"model,omitempty"`
	FirmwareVersion string `json:"firmwareVersion,omitempty"`
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

func init() {
	SchemeBuilder.Register(&ArprtScreen{}, &ArprtScreenList{}, &ArprtDevice{}, &ArprtDeviceList{}, &ArprtPlaylist{}, &ArprtPlaylistList{})
}
