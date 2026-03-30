package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SchemaSpec defines the desired state of a SQL Server schema.
type SchemaSpec struct {
	// Server defines the SQL Server connection details.
	Server ServerReference `json:"server"`

	// DatabaseName is the target database.
	// +kubebuilder:validation:MinLength=1
	DatabaseName string `json:"databaseName"`

	// SchemaName is the name of the schema inside the database.
	// +kubebuilder:validation:MinLength=1
	SchemaName string `json:"schemaName"`

	// Owner sets the schema owner (ALTER AUTHORIZATION ON SCHEMA).
	// +optional
	Owner *string `json:"owner,omitempty"`

	// DeletionPolicy determines whether to DROP or RETAIN the schema on CR deletion.
	// +optional
	// +kubebuilder:default=Retain
	DeletionPolicy *DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// SchemaStatus defines the observed state of a SQL Server schema.
type SchemaStatus struct {
	// Conditions represent the latest available observations of the schema's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=msschema,categories=mssql
// +kubebuilder:printcolumn:name="Database",type=string,JSONPath=`.spec.databaseName`
// +kubebuilder:printcolumn:name="Schema",type=string,JSONPath=`.spec.schemaName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Schema is the Schema for the schemas API.
type Schema struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SchemaSpec   `json:"spec,omitempty"`
	Status SchemaStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SchemaList contains a list of Schema.
type SchemaList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Schema `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Schema{}, &SchemaList{})
}
