package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	sqlclient "github.com/popul/mssql-k8s-operator/internal/sql"
)

const (
	// certPasswordKey is the key in the certificate Secret holding the encryption password.
	certPasswordKey = "password"
	// certCertKey is the key in the certificate Secret holding the PEM certificate.
	certCertKey = "tls.crt"
	// certKeyKey is the key in the certificate Secret holding the PEM private key.
	certKeyKey = "tls.key"
)

// reconcileCertificates ensures certificates are provisioned for all replicas in a managed cluster.
func (r *SQLServerReconciler) reconcileCertificates(ctx context.Context, srv *v1alpha1.SQLServer) (bool, error) {
	inst := srv.Spec.Instance
	replicas := int32(1)
	if inst.Replicas != nil {
		replicas = *inst.Replicas
	}
	if replicas <= 1 {
		return true, nil
	}

	mode := v1alpha1.CertificateModeSelfSigned
	if inst.Certificates != nil && inst.Certificates.Mode != nil {
		mode = *inst.Certificates.Mode
	}

	switch mode {
	case v1alpha1.CertificateModeSelfSigned:
		return r.reconcileSelfSignedCerts(ctx, srv, replicas)
	case v1alpha1.CertificateModeCertManager:
		return r.reconcileCertManagerCerts(ctx, srv, replicas)
	default:
		return false, fmt.Errorf("unsupported certificate mode: %s", mode)
	}
}

// reconcileSelfSignedCerts generates a CA and per-replica certificates, stores them in Secrets.
func (r *SQLServerReconciler) reconcileSelfSignedCerts(ctx context.Context, srv *v1alpha1.SQLServer, replicas int32) (bool, error) {
	logger := log.FromContext(ctx)

	// Generate or fetch CA
	caSecretName := srv.Name + "-ca"
	caKey, caCert, err := r.ensureCASecret(ctx, srv, caSecretName)
	if err != nil {
		return false, fmt.Errorf("failed to ensure CA secret: %w", err)
	}

	// Generate per-replica certificates
	for i := int32(0); i < replicas; i++ {
		replicaCertSecret := fmt.Sprintf("%s-cert-%d", srv.Name, i)
		var existing corev1.Secret
		err := r.Get(ctx, types.NamespacedName{Name: replicaCertSecret, Namespace: srv.Namespace}, &existing)
		if err == nil {
			continue // Already exists
		}
		if !apierrors.IsNotFound(err) {
			return false, err
		}

		// Generate replica cert signed by CA
		replicaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return false, fmt.Errorf("failed to generate replica %d key: %w", i, err)
		}

		host := replicaHost(srv, int(i))
		template := &x509.Certificate{
			SerialNumber: big.NewInt(int64(i) + 2),
			Subject: pkix.Name{
				CommonName: host,
			},
			DNSNames:              []string{host, fmt.Sprintf("%s-%d", srv.Name, i)},
			NotBefore:             time.Now(),
			NotAfter:              time.Now().Add(365 * 24 * time.Hour),
			KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
			BasicConstraintsValid: true,
		}

		certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &replicaKey.PublicKey, caKey)
		if err != nil {
			return false, fmt.Errorf("failed to create replica %d certificate: %w", i, err)
		}

		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
		keyDER, err := x509.MarshalECPrivateKey(replicaKey)
		if err != nil {
			return false, fmt.Errorf("failed to marshal replica %d key: %w", i, err)
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      replicaCertSecret,
				Namespace: srv.Namespace,
				Labels:    instanceLabels(srv),
			},
			Data: map[string][]byte{
				certCertKey:     certPEM,
				certKeyKey:      keyPEM,
				certPasswordKey: []byte(fmt.Sprintf("CertP@ss%d!", i)),
			},
		}
		if err := controllerutil.SetControllerReference(srv, secret, r.Scheme); err != nil {
			return false, err
		}
		if err := r.Create(ctx, secret); err != nil {
			return false, fmt.Errorf("failed to create cert secret for replica %d: %w", i, err)
		}
		logger.Info("created certificate secret", "replica", i, "secret", replicaCertSecret)
	}

	return true, nil
}

// ensureCASecret creates or fetches the CA certificate Secret.
func (r *SQLServerReconciler) ensureCASecret(ctx context.Context, srv *v1alpha1.SQLServer, name string) (*ecdsa.PrivateKey, *x509.Certificate, error) {
	var existing corev1.Secret
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: srv.Namespace}, &existing)
	if err == nil {
		// Parse existing CA
		block, _ := pem.Decode(existing.Data[certKeyKey])
		if block == nil {
			return nil, nil, fmt.Errorf("failed to decode CA private key PEM")
		}
		caKey, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse CA private key: %w", err)
		}
		certBlock, _ := pem.Decode(existing.Data[certCertKey])
		if certBlock == nil {
			return nil, nil, fmt.Errorf("failed to decode CA certificate PEM")
		}
		caCert, err := x509.ParseCertificate(certBlock.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse CA certificate: %w", err)
		}
		return caKey, caCert, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, nil, err
	}

	// Generate new CA
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate CA key: %w", err)
	}

	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: srv.Name + "-ca",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		MaxPathLen:            1,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create CA certificate: %w", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
	keyDER, err := x509.MarshalECPrivateKey(caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal CA key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: srv.Namespace,
			Labels:    instanceLabels(srv),
		},
		Data: map[string][]byte{
			certCertKey: certPEM,
			certKeyKey:  keyPEM,
		},
	}
	if err := controllerutil.SetControllerReference(srv, secret, r.Scheme); err != nil {
		return nil, nil, err
	}
	if err := r.Create(ctx, secret); err != nil {
		return nil, nil, fmt.Errorf("failed to create CA secret: %w", err)
	}
	return caKey, caCert, nil
}

