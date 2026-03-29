package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AvailabilityMode defines how data is synchronized between replicas.
// +kubebuilder:validation:Enum=SynchronousCommit;AsynchronousCommit
type AvailabilityMode string

const (
	AvailabilityModeSynchronous  AvailabilityMode = "SynchronousCommit"
	AvailabilityModeAsynchronous AvailabilityMode = "AsynchronousCommit"
)

// FailoverMode defines whether failover is automatic or manual.
// +kubebuilder:validation:Enum=Automatic;Manual
type FailoverMode string

const (
	FailoverModeAutomatic FailoverMode = "Automatic"
	FailoverModeManual    FailoverMode = "Manual"
)

// SeedingMode defines how initial data is populated on secondaries.
// +kubebuilder:validation:Enum=Automatic;Manual
type SeedingMode string

const (
	SeedingModeAutomatic SeedingMode = "Automatic"
	SeedingModeManual    SeedingMode = "Manual"
)

// SecondaryRole defines read access on secondary replicas.
// +kubebuilder:validation:Enum=AllowAllConnections;AllowReadIntentOnly;No
type SecondaryRole string

const (
	SecondaryRoleAllowAll      SecondaryRole = "AllowAllConnections"
	SecondaryRoleReadIntentOnly SecondaryRole = "AllowReadIntentOnly"
	SecondaryRoleNo            SecondaryRole = "No"
)

// AGReplicaSpec defines a replica participating in the Availability Group.
type AGReplicaSpec struct {
	// ServerName is the SQL Server instance name (e.g. "sql-0" or "sql-0.sql-headless.ns.svc").
	// +kubebuilder:validation:MinLength=1
	ServerName string `json:"serverName"`

	// EndpointURL is the database mirroring endpoint (e.g. "TCP://sql-0.sql-headless:5022").
	// +kubebuilder:validation:MinLength=1
	EndpointURL string `json:"endpointURL"`

	// AvailabilityMode is the synchronization mode: SynchronousCommit or AsynchronousCommit.
	// +kubebuilder:default=SynchronousCommit
	AvailabilityMode AvailabilityMode `json:"availabilityMode,omitempty"`

	// FailoverMode is Automatic or Manual. Automatic requires SynchronousCommit.
	// +kubebuilder:default=Automatic
	FailoverMode FailoverMode `json:"failoverMode,omitempty"`

	// SeedingMode determines how data is initially populated: Automatic or Manual.
	// +kubebuilder:default=Automatic
	SeedingMode SeedingMode `json:"seedingMode,omitempty"`

	// SecondaryRole defines read access on this replica when it is secondary.
	// +optional
	// +kubebuilder:default=No
	SecondaryRole SecondaryRole `json:"secondaryRole,omitempty"`

	// Server connection details for this specific replica.
	// The operator connects to each replica to execute JOIN commands on secondaries.
	Server ServerReference `json:"server"`
}

// AGListenerSpec defines the Availability Group listener configuration.
type AGListenerSpec struct {
	// Name is the listener DNS name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Port is the TCP port the listener listens on.
	// +optional
	// +kubebuilder:default=1433
	Port *int32 `json:"port,omitempty"`

	// IP addresses for the listener. On Kubernetes, this is typically managed by a Service.
	// +optional
	IPAddresses []AGListenerIP `json:"ipAddresses,omitempty"`
}

// AGListenerIP defines a listener IP address with subnet mask.
type AGListenerIP struct {
	// IP is the listener IP address.
	IP string `json:"ip"`

	// SubnetMask is the subnet mask (e.g. "255.255.255.0").
	SubnetMask string `json:"subnetMask"`
}

