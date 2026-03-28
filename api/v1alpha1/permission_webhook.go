package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Default implements webhook.Defaulter.
func (r *Permission) Default() {
	if r.Spec.Server.Port == nil {
		port := int32(1433)
		r.Spec.Server.Port = &port
	}
}

// ValidateCreate implements webhook.Validator.
func (r *Permission) ValidateCreate() ([]string, error) {
	return nil, r.validatePermission()
}

// ValidateUpdate implements webhook.Validator.
func (r *Permission) ValidateUpdate(old runtime.Object) ([]string, error) {
	oldPerm, ok := old.(*Permission)
	if !ok {
		return nil, fmt.Errorf("expected Permission, got %T", old)
	}

	var allErrs field.ErrorList

	// userName is immutable
	if r.Spec.UserName != oldPerm.Spec.UserName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "userName"),
			"userName is immutable after creation"))
	}

	// databaseName is immutable
	if r.Spec.DatabaseName != oldPerm.Spec.DatabaseName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "databaseName"),
			"databaseName is immutable after creation"))
	}

	// server reference is immutable
	if r.Spec.Server.Host != oldPerm.Spec.Server.Host {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "host"),
			"server host is immutable"))
	}

	if !int32PtrEqual(r.Spec.Server.Port, oldPerm.Spec.Server.Port) {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "port"),
			"server port is immutable"))
	}

	if !boolPtrEqual(r.Spec.Server.TLS, oldPerm.Spec.Server.TLS) {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "tls"),
			"server tls is immutable"))
	}

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, r.validatePermission()
}

// ValidateDelete implements webhook.Validator.
func (r *Permission) ValidateDelete() ([]string, error) {
	return nil, nil
}

func (r *Permission) validatePermission() error {
	var allErrs field.ErrorList

	if r.Spec.UserName == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "userName"),
			"userName is required"))
	}

	if r.Spec.DatabaseName == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "databaseName"),
			"databaseName is required"))
	}

	if r.Spec.Server.Host == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "server", "host"),
			"server host is required"))
	}

	if r.Spec.Server.CredentialsSecret.Name == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "server", "credentialsSecret", "name"),
			"credentialsSecret name is required"))
	}

	for i, g := range r.Spec.Grants {
		if g.Permission == "" {
			allErrs = append(allErrs, field.Required(
				field.NewPath("spec", "grants").Index(i).Child("permission"),
				"permission is required"))
		}
		if g.On == "" {
			allErrs = append(allErrs, field.Required(
				field.NewPath("spec", "grants").Index(i).Child("on"),
				"on is required"))
		}
	}

	for i, d := range r.Spec.Denies {
		if d.Permission == "" {
			allErrs = append(allErrs, field.Required(
				field.NewPath("spec", "denies").Index(i).Child("permission"),
				"permission is required"))
		}
		if d.On == "" {
			allErrs = append(allErrs, field.Required(
				field.NewPath("spec", "denies").Index(i).Child("on"),
				"on is required"))
		}
	}

	if len(allErrs) > 0 {
		return allErrs.ToAggregate()
	}
	return nil
}
