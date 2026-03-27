package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LoginSpec defines the desired state of a SQL Server login.
type LoginSpec struct {
	// Server defines the SQL Server connection details.
	Server ServerReference `json:"server"`

	// LoginName is the SQL Server login name.
	// +kubebuilder:validation:MinLength=1
	LoginName string `json:"loginName"`

	// PasswordSecret references a Secret containing a "password" key.
	PasswordSecret SecretReference `json:"passwordSecret"`

	// DefaultDatabase for the login.
	// +optional
	DefaultDatabase *string `json:"defaultDatabase,omitempty"`

	// ServerRoles is the list of server-level roles for this login.
	// +optional
	ServerRoles []string `json:"serverRoles,omitempty"`

	// DeletionPolicy determines whether to DROP or RETAIN the login on CR deletion.
	// +optional
	// +kubebuilder:default=Retain
	DeletionPolicy *DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// LoginStatus defines the observed state of a SQL Server login.
type LoginStatus struct {
	// Conditions represent the latest available observations of the login's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// PasswordSecretResourceVersion tracks the ResourceVersion of the password Secret
	// to detect password rotation.
	PasswordSecretResourceVersion string `json:"passwordSecretResourceVersion,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Login",type=string,JSONPath=`.spec.loginName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Login is the Schema for the logins API.
type Login struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LoginSpec   `json:"spec,omitempty"`
	Status LoginStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LoginList contains a list of Login.
type LoginList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Login `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Login{}, &LoginList{})
}
