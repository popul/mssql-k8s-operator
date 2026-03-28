package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FailoverPhase represents the current phase of a failover operation.
// +kubebuilder:validation:Enum=Pending;Running;Completed;Failed
type FailoverPhase string

const (
	FailoverPhasePending   FailoverPhase = "Pending"
	FailoverPhaseRunning   FailoverPhase = "Running"
	FailoverPhaseCompleted FailoverPhase = "Completed"
	FailoverPhaseFailed    FailoverPhase = "Failed"
)

// ConfirmDataLossAnnotation is the annotation key required when force=true.
const ConfirmDataLossAnnotation = "mssql.popul.io/confirm-data-loss"

// AGFailoverSpec defines the desired failover operation.
type AGFailoverSpec struct {
	// AGName is the name of the Availability Group to failover.
	// +kubebuilder:validation:MinLength=1
	AGName string `json:"agName"`

	// TargetReplica is the server name of the secondary to promote to primary.
	// The operator connects to this replica to execute the FAILOVER command.
	// +kubebuilder:validation:MinLength=1
	TargetReplica string `json:"targetReplica"`

	// Server connection details for the target replica.
	Server ServerReference `json:"server"`

	// Force allows failover to an asynchronous replica, accepting potential data loss.
	// Requires the annotation mssql.popul.io/confirm-data-loss: "yes".
	// +optional
	// +kubebuilder:default=false
	Force *bool `json:"force,omitempty"`
}

// AGFailoverStatus defines the observed state of the failover operation.
type AGFailoverStatus struct {
	// Phase is the current phase: Pending, Running, Completed, Failed.
	// +optional
	Phase FailoverPhase `json:"phase,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// StartTime is when the failover started.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the failover completed.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// PreviousPrimary is the server name of the primary before failover.
	// +optional
	PreviousPrimary string `json:"previousPrimary,omitempty"`

	// NewPrimary is the server name of the primary after failover.
	// +optional
	NewPrimary string `json:"newPrimary,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=msagfo,categories=mssql
// +kubebuilder:printcolumn:name="AG",type=string,JSONPath=`.spec.agName`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetReplica`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Previous",type=string,JSONPath=`.status.previousPrimary`,priority=1
// +kubebuilder:printcolumn:name="New",type=string,JSONPath=`.status.newPrimary`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AGFailover is the Schema for one-shot AG failover operations.
type AGFailover struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AGFailoverSpec   `json:"spec,omitempty"`
	Status AGFailoverStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AGFailoverList contains a list of AGFailover.
type AGFailoverList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AGFailover `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AGFailover{}, &AGFailoverList{})
}
