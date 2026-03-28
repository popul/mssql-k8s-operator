package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RestorePhase describes the current phase of the restore operation.
type RestorePhase string

const (
	RestorePhasePending   RestorePhase = "Pending"
	RestorePhaseRunning   RestorePhase = "Running"
	RestorePhaseCompleted RestorePhase = "Completed"
	RestorePhaseFailed    RestorePhase = "Failed"
)

// RestoreSpec defines the desired state of a SQL Server restore.
type RestoreSpec struct {
	// Server defines the SQL Server connection details.
	Server ServerReference `json:"server"`

	// DatabaseName is the target database name for the restore.
	// +kubebuilder:validation:MinLength=1
	DatabaseName string `json:"databaseName"`

	// Source is the backup file path on the SQL Server filesystem.
	// +kubebuilder:validation:MinLength=1
	Source string `json:"source"`
}

// RestoreStatus defines the observed state of a SQL Server restore.
type RestoreStatus struct {
	// Phase is the current phase of the restore operation.
	Phase RestorePhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// StartTime is when the restore operation started.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the restore operation completed.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=msrestore,categories=mssql
// +kubebuilder:printcolumn:name="Database",type=string,JSONPath=`.spec.databaseName`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Restore is the Schema for the restores API.
type Restore struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RestoreSpec   `json:"spec,omitempty"`
	Status RestoreStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RestoreList contains a list of Restore.
type RestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Restore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Restore{}, &RestoreList{})
}
