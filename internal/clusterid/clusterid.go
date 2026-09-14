/*
Copyright 2026.

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

// Package clusterid discovers a stable identifier for the cluster the operator runs in.
package clusterid

import (
	"context"
	"fmt"
	"os"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EnvVar overrides discovery entirely. Useful in tests and on plain Kubernetes.
const EnvVar = "CLUSTER_ID"

// FallbackNamespace is read for its UID when no ClusterVersion is available.
const FallbackNamespace = "kube-system"

var clusterVersionGVK = schema.GroupVersionKind{
	Group:   "config.openshift.io",
	Version: "v1",
	Kind:    "ClusterVersion",
}

// Resolver discovers the cluster id once and caches it for the process lifetime.
// The zero value is ready to use.
type Resolver struct {
	once sync.Once
	id   string
	err  error
}

// Get returns the cluster id, discovering it on the first call. A client.Reader is
// enough, so the manager's uncached API reader can be used before the cache starts.
//
// Order: the CLUSTER_ID environment variable, then the OpenShift ClusterVersion object
// named "version" (spec.clusterID), then the UID of the kube-system namespace.
func (r *Resolver) Get(ctx context.Context, c client.Reader) (string, error) {
	r.once.Do(func() {
		r.id, r.err = discover(ctx, c)
	})
	return r.id, r.err
}

// Set overrides the cached value. Tests use it to pin a known id.
func (r *Resolver) Set(id string) {
	r.once.Do(func() {})
	r.id, r.err = id, nil
}

func discover(ctx context.Context, c client.Reader) (string, error) {
	if v := os.Getenv(EnvVar); v != "" {
		return v, nil
	}

	cv := &unstructured.Unstructured{}
	cv.SetGroupVersionKind(clusterVersionGVK)
	err := c.Get(ctx, types.NamespacedName{Name: "version"}, cv)
	if err == nil {
		id, found, ferr := unstructured.NestedString(cv.Object, "spec", "clusterID")
		if ferr == nil && found && id != "" {
			return id, nil
		}
	}

	ns := &corev1.Namespace{}
	if nerr := c.Get(ctx, types.NamespacedName{Name: FallbackNamespace}, ns); nerr != nil {
		return "", fmt.Errorf("clusterid: no ClusterVersion and no %s namespace: %w", FallbackNamespace, nerr)
	}
	if ns.UID == "" {
		return "", fmt.Errorf("clusterid: namespace %s has no UID", FallbackNamespace)
	}
	return string(ns.UID), nil
}
