package v1alpha1

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
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

	// Instance defaults
	if inst := r.Spec.Instance; inst != nil {
		if inst.Image == nil {
			img := "mcr.microsoft.com/mssql/server:2022-latest"
			inst.Image = &img
		}
		if inst.Edition == nil {
			ed := "Developer"
			inst.Edition = &ed
		}
		if inst.Replicas == nil {
			rep := int32(1)
			inst.Replicas = &rep
		}
		if inst.StorageSize == nil {
			sz := "10Gi"
			inst.StorageSize = &sz
		}
		if inst.ServiceType == nil {
			st := corev1.ServiceTypeClusterIP
			inst.ServiceType = &st
		}
		// Certificates defaults for cluster mode
		if *inst.Replicas > 1 {
			if inst.Certificates == nil {
				inst.Certificates = &CertificateSpec{}
			}
			if inst.Certificates.Mode == nil {
				mode := CertificateModeSelfSigned
				inst.Certificates.Mode = &mode
			}
			if inst.Certificates.Duration == nil {
				dur := "8760h"
				inst.Certificates.Duration = &dur
			}
			if inst.Certificates.RenewBefore == nil {
				rb := "720h"
				inst.Certificates.RenewBefore = &rb
			}
			// AG defaults
			if inst.AvailabilityGroup == nil {
				inst.AvailabilityGroup = &ManagedAGSpec{}
			}
			ag := inst.AvailabilityGroup
			if ag.AGName == nil {
				name := r.Name + "-ag"
				ag.AGName = &name
			}
			if ag.AvailabilityMode == nil {
				mode := "SynchronousCommit"
				ag.AvailabilityMode = &mode
			}
			if ag.AutoFailover == nil {
				af := true
				ag.AutoFailover = &af
			}
			if ag.HealthCheckInterval == nil {
				hci := "10s"
				ag.HealthCheckInterval = &hci
			}
			if ag.FailoverCooldown == nil {
				fc := "60s"
				ag.FailoverCooldown = &fc
			}
		}
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
	specPath := field.NewPath("spec")

	// Host is immutable (external mode)
	if r.Spec.Instance == nil && r.Spec.Host != oldSrv.Spec.Host {
		allErrs = append(allErrs, field.Forbidden(
			specPath.Child("host"),
			"host is immutable after creation",
		))
	}

	// Instance presence is immutable (cannot switch modes)
	if (r.Spec.Instance == nil) != (oldSrv.Spec.Instance == nil) {
		allErrs = append(allErrs, field.Forbidden(
			specPath.Child("instance"),
			"cannot switch between managed and external mode after creation",
		))
	}

	// Instance immutable fields
	if r.Spec.Instance != nil && oldSrv.Spec.Instance != nil {
		instPath := specPath.Child("instance")

		// StorageClassName immutable
		if !stringPtrEqual(r.Spec.Instance.StorageClassName, oldSrv.Spec.Instance.StorageClassName) {
			allErrs = append(allErrs, field.Forbidden(
				instPath.Child("storageClassName"),
				"storageClassName is immutable after creation",
			))
		}

		// Cannot switch between standalone and cluster mode
		oldReplicas := int32(1)
		if oldSrv.Spec.Instance.Replicas != nil {
			oldReplicas = *oldSrv.Spec.Instance.Replicas
		}
		newReplicas := int32(1)
		if r.Spec.Instance.Replicas != nil {
			newReplicas = *r.Spec.Instance.Replicas
		}
		oldIsCluster := oldReplicas > 1
		newIsCluster := newReplicas > 1
		if oldIsCluster != newIsCluster {
			allErrs = append(allErrs, field.Forbidden(
				instPath.Child("replicas"),
				"cannot switch between standalone (1) and cluster (2+) mode after creation",
			))
		}
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

	if r.Spec.Instance != nil {
		instPath := specPath.Child("instance")

		// Host must be empty in managed mode
		if r.Spec.Host != "" {
			allErrs = append(allErrs, field.Forbidden(
				specPath.Child("host"),
				"host must not be set when instance is configured (managed mode)",
			))
		}

		// AcceptEULA is required
		if !r.Spec.Instance.AcceptEULA {
			allErrs = append(allErrs, field.Required(
				instPath.Child("acceptEULA"),
				"acceptEULA must be true to deploy SQL Server",
			))
		}

		// SAPasswordSecret required
		if r.Spec.Instance.SAPasswordSecret.Name == "" {
			allErrs = append(allErrs, field.Required(
				instPath.Child("saPasswordSecret", "name"),
				"saPasswordSecret is required",
			))
		}

		replicas := int32(1)
		if r.Spec.Instance.Replicas != nil {
			replicas = *r.Spec.Instance.Replicas
		}

		// Express edition cannot be used with AG
		if replicas > 1 && r.Spec.Instance.Edition != nil && *r.Spec.Instance.Edition == "Express" {
			allErrs = append(allErrs, field.Forbidden(
				instPath.Child("edition"),
				"Express edition does not support Availability Groups",
			))
		}

		// CertManager mode requires issuerRef
		if replicas > 1 && r.Spec.Instance.Certificates != nil &&
			r.Spec.Instance.Certificates.Mode != nil && *r.Spec.Instance.Certificates.Mode == CertificateModeCertManager {
			if r.Spec.Instance.Certificates.IssuerRef == nil {
				allErrs = append(allErrs, field.Required(
					instPath.Child("certificates", "issuerRef"),
					"issuerRef is required when certificate mode is CertManager",
				))
			}
		}

		// In managed mode, credentialsSecret is optional for SqlLogin:
		// if omitted, the operator falls back to sa + saPasswordSecret.
		// No validation needed here — the controller handles the fallback.
	} else {
		// External mode: host is required
		if r.Spec.Host == "" {
			allErrs = append(allErrs, field.Required(specPath.Child("host"), "host is required when instance is not set"))
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
	}

	if len(allErrs) > 0 {
		return allErrs.ToAggregate()
	}
	return nil
}
