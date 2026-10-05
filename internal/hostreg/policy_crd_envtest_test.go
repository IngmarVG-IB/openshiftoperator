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

package hostreg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// TestHostRegistrationPolicyCRDValidationEnvtest proves, against a real apiserver, that the
// generated HostRegistrationPolicy CRD defaults and validates the milestone 7 fields as
// DESIGN.md 4.7 says: macSource.metal3, and additionalInterfaces (at most 8, unique by name,
// DNS label names, IPv4 or IPv6 CIDRs, a spaceRef that names an IPSpace, a bounded
// hostnameSuffix). It runs on whatever Kubernetes version the envtest assets provide, so
// make test-floor covers the 1.29 CEL and schema behaviour.
//
//nolint:gocyclo // one table of accept and reject cases per field, sharing one apiserver start.
func TestHostRegistrationPolicyCRDValidationEnvtest(t *testing.T) {
	if testing.Short() {
		t.Skip("envtest starts a real kube-apiserver; skipped in -short mode")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
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
	if err := uddiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("uddiv1alpha1.AddToScheme: %v", err)
	}
	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	ctx := context.Background()

	// newPolicy builds a minimal valid policy and applies mutate to it.
	newPolicy := func(mutate func(*uddiv1alpha1.HostRegistrationPolicySpec)) *uddiv1alpha1.HostRegistrationPolicy {
		p := &uddiv1alpha1.HostRegistrationPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", GenerateName: "hrp-"},
			Spec:       uddiv1alpha1.HostRegistrationPolicySpec{SpaceRef: uddiv1alpha1.ObjectRef{Name: "corp"}},
		}
		if mutate != nil {
			mutate(&p.Spec)
		}
		return p
	}
	iface := func(name, cidr string) uddiv1alpha1.AdditionalInterface {
		return uddiv1alpha1.AdditionalInterface{Name: name, CIDR: cidr}
	}
	withIfaces := func(entries ...uddiv1alpha1.AdditionalInterface) func(*uddiv1alpha1.HostRegistrationPolicySpec) {
		return func(s *uddiv1alpha1.HostRegistrationPolicySpec) { s.AdditionalInterfaces = entries }
	}

	t.Run("metal3 defaults", func(t *testing.T) {
		p := newPolicy(nil)
		if err := k8sClient.Create(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		m := p.Spec.MACSource.Metal3
		if m.Enabled == nil || !*m.Enabled {
			t.Errorf("metal3.enabled = %v, want defaulted to true", m.Enabled)
		}
		if m.Namespace != "openshift-machine-api" {
			t.Errorf("metal3.namespace = %q, want the default openshift-machine-api", m.Namespace)
		}
		if p.Spec.AdditionalInterfaces != nil {
			t.Errorf("additionalInterfaces = %+v, want none", p.Spec.AdditionalInterfaces)
		}
	})

	t.Run("metal3 explicit values are kept", func(t *testing.T) {
		off := false
		p := newPolicy(func(s *uddiv1alpha1.HostRegistrationPolicySpec) {
			s.MACSource.Metal3 = uddiv1alpha1.Metal3Source{Enabled: &off, Namespace: "metal3-system"}
		})
		if err := k8sClient.Create(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		m := p.Spec.MACSource.Metal3
		if m.Enabled == nil || *m.Enabled || m.Namespace != "metal3-system" {
			t.Errorf("metal3 = %+v, want enabled=false namespace=metal3-system", m)
		}
	})

	t.Run("metal3 namespace validation", func(t *testing.T) {
		for _, ns := range []string{"Bad_NS", "-lead", "trail-", "has space", strings.Repeat("a", 64)} {
			p := newPolicy(func(s *uddiv1alpha1.HostRegistrationPolicySpec) { s.MACSource.Metal3.Namespace = ns })
			if err := k8sClient.Create(ctx, p); err == nil {
				t.Errorf("namespace %q was accepted", ns)
			}
		}
	})

	t.Run("valid additionalInterfaces", func(t *testing.T) {
		p := newPolicy(withIfaces(
			uddiv1alpha1.AdditionalInterface{Name: "storage", CIDR: "10.20.0.0/16", HostnameSuffix: "-storage", SpaceRef: &uddiv1alpha1.ObjectRef{Name: "storage"}},
			iface("v6", "fd00:20::/64"),
			iface("a1", "0.0.0.0/0"),
			iface("host", "192.168.1.5/32"),
		))
		if err := k8sClient.Create(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		if len(p.Spec.AdditionalInterfaces) != 4 {
			t.Fatalf("round trip kept %d interfaces, want 4", len(p.Spec.AdditionalInterfaces))
		}
	})

	t.Run("exactly 8 additionalInterfaces is allowed, 9 is not", func(t *testing.T) {
		var entries []uddiv1alpha1.AdditionalInterface
		for i := 0; i < 8; i++ {
			entries = append(entries, iface(fmt.Sprintf("if%d", i), fmt.Sprintf("10.%d.0.0/16", i)))
		}
		if err := k8sClient.Create(ctx, newPolicy(withIfaces(entries...))); err != nil {
			t.Fatalf("8 entries rejected: %v", err)
		}
		entries = append(entries, iface("if8", "10.8.0.0/16"))
		if err := k8sClient.Create(ctx, newPolicy(withIfaces(entries...))); err == nil {
			t.Errorf("9 entries were accepted")
		}
	})

	t.Run("duplicate names are rejected", func(t *testing.T) {
		err := k8sClient.Create(ctx, newPolicy(withIfaces(iface("storage", "10.20.0.0/16"), iface("storage", "10.30.0.0/16"))))
		if err == nil {
			t.Errorf("duplicate interface names were accepted")
		}
	})

	t.Run("interface names must be DNS labels", func(t *testing.T) {
		for _, name := range []string{"", "Storage", "stor_age", "-storage", "storage-", "has space", strings.Repeat("a", 64)} {
			if err := k8sClient.Create(ctx, newPolicy(withIfaces(iface(name, "10.20.0.0/16")))); err == nil {
				t.Errorf("name %q was accepted", name)
			}
		}
		for _, name := range []string{"a", "storage", "s-1", "0day", strings.Repeat("a", 63)} {
			if err := k8sClient.Create(ctx, newPolicy(withIfaces(iface(name, "10.20.0.0/16")))); err != nil {
				t.Errorf("name %q was rejected: %v", name, err)
			}
		}
	})

	t.Run("cidr must be an IPv4 or IPv6 CIDR", func(t *testing.T) {
		good := []string{
			"10.20.0.0/16", "0.0.0.0/0", "255.255.255.255/32", "192.168.1.5/24",
			"fd00:20::/64", "::/0", "::1/128", "2001:db8::/32", "fe80::1/10",
			"2001:db8:0:0:0:0:0:0/64", "1::/8", "::ffff:0:0/96", "2001:DB8::/48",
		}
		bad := []string{
			"", "10.20.0.0", "10.20.0.0/", "10.20.0.0/33", "10.20.0.0/033", "256.0.0.0/8", "10.20.0/16", "10.20.0.0.0/16",
			"fd00:20::", "fd00:20::/129", "fd00:20::/", "gggg::/64", "fd00::20::1/64", "12345::/16",
			"not-a-cidr", "10.20.0.0/16 ", " 10.20.0.0/16", "10.20.0.0/-1",
		}
		for _, cidr := range good {
			if err := k8sClient.Create(ctx, newPolicy(withIfaces(iface("x", cidr)))); err != nil {
				t.Errorf("cidr %q was rejected: %v", cidr, err)
			}
		}
		for _, cidr := range bad {
			if err := k8sClient.Create(ctx, newPolicy(withIfaces(iface("x", cidr)))); err == nil {
				t.Errorf("cidr %q was accepted", cidr)
			}
		}
	})

	t.Run("interface spaceRef must name an IPSpace", func(t *testing.T) {
		for name, ref := range map[string]*uddiv1alpha1.ObjectRef{
			"id only":       {ID: "ipam/ip_space/1234"},
			"name and id":   {Name: "storage", ID: "ipam/ip_space/1234"},
			"empty":         {},
			"empty name":    {Name: ""},
			"name too long": {Name: strings.Repeat("a", 254)},
		} {
			e := iface("storage", "10.20.0.0/16")
			e.SpaceRef = ref
			if err := k8sClient.Create(ctx, newPolicy(withIfaces(e))); err == nil {
				t.Errorf("spaceRef %q (%+v) was accepted", name, ref)
			}
		}
	})

	t.Run("hostnameSuffix is bounded DNS label characters", func(t *testing.T) {
		for _, suffix := range []string{"", "-storage", "s", "-ST-1", strings.Repeat("a", 63)} {
			e := iface("storage", "10.20.0.0/16")
			e.HostnameSuffix = suffix
			if err := k8sClient.Create(ctx, newPolicy(withIfaces(e))); err != nil {
				t.Errorf("suffix %q was rejected: %v", suffix, err)
			}
		}
		for _, suffix := range []string{"_storage", ".storage", "-stor age", "-sto/rage", strings.Repeat("a", 64)} {
			e := iface("storage", "10.20.0.0/16")
			e.HostnameSuffix = suffix
			if err := k8sClient.Create(ctx, newPolicy(withIfaces(e))); err == nil {
				t.Errorf("suffix %q was accepted", suffix)
			}
		}
	})

	t.Run("the sample is valid", func(t *testing.T) {
		f, err := os.Open(filepath.Join("..", "..", "config", "samples", "uddi_v1alpha1_hostregistrationpolicy.yaml"))
		if err != nil {
			t.Fatalf("open sample: %v", err)
		}
		defer func() { _ = f.Close() }()

		sample := &uddiv1alpha1.HostRegistrationPolicy{}
		if err := yaml.NewYAMLOrJSONDecoder(f, 4096).Decode(sample); err != nil {
			t.Fatalf("decode sample: %v", err)
		}
		sample.Namespace = "default"
		if err := k8sClient.Create(ctx, sample); err != nil {
			t.Fatalf("create sample: %v", err)
		}
		if len(sample.Spec.AdditionalInterfaces) == 0 {
			t.Errorf("the sample shows no additionalInterfaces")
		}
		if sample.Spec.MACSource.Metal3.Namespace == "" {
			t.Errorf("the sample shows no macSource.metal3")
		}
	})
}

// firstEnvtestBinaryDir mirrors the helper of the same name in internal/nmstateapi's
// envtest test: it locates the envtest binaries for running the test directly, when
// KUBEBUILDER_ASSETS is not set.
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