// AGDatabaseSpec defines a database to include in the Availability Group.
type AGDatabaseSpec struct {
	// Name is the database name to add to the AG.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AvailabilityGroupSpec defines the desired state of an Always On Availability Group.
type AvailabilityGroupSpec struct {
	// AGName is the name of the Availability Group on SQL Server.
	// +kubebuilder:validation:MinLength=1
	AGName string `json:"agName"`

	// Replicas defines the SQL Server instances participating in the AG.
	// The first replica is assumed to be the initial primary.
	// +kubebuilder:validation:MinItems=2
	Replicas []AGReplicaSpec `json:"replicas"`

	// Databases to include in the Availability Group.
	// +optional
	Databases []AGDatabaseSpec `json:"databases,omitempty"`

	// Listener configures the AG listener (virtual DNS endpoint).
	// +optional
	Listener *AGListenerSpec `json:"listener,omitempty"`

	// AutomatedBackupPreference determines where automated backups run.
	// +optional
	// +kubebuilder:validation:Enum=Primary;SecondaryOnly;Secondary;None
	// +kubebuilder:default=Secondary
	AutomatedBackupPreference *string `json:"automatedBackupPreference,omitempty"`

	// DBFailover enables database-level health detection for automatic failover.
	// +optional
	// +kubebuilder:default=true
	DBFailover *bool `json:"dbFailover,omitempty"`

	// ClusterType specifies the cluster manager: WSFC, External (Pacemaker), or None.
	// None allows AG creation without a cluster manager (manual failover only).
	// +optional
	// +kubebuilder:validation:Enum=WSFC;External;None
	// +kubebuilder:default=External
	ClusterType *string `json:"clusterType,omitempty"`

	// AutoFailover enables automatic failover when the primary becomes unreachable.
	// The operator monitors all replicas and triggers failover to the best secondary.
	// +optional
	// +kubebuilder:default=false
	AutoFailover *bool `json:"autoFailover,omitempty"`

	// HealthCheckInterval is how often to check replica health when autoFailover is enabled.
	// +optional
	// +kubebuilder:default="10s"
	HealthCheckInterval *string `json:"healthCheckInterval,omitempty"`

	// FailoverCooldown is the minimum time between automatic failovers to prevent flapping.
	// +optional
	// +kubebuilder:default="60s"
	FailoverCooldown *string `json:"failoverCooldown,omitempty"`
}

// AGReplicaStatus represents the observed state of a replica.
type AGReplicaStatus struct {
	// ServerName identifies the replica.
	ServerName string `json:"serverName"`

	// Role is the current role: PRIMARY, SECONDARY, or RESOLVING.
	Role string `json:"role,omitempty"`

	// SynchronizationState is SYNCHRONIZED, SYNCHRONIZING, or NOT_SYNCHRONIZING.
	SynchronizationState string `json:"synchronizationState,omitempty"`

	// Connected indicates whether the replica is connected to the AG.
	Connected bool `json:"connected"`
}

// AGDatabaseStatus represents the observed state of a database in the AG.
type AGDatabaseStatus struct {
	// Name is the database name.
	Name string `json:"name"`

	// SynchronizationState is SYNCHRONIZED, SYNCHRONIZING, or NOT_SYNCHRONIZING.
	SynchronizationState string `json:"synchronizationState,omitempty"`

	// Joined indicates whether the database has joined the AG on all replicas.
	Joined bool `json:"joined"`
}

// AvailabilityGroupStatus defines the observed state of an Availability Group.
type AvailabilityGroupStatus struct {
	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// PrimaryReplica is the server name of the current primary.
	// +optional
	PrimaryReplica string `json:"primaryReplica,omitempty"`

	// Replicas shows the observed state of each replica.
	// +optional
	Replicas []AGReplicaStatus `json:"replicas,omitempty"`

	// Databases shows the observed state of each database in the AG.
	// +optional
	Databases []AGDatabaseStatus `json:"databases,omitempty"`

	// LastAutoFailoverTime records when the last automatic failover was triggered.
	// +optional
	LastAutoFailoverTime *metav1.Time `json:"lastAutoFailoverTime,omitempty"`

	// AutoFailoverCount is the total number of automatic failovers executed.
	// +optional
	AutoFailoverCount int32 `json:"autoFailoverCount,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=msag,categories=mssql
// +kubebuilder:printcolumn:name="AG",type=string,JSONPath=`.spec.agName`
// +kubebuilder:printcolumn:name="Primary",type=string,JSONPath=`.status.primaryReplica`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas[*].serverName`,priority=1
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AvailabilityGroup is the Schema for the availabilitygroups API.
type AvailabilityGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AvailabilityGroupSpec   `json:"spec,omitempty"`
	Status AvailabilityGroupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AvailabilityGroupList contains a list of AvailabilityGroup.
type AvailabilityGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AvailabilityGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AvailabilityGroup{}, &AvailabilityGroupList{})
}
