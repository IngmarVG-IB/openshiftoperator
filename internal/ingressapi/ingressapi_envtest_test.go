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

package ingressapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	operatoringressv1 "github.com/openshift/api/operatoringress/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestDNSRecordCRDEnvtest proves that test/crds/operatoringress.operator.openshift.io_dnsrecords.yaml
// loads into a real envtest apiserver, that AddToScheme lets a controller-runtime client
// round-trip a DNSRecord shaped like the ingress operator's default router wildcard, and
// that Available reports the API as present once the CRD is installed. The discovery
// response parsing itself, including the "no such API here" case, is unit-tested against a
// fake apiserver in TestAvailable.
func TestDNSRecordCRDEnvtest(t *testing.T) {
	if testing.Short() {
		t.Skip("envtest starts a real kube-apiserver; skipped in -short mode")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "test", "crds"),
			filepath.Join("..", "..", "config", "crd", "bases"),
		},
		ErrorIfCRDPathMissing: true,
	}
	if dir := firstEnvtestBinaryDir(); dir != "" {
		env.BinaryAssetsDirectory = dir
	}

	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("envtest Start: %v", err)
	}
	t.Cleanup(func() {
		if stopErr := env.Stop(); stopErr != nil {
			t.Errorf("envtest Stop: %v", stopErr)
		}
	})

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("clientgoscheme.AddToScheme: %v", err)
	}
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}

	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ingressapi-test-"}}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	record := &operatoringressv1.DNSRecord{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns.Name, Name: "default-wildcard"},
		Spec: operatoringressv1.DNSRecordSpec{
			DNSName:    "*.apps.cluster.example.com.",
			Targets:    []string{"192.0.2.10"},
			RecordType: operatoringressv1.ARecordType,
			RecordTTL:  30,
		},
	}
	if err := k8sClient.Create(ctx, record); err != nil {
		t.Fatalf("create DNSRecord: %v", err)
	}

	got := &operatoringressv1.DNSRecord{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(record), got); err != nil {
		t.Fatalf("get DNSRecord: %v", err)
	}
	if got.Spec.DNSName != "*.apps.cluster.example.com." {
		t.Errorf("got.Spec.DNSName = %q, want %q", got.Spec.DNSName, "*.apps.cluster.example.com.")
	}
	if len(got.Spec.Targets) != 1 || got.Spec.Targets[0] != "192.0.2.10" {
		t.Errorf("got.Spec.Targets = %v, want [192.0.2.10]", got.Spec.Targets)
	}
	if got.Spec.RecordType != operatoringressv1.ARecordType {
		t.Errorf("got.Spec.RecordType = %q, want %q", got.Spec.RecordType, operatoringressv1.ARecordType)
	}
	if got.Spec.RecordTTL != 30 {
		t.Errorf("got.Spec.RecordTTL = %d, want 30", got.Spec.RecordTTL)
	}
	if got.Spec.DNSManagementPolicy != operatoringressv1.ManagedDNS {
		t.Errorf("got.Spec.DNSManagementPolicy = %q, want the CRD default %q", got.Spec.DNSManagementPolicy, operatoringressv1.ManagedDNS)
	}
	if got.UID == "" {
		t.Errorf("got.UID is empty; the apiserver did not assign one")
	}

	var list operatoringressv1.DNSRecordList
	if err := k8sClient.List(ctx, &list, client.InNamespace(ns.Name)); err != nil {
		t.Fatalf("list DNSRecords: %v", err)
	}
	if len(list.Items) != 1 {
		t.Errorf("listed %d DNSRecords, want 1", len(list.Items))
	}

	available, err := Available(cfg)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if !available {
		t.Errorf("Available = false, want true once the DNSRecord CRD is installed")
	}
}

// firstEnvtestBinaryDir mirrors getFirstFoundEnvTestBinaryDir in
// internal/controller/suite_test.go: it locates the envtest binaries so `go test` finds
// them the same way `make test` does (via the KUBEBUILDER_ASSETS environment variable,
// which envtest.Environment honours on its own when BinaryAssetsDirectory is left unset)
// and, as a fallback for running the test directly from an IDE, the first version
// directory under bin/k8s.
func firstEnvtestBinaryDir() string {
	base := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(base, e.Name())
		}
	}
	return ""
}
