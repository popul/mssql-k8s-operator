package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PermissionEntry represents a single permission grant or deny.
type PermissionEntry struct {
	// Permission is the SQL Server permission (e.g. SELECT, INSERT, UPDATE, DELETE, EXECUTE, ALTER, CONTROL).
	// +kubebuilder:validation:MinLength=1
	Permission string `json:"permission"`

	// On is the target scope (e.g. "SCHEMA::app", "OBJECT::dbo.Users", "DATABASE::myapp").
	// +kubebuilder:validation:MinLength=1
	On string `json:"on"`
}

// PermissionSpec defines the desired state of SQL Server permissions for a user.
type PermissionSpec struct {
	// Server defines the SQL Server connection details.
	Server ServerReference `json:"server"`

	// DatabaseName is the target database.
	// +kubebuilder:validation:MinLength=1
	DatabaseName string `json:"databaseName"`

	// UserName is the database user to grant/deny permissions to.
	// +kubebuilder:validation:MinLength=1
	UserName string `json:"userName"`

	// Grants is the list of permissions to GRANT.
	// +optional
	Grants []PermissionEntry `json:"grants,omitempty"`

	// Denies is the list of permissions to DENY.
	// +optional
	Denies []PermissionEntry `json:"denies,omitempty"`
}

// PermissionStatus defines the observed state of SQL Server permissions.
type PermissionStatus struct {
	// Conditions represent the latest available observations of the permission's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=msperm,categories=mssql
// +kubebuilder:printcolumn:name="Database",type=string,JSONPath=`.spec.databaseName`
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.userName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Permission is the Schema for the permissions API.
type Permission struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PermissionSpec   `json:"spec,omitempty"`
	Status PermissionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PermissionList contains a list of Permission.
type PermissionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Permission `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Permission{}, &PermissionList{})
}
