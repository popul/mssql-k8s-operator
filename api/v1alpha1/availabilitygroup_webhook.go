package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Default implements webhook.Defaulter.
func (r *AvailabilityGroup) Default() {
	if r.Spec.AutomatedBackupPreference == nil {
		pref := "Secondary"
		r.Spec.AutomatedBackupPreference = &pref
	}
	if r.Spec.DBFailover == nil {
		t := true
		r.Spec.DBFailover = &t
	}
	for i := range r.Spec.Replicas {
		if r.Spec.Replicas[i].AvailabilityMode == "" {
			r.Spec.Replicas[i].AvailabilityMode = AvailabilityModeSynchronous
		}
		if r.Spec.Replicas[i].FailoverMode == "" {
			r.Spec.Replicas[i].FailoverMode = FailoverModeAutomatic
		}
		if r.Spec.Replicas[i].SeedingMode == "" {
			r.Spec.Replicas[i].SeedingMode = SeedingModeAutomatic
		}
		if r.Spec.Replicas[i].SecondaryRole == "" {
			r.Spec.Replicas[i].SecondaryRole = SecondaryRoleNo
		}
		if r.Spec.Replicas[i].Server.Port == nil {
			port := int32(1433)
			r.Spec.Replicas[i].Server.Port = &port
		}
	}
	if r.Spec.Listener != nil && r.Spec.Listener.Port == nil {
		port := int32(1433)
		r.Spec.Listener.Port = &port
	}
}

// ValidateCreate implements webhook.Validator.
func (r *AvailabilityGroup) ValidateCreate() ([]string, error) {
	return nil, r.validateAG()
}

// ValidateUpdate implements webhook.Validator.
func (r *AvailabilityGroup) ValidateUpdate(old runtime.Object) ([]string, error) {
	oldAG, ok := old.(*AvailabilityGroup)
	if !ok {
		return nil, fmt.Errorf("expected AvailabilityGroup, got %T", old)
	}

	var allErrs field.ErrorList

	// AG name is immutable
	if r.Spec.AGName != oldAG.Spec.AGName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "agName"),
			"agName is immutable after creation"))
	}

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}

	// Validate the rest normally (replicas, databases, listener can be updated)
	return nil, r.validateAG()
}

// ValidateDelete implements webhook.Validator.
func (r *AvailabilityGroup) ValidateDelete() ([]string, error) {
	return nil, nil
}

func (r *AvailabilityGroup) validateAG() error {
	var allErrs field.ErrorList

	specPath := field.NewPath("spec")

	if r.Spec.AGName == "" {
		allErrs = append(allErrs, field.Required(
			specPath.Child("agName"),
			"agName is required"))
	}

	if len(r.Spec.Replicas) < 2 {
		allErrs = append(allErrs, field.Invalid(
			specPath.Child("replicas"),
			len(r.Spec.Replicas),
			"at least 2 replicas are required"))
	}

	// Validate each replica
	serverNames := make(map[string]bool)
	for i := range r.Spec.Replicas {
		replica := &r.Spec.Replicas[i]
		replicaPath := specPath.Child("replicas").Index(i)

		if replica.ServerName == "" {
			allErrs = append(allErrs, field.Required(
				replicaPath.Child("serverName"),
				"serverName is required"))
		}
		if serverNames[replica.ServerName] {
			allErrs = append(allErrs, field.Duplicate(
				replicaPath.Child("serverName"),
				replica.ServerName))
		}
		serverNames[replica.ServerName] = true

		if replica.EndpointURL == "" {
			allErrs = append(allErrs, field.Required(
				replicaPath.Child("endpointURL"),
				"endpointURL is required"))
		}
		if replica.Server.Host == "" {
			allErrs = append(allErrs, field.Required(
				replicaPath.Child("server", "host"),
				"server host is required"))
		}
		if replica.Server.CredentialsSecret.Name == "" {
			allErrs = append(allErrs, field.Required(
				replicaPath.Child("server", "credentialsSecret", "name"),
				"credentialsSecret name is required"))
		}

		// Automatic failover requires synchronous commit
		if replica.FailoverMode == FailoverModeAutomatic &&
			replica.AvailabilityMode == AvailabilityModeAsynchronous {
			allErrs = append(allErrs, field.Invalid(
				replicaPath.Child("failoverMode"),
				string(replica.FailoverMode),
				"Automatic failover requires SynchronousCommit availability mode"))
		}
	}

	// Validate databases
	dbNames := make(map[string]bool)
	for i, db := range r.Spec.Databases {
		dbPath := specPath.Child("databases").Index(i)
		if db.Name == "" {
			allErrs = append(allErrs, field.Required(
				dbPath.Child("name"),
				"database name is required"))
		}
		if dbNames[db.Name] {
			allErrs = append(allErrs, field.Duplicate(
				dbPath.Child("name"),
				db.Name))
		}
		dbNames[db.Name] = true
	}

	// Validate listener
	if r.Spec.Listener != nil {
		listenerPath := specPath.Child("listener")
		if r.Spec.Listener.Name == "" {
			allErrs = append(allErrs, field.Required(
				listenerPath.Child("name"),
				"listener name is required"))
		}
	}

	if len(allErrs) > 0 {
		return allErrs.ToAggregate()
	}
	return nil
}
