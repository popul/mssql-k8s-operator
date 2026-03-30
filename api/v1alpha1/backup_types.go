package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BackupType defines the type of SQL Server backup.
// +kubebuilder:validation:Enum=Full;Differential;Log
type BackupType string

const (
	BackupTypeFull         BackupType = "Full"
	BackupTypeDifferential BackupType = "Differential"
	BackupTypeLog          BackupType = "Log"
)

// BackupPhase describes the current phase of the backup operation.
type BackupPhase string

const (
	BackupPhasePending   BackupPhase = "Pending"
	BackupPhaseRunning   BackupPhase = "Running"
	BackupPhaseCompleted BackupPhase = "Completed"
	BackupPhaseFailed    BackupPhase = "Failed"
)

// BackupSpec defines the desired state of a SQL Server backup.
type BackupSpec struct {
	// Server defines the SQL Server connection details.
	Server ServerReference `json:"server"`

	// DatabaseName is the database to back up.
	// +kubebuilder:validation:MinLength=1
	DatabaseName string `json:"databaseName"`

	// Type is the backup type: Full, Differential, or Log.
	// +kubebuilder:default=Full
	Type BackupType `json:"type,omitempty"`

	// Destination is the file path on the SQL Server filesystem (e.g. /var/opt/mssql/backups/mydb.bak).
	// +kubebuilder:validation:MinLength=1
	Destination string `json:"destination"`

	// Compression enables backup compression.
	// +optional
	// +kubebuilder:default=false
	Compression *bool `json:"compression,omitempty"`
}

// BackupStatus defines the observed state of a SQL Server backup.
type BackupStatus struct {
	// Phase is the current phase of the backup operation.
	Phase BackupPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// StartTime is when the backup operation started.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the backup operation completed.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// BackupSize is the size of the backup file in bytes (reported by SQL Server).
	// +optional
	BackupSize *int64 `json:"backupSize,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=msbak,categories=mssql
// +kubebuilder:printcolumn:name="Database",type=string,JSONPath=`.spec.databaseName`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Backup is the Schema for the backups API.
type Backup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BackupSpec   `json:"spec,omitempty"`
	Status BackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BackupList contains a list of Backup.
type BackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Backup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Backup{}, &BackupList{})
}
