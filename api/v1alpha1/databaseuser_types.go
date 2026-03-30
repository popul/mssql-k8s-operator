package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DatabaseUserSpec defines the desired state of a SQL Server database user.
type DatabaseUserSpec struct {
	// Server defines the SQL Server connection details.
	Server ServerReference `json:"server"`

	// DatabaseName is the target database.
	// +kubebuilder:validation:MinLength=1
	DatabaseName string `json:"databaseName"`

	// UserName is the user name inside the database.
	// +kubebuilder:validation:MinLength=1
	UserName string `json:"userName"`

	// LoginRef references a Login CR in the same namespace.
	LoginRef LoginReference `json:"loginRef"`

	// DatabaseRoles is the list of database-level roles for this user.
	// +optional
	DatabaseRoles []string `json:"databaseRoles,omitempty"`
}

// DatabaseUserStatus defines the observed state of a SQL Server database user.
type DatabaseUserStatus struct {
	// Conditions represent the latest available observations of the user's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=msuser,categories=mssql
// +kubebuilder:printcolumn:name="Database",type=string,JSONPath=`.spec.databaseName`
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.userName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DatabaseUser is the Schema for the databaseusers API.
type DatabaseUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DatabaseUserSpec   `json:"spec,omitempty"`
	Status DatabaseUserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DatabaseUserList contains a list of DatabaseUser.
type DatabaseUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DatabaseUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DatabaseUser{}, &DatabaseUserList{})
}
