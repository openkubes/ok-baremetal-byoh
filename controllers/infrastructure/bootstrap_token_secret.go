// Copyright 2026 OpenKubes Contributors.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
    "context"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "time"

    infrastructurev1beta1 "github.com/openkubes/ok-baremetal-byoh/apis/infrastructure/v1beta1"
    "github.com/openkubes/ok-baremetal-byoh/common/bootstraptoken"
    corev1 "k8s.io/api/core/v1"
    apierrors "k8s.io/apimachinery/pkg/api/errors"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    bootstrapapi "k8s.io/cluster-bootstrap/token/api"
    bootstraputil "k8s.io/cluster-bootstrap/token/util"
    "sigs.k8s.io/controller-runtime/pkg/client"
)

// The Kubernetes bootstrap token ID is six characters. Deriving the ID from
// the immutable object UID makes the Secret name stable across failed status
// patches and manager restarts. A rare ID collision fails closed: we never
// read or overwrite a Secret that belongs to a different request.
const bootstrapRequestUIDAnnotation = "byoh.openkubes.io/bootstrap-request-uid"

func ensureBootstrapTokenSecret(ctx context.Context, c client.Client, request *infrastructurev1beta1.BootstrapKubeconfig, ttl time.Duration) (string, error) {
    if request.UID == "" {
        return "", fmt.Errorf("bootstrap kubeconfig %s/%s has no UID", request.Namespace, request.Name)
    }
    digest := sha256.Sum256([]byte(request.UID))
    tokenID := hex.EncodeToString(digest[:])[:6]
    secretName := bootstraputil.BootstrapTokenSecretName(tokenID)
    key := client.ObjectKey{Namespace: metav1.NamespaceSystem, Name: secretName}

    // An existing Secret takes precedence, including when a previous
    // reconciliation failed after creating it but before updating status.
    existing := &corev1.Secret{}
    err := c.Get(ctx, key, existing)
    if err == nil {
        return recoverBootstrapToken(existing, request, tokenID)
    }
    if !apierrors.IsNotFound(err) {
        return "", err
    }

    generated, err := bootstraputil.GenerateBootstrapToken()
    if err != nil {
        return "", err
    }
    _, tokenSecret, err := bootstraptoken.GetTokenIDSecretFromBootstrapToken(generated)
    if err != nil {
        return "", err
    }
    token := tokenID + "." + tokenSecret
    secret, err := bootstraptoken.GenerateSecretFromBootstrapToken(token, ttl)
    if err != nil {
        return "", err
    }
    secret.Annotations = map[string]string{bootstrapRequestUIDAnnotation: string(request.UID)}
    if err := c.Create(ctx, secret); err != nil {
        if !apierrors.IsAlreadyExists(err) {
            return "", err
        }
        // Concurrent reconciliation: recover the winner's token, never
        // return the random token from our unsuccessful Create.
        if err := c.Get(ctx, key, existing); err != nil {
            return "", err
        }
        return recoverBootstrapToken(existing, request, tokenID)
    }
    return token, nil
}

func recoverBootstrapToken(secret *corev1.Secret, request *infrastructurev1beta1.BootstrapKubeconfig, expectedID string) (string, error) {
    if secret.Type != bootstrapapi.SecretTypeBootstrapToken ||
        secret.Annotations[bootstrapRequestUIDAnnotation] != string(request.UID) ||
        string(secret.Data[bootstrapapi.BootstrapTokenIDKey]) != expectedID {
        return "", fmt.Errorf("bootstrap token Secret %s/%s is not owned by request %s/%s (UID %s)",
            secret.Namespace, secret.Name, request.Namespace, request.Name, request.UID)
    }
    tokenSecret := string(secret.Data[bootstrapapi.BootstrapTokenSecretKey])
    token := expectedID + "." + tokenSecret
    if _, _, err := bootstraptoken.GetTokenIDSecretFromBootstrapToken(token); err != nil {
        return "", fmt.Errorf("invalid stored bootstrap token for %s/%s: %w", request.Namespace, request.Name, err)
    }
    expiry, err := time.Parse(time.RFC3339, string(secret.Data[bootstrapapi.BootstrapTokenExpirationKey]))
    if err != nil {
        return "", fmt.Errorf("invalid bootstrap token expiry for %s/%s: %w", request.Namespace, request.Name, err)
    }
    if !time.Now().Before(expiry) {
        return "", fmt.Errorf("bootstrap token for %s/%s expired; refusing to silently rotate credentials", request.Namespace, request.Name)
    }
    return token, nil
}