// reconcileCertManagerCerts creates cert-manager Certificate CRs for each replica.
// It uses unstructured objects to avoid a hard dependency on the cert-manager API.
func (r *SQLServerReconciler) reconcileCertManagerCerts(ctx context.Context, srv *v1alpha1.SQLServer, replicas int32) (bool, error) {
	logger := log.FromContext(ctx)
	inst := srv.Spec.Instance

	issuerRef := inst.Certificates.IssuerRef
	if issuerRef == nil {
		return false, fmt.Errorf("issuerRef is required for CertManager mode")
	}

	allReady := true
	for i := int32(0); i < replicas; i++ {
		secretName := fmt.Sprintf("%s-cert-%d", srv.Name, i)

		// Check if the Secret already exists (created by cert-manager)
		var secret corev1.Secret
		err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: srv.Namespace}, &secret)
		if err == nil {
			// Secret exists, cert-manager has provisioned it
			if len(secret.Data[certCertKey]) > 0 {
				continue
			}
			allReady = false
			continue
		}
		if !apierrors.IsNotFound(err) {
			return false, err
		}

		// Create a cert-manager Certificate CR using unstructured to avoid hard dependency
		host := replicaHost(srv, int(i))
		certObj := &metav1.PartialObjectMetadata{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "cert-manager.io/v1",
				Kind:       "Certificate",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: srv.Namespace,
				Labels:    instanceLabels(srv),
			},
		}

		// We need to use unstructured for cert-manager CRs
		logger.Info("cert-manager Certificate CR needed", "name", secretName, "host", host, "issuer", issuerRef.Name)
		_ = certObj
		// For now, mark as not ready — the actual cert-manager integration requires
		// unstructured client. This will be wired up when cert-manager is available.
		allReady = false
	}

	return allReady, nil
}

// distributeCertificatesToSQL connects to each replica and sets up the certificate infrastructure.
func (r *SQLServerReconciler) distributeCertificatesToSQL(ctx context.Context, srv *v1alpha1.SQLServer) error {
	logger := log.FromContext(ctx)
	inst := srv.Spec.Instance
	replicas := int32(1)
	if inst.Replicas != nil {
		replicas = *inst.Replicas
	}

	// Read credentials for connecting to each replica
	secretNS := srv.Namespace
	if srv.Spec.CredentialsSecret != nil && srv.Spec.CredentialsSecret.Namespace != nil {
		secretNS = *srv.Spec.CredentialsSecret.Namespace
	}
	if srv.Spec.CredentialsSecret == nil {
		return fmt.Errorf("credentialsSecret is required")
	}

	username, password, err := getCredentialsFromSecret(ctx, r.Client, secretNS, srv.Spec.CredentialsSecret.Name)
	if err != nil {
		return fmt.Errorf("failed to read credentials: %w", err)
	}

	port := int32(sqlPort)
	if srv.Spec.Port != nil {
		port = *srv.Spec.Port
	}
	tlsEnabled := srv.Spec.TLS != nil && *srv.Spec.TLS

	for i := int32(0); i < replicas; i++ {
		host := replicaHost(srv, int(i))
		logger.V(1).Info("distributing certificates to replica", "replica", i, "host", host)

		sqlConn, err := r.SQLClientFactory(host, int(port), username, password, tlsEnabled)
		if err != nil {
			return fmt.Errorf("failed to connect to replica %d (%s): %w", i, host, err)
		}

		if err := r.setupReplicaCertificates(ctx, sqlConn, srv, i); err != nil {
			sqlConn.Close()
			return fmt.Errorf("failed to setup certificates on replica %d: %w", i, err)
		}
		sqlConn.Close()
	}

	return nil
}

// setupReplicaCertificates configures master key, certificate, endpoint, and peer certs on a single replica.
func (r *SQLServerReconciler) setupReplicaCertificates(ctx context.Context, conn sqlclient.SQLClient, srv *v1alpha1.SQLServer, replicaIndex int32) error {
	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()

	certName := fmt.Sprintf("ag_cert_%d", replicaIndex)

	// 1. Create master key if not exists
	exists, err := conn.MasterKeyExists(sqlCtx)
	if err != nil {
		return fmt.Errorf("failed to check master key: %w", err)
	}
	if !exists {
		if err := conn.CreateMasterKey(sqlCtx, fmt.Sprintf("MasterKey%sP@ss!", srv.Name)); err != nil {
			return fmt.Errorf("failed to create master key: %w", err)
		}
	}

	// 2. Create own certificate if not exists
	certExists, err := conn.CertificateExists(sqlCtx, certName)
	if err != nil {
		return fmt.Errorf("failed to check certificate: %w", err)
	}
	if !certExists {
		subject := fmt.Sprintf("%s-%d cert", srv.Name, replicaIndex)
		if err := conn.CreateCertificate(sqlCtx, certName, subject, "2030-01-01"); err != nil {
			return fmt.Errorf("failed to create certificate: %w", err)
		}
	}

	// 3. Create HADR endpoint if not exists
	endpointExists, err := conn.HADREndpointExists(sqlCtx)
	if err != nil {
		return fmt.Errorf("failed to check endpoint: %w", err)
	}
	if !endpointExists {
		if err := conn.CreateHADREndpointWithCert(sqlCtx, hadrEndpointPort, certName); err != nil {
			return fmt.Errorf("failed to create HADR endpoint: %w", err)
		}
	}

	// 4. Import peer certificates and grant connect
	// In self-signed mode, we use the same cert name pattern, so each replica
	// needs to trust the others. For now, we use T-SQL certificate exchange.
	// Peer cert import will be handled via backup/restore of certificate files
	// through shared volume or Secrets in a future iteration.
	// The current implementation creates the local infrastructure needed.

	return nil
}
