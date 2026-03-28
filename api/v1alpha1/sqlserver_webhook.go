package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Default implements defaulting for SQLServer.
func (r *SQLServer) Default() {
	if r.Spec.Port == nil {
		p := int32(1433)
		r.Spec.Port = &p
	}
	if r.Spec.AuthMethod == "" {
		r.Spec.AuthMethod = AuthSqlLogin
	}
	if r.Spec.TLS == nil {
		f := false
		r.Spec.TLS = &f
	}
	if r.Spec.MaxConnections == nil {
		m := int32(10)
		r.Spec.MaxConnections = &m
	}
	if r.Spec.ConnectionTimeout == nil {
		t := int32(30)
		r.Spec.ConnectionTimeout = &t
	}
}

// ValidateCreate validates a new SQLServer.
func (r *SQLServer) ValidateCreate() ([]string, error) {
	return nil, r.validate()
}

// ValidateUpdate validates an update to SQLServer.
func (r *SQLServer) ValidateUpdate(old runtime.Object) ([]string, error) {
	oldSrv, ok := old.(*SQLServer)
	if !ok {
		return nil, fmt.Errorf("expected SQLServer, got %T", old)
	}

	var allErrs field.ErrorList

	// Host is immutable
	if r.Spec.Host != oldSrv.Spec.Host {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "host"),
			"host is immutable after creation",
		))
	}

	if err := r.validate(); err != nil {
		return nil, err
	}

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, nil
}

// ValidateDelete validates deletion.
func (r *SQLServer) ValidateDelete() ([]string, error) {
	return nil, nil
}

func (r *SQLServer) validate() error {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")

	if r.Spec.Host == "" {
		allErrs = append(allErrs, field.Required(specPath.Child("host"), "host is required"))
	}

	switch r.Spec.AuthMethod {
	case AuthSqlLogin:
		if r.Spec.CredentialsSecret == nil {
			allErrs = append(allErrs, field.Required(specPath.Child("credentialsSecret"),
				"credentialsSecret is required when authMethod is SqlLogin"))
		}
	case AuthAzureAD:
		if r.Spec.AzureAD == nil {
			allErrs = append(allErrs, field.Required(specPath.Child("azureAD"),
				"azureAD is required when authMethod is AzureAD"))
		}
	case AuthManagedIdentity:
		// managedIdentity block is optional (system-assigned needs no config)
	}

	if len(allErrs) > 0 {
		return allErrs.ToAggregate()
	}
	return nil
}
