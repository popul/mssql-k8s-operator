package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RecoveryModel defines the SQL Server recovery model for a database.
// +kubebuilder:validation:Enum=Full;Simple;BulkLogged
type RecoveryModel string

const (
	RecoveryModelFull       RecoveryModel = "Full"
	RecoveryModelSimple     RecoveryModel = "Simple"
	RecoveryModelBulkLogged RecoveryModel = "BulkLogged"
)

// DatabaseOption represents a boolean database option (SET ON/OFF).
type DatabaseOption struct {
	// Name is the database option name (e.g. ALLOW_SNAPSHOT_ISOLATION, READ_COMMITTED_SNAPSHOT).
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Value enables or disables the option.
	Value bool `json:"value"`
}

// DatabaseSpec defines the desired state of a SQL Server database.
type DatabaseSpec struct {
	// Server defines the SQL Server connection details.
	Server ServerReference `json:"server"`

	// DatabaseName is the name of the database on SQL Server.
	// +kubebuilder:validation:MinLength=1
	DatabaseName string `json:"databaseName"`

	// Collation for the database. Immutable after creation.
	// +optional
	Collation *string `json:"collation,omitempty"`

	// Owner sets the database owner (ALTER AUTHORIZATION).
	// +optional
	Owner *string `json:"owner,omitempty"`

	// RecoveryModel sets the database recovery model (Full, Simple, BulkLogged).
	// Full is required for transaction log backups and point-in-time restore.
	// +optional
	RecoveryModel *RecoveryModel `json:"recoveryModel,omitempty"`

	// CompatibilityLevel sets the database compatibility level (e.g. 160 for SQL 2022).
	// +optional
	// +kubebuilder:validation:Minimum=100
	// +kubebuilder:validation:Maximum=170
	CompatibilityLevel *int32 `json:"compatibilityLevel,omitempty"`

	// Options sets database-level options (e.g. ALLOW_SNAPSHOT_ISOLATION).
	// +optional
	Options []DatabaseOption `json:"options,omitempty"`

	// DeletionPolicy determines whether to DROP or RETAIN the database on CR deletion.
	// +optional
	// +kubebuilder:default=Retain
	DeletionPolicy *DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// DatabaseStatus defines the observed state of a SQL Server database.
type DatabaseStatus struct {
	// Conditions represent the latest available observations of the database's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=msdb,categories=mssql
// +kubebuilder:printcolumn:name="Database",type=string,JSONPath=`.spec.databaseName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Database is the Schema for the databases API.
type Database struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DatabaseSpec   `json:"spec,omitempty"`
	Status DatabaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DatabaseList contains a list of Database.
type DatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Database `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Database{}, &DatabaseList{})
}
