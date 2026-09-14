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

package controller

// Cluster-scoped reads that belong to the operator as a whole rather than to one
// controller. Cluster identity comes from the OpenShift ClusterVersion object, falling
// back to the kube-system namespace UID; see internal/clusterid.
//
// The integration-layer permissions from DESIGN.md section 10 (Routes, Ingresses,
// Services, Nodes, and patch on Services) arrive with milestones 2 and 3.

// +kubebuilder:rbac:groups=config.openshift.io,resources=clusterversions,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
