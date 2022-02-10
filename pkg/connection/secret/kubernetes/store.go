/*
Copyright 2022 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package kubernetes

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/connection/secret/store"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
)

// Error strings.
const (
	errGetSecret            = "cannot get secret"
	errDeleteSecret         = "cannot delete secret"
	errUpdateSecret         = "cannot update secret"
	errCreateOrUpdateSecret = "cannot create or update connection applicator"

	errExtractKubernetesAuthCreds = "cannot extract kubernetes auth credentials"
)

type SecretStore struct {
	client     client.Client
	applicator resource.Applicator

	// remoteCluster will be used to decide whether to use owner references
	remoteCluster    bool
	defaultNamespace string
}

// NewSecretStore returns a new KubernetesSecretStore.
func NewSecretStore(ctx context.Context, local client.Client, cfg v1.SecretStoreConfig) (store.Store, error) {
	if cfg.Kubernetes == nil {
		// No KubernetesSecretStoreConfig provided, local API Server
		// will be used as Secret Store.
		return &SecretStore{
			client: local,
			applicator: resource.NewApplicatorWithRetry(resource.NewAPIPatchingApplicator(local),
				resource.IsAPIErrorWrapped, nil),
			defaultNamespace: cfg.DefaultScope,
		}, nil
	}

	kfg, err := resource.CommonCredentialExtractor(ctx, cfg.Kubernetes.Auth.Source, local, cfg.Kubernetes.Auth.CommonCredentialSelectors)
	if err != nil {
		return nil, errors.Wrap(err, errExtractKubernetesAuthCreds)
	}
	remote, err := clientForKubeconfig(kfg)
	if err != nil {
		return nil, errors.Wrap(err, errExtractKubernetesAuthCreds)
	}

	return &SecretStore{
		client: remote,
		applicator: resource.NewApplicatorWithRetry(resource.NewAPIPatchingApplicator(remote),
			resource.IsAPIErrorWrapped, nil),
		defaultNamespace: cfg.DefaultScope,
		remoteCluster:    true,
	}, nil
}

func (ss *SecretStore) ReadKeyValues(ctx context.Context, i store.SecretInstance) (store.KeyValues, error) {
	s := &corev1.Secret{}
	return s.Data, errors.Wrapf(ss.client.Get(ctx, types.NamespacedName{Name: i.Name, Namespace: i.Scope}, s), errGetSecret)
}

func (ss *SecretStore) WriteKeyValues(ctx context.Context, i store.SecretInstance, kv store.KeyValues) error {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            i.Name,
			Namespace:       i.Scope,
			OwnerReferences: []metav1.OwnerReference{i.Owner},
		},
		Type: resource.SecretTypeConnection,
		Data: kv,
	}

	if !ss.remoteCluster {
		return errors.Wrap(ss.applicator.Apply(ctx, s, resource.ConnectionSecretMustBeControllableBy(i.Owner.UID)), errCreateOrUpdateSecret)
	}
	// TODO(turkenh): Owner references will not work for remote clusters,
	//  find an alternative.
	return errors.Wrap(ss.applicator.Apply(ctx, s), errCreateOrUpdateSecret)
}

func (ss *SecretStore) DeleteKeyValues(ctx context.Context, i store.SecretInstance, kv store.KeyValues) error {
	s := &corev1.Secret{}
	err := ss.client.Get(ctx, types.NamespacedName{Name: i.Name, Namespace: i.Scope}, s)
	if kerrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return errors.Wrap(err, errGetSecret)
	}
	// Delete all keys from secret data
	for k := range kv {
		delete(s.Data, k)
	}
	// If there are still keys left, update the secret with the remaining.
	if len(s.Data) > 0 {
		return errors.Wrapf(ss.client.Update(ctx, s), errUpdateSecret)
	}
	// If there are no keys left, delete the secret.
	return errors.Wrapf(ss.client.Delete(ctx, s), errDeleteSecret)
}
