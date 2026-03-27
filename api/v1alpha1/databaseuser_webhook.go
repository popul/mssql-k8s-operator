package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Default implements webhook.Defaulter.
func (r *DatabaseUser) Default() {
	if r.Spec.Server.Port == nil {
		port := int32(1433)
		r.Spec.Server.Port = &port
	}
}

// ValidateCreate implements webhook.Validator.
func (r *DatabaseUser) ValidateCreate() ([]string, error) {
	return nil, r.validateDatabaseUser()
}

// ValidateUpdate implements webhook.Validator.
func (r *DatabaseUser) ValidateUpdate(old runtime.Object) ([]string, error) {
	oldUser, ok := old.(*DatabaseUser)
	if !ok {
		return nil, fmt.Errorf("expected DatabaseUser, got %T", old)
	}

	var allErrs field.ErrorList

	// userName is immutable
	if r.Spec.UserName != oldUser.Spec.UserName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "userName"),
			"userName is immutable after creation"))
	}

	// databaseName is immutable
	if r.Spec.DatabaseName != oldUser.Spec.DatabaseName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "databaseName"),
			"databaseName is immutable after creation"))
	}

	// server reference is immutable
	if r.Spec.Server.Host != oldUser.Spec.Server.Host {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "host"),
			"server host is immutable"))
	}

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, r.validateDatabaseUser()
}

// ValidateDelete implements webhook.Validator.
func (r *DatabaseUser) ValidateDelete() ([]string, error) {
	return nil, nil
}

func (r *DatabaseUser) validateDatabaseUser() error {
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

	if r.Spec.LoginRef.Name == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "loginRef", "name"),
			"loginRef name is required"))
	}

	if len(allErrs) > 0 {
		return allErrs.ToAggregate()
	}
	return nil
}
