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

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/connection"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client

	// testFake stands in for Universal DDI. Every controller under test talks to it
	// through the same interfaces the SDK client implements.
	testFake *uddi.Fake
	// testResolver resolves Connections, with its client factory pointed at testFake.
	testResolver *connection.Resolver
	// testRuntime is the shared object-layer runtime.
	testRuntime *uddisync.Runtime
	// testRecorder captures the events the loop emits.
	testRecorder *record.FakeRecorder

	nsCounter int
)

// TestClusterID is the cluster id stamped into the ownership tags during tests.
const TestClusterID = "test-cluster-0001"

// newNamespace creates a fresh namespace so specs cannot collide on object names.
func newNamespace() string {
	nsCounter++
	name := fmt.Sprintf("uddi-test-%d", nsCounter)
	Expect(k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	})).To(Succeed())
	return name
}

// drainEvents empties the recorder so each spec starts clean.
func drainEvents() {
	for {
		select {
		case <-testRecorder.Events:
		default:
			return
		}
	}
}

// collectEvents returns every event recorded so far.
func collectEvents() []string {
	var out []string
	for {
		select {
		case e := <-testRecorder.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// makeSecret creates a Secret holding an API key.
func makeSecret(namespace, name, key, value string) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string][]byte{key: []byte(value)},
	}
	Expect(k8sClient.Create(ctx, s)).To(Succeed())
	return s
}

// makeReadyConnection creates a Secret plus a Connection and reconciles the Connection
// until it reports Ready, which is what every object-layer spec needs first.
func makeReadyConnection(namespace, name string) *uddiv1alpha1.Connection {
	makeSecret(namespace, name+"-key", uddiv1alpha1.DefaultSecretKey, "not-a-real-key")
	conn := &uddiv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: uddiv1alpha1.ConnectionSpec{
			SecretRef:   uddiv1alpha1.SecretKeyRef{Name: name + "-key"},
			DefaultTags: map[string]string{"env": "test"},
		},
	}
	Expect(k8sClient.Create(ctx, conn)).To(Succeed())

	r := &ConnectionReconciler{Client: k8sClient, Connections: testResolver, Recorder: testRecorder}
	_, err := r.Reconcile(ctx, requestFor(conn))
	Expect(err).NotTo(HaveOccurred())

	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(conn), conn)).To(Succeed())
	Expect(connection.IsReady(conn)).To(BeTrue())
	return conn
}

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	err = uddiv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// Wire the controllers to the in-memory Universal DDI fake. The factory hook on
	// the resolver is the only seam needed: everything above it is production code.
	testFake = uddi.NewFake()
	testResolver = &connection.Resolver{
		Client: k8sClient,
		Factory: func(uddi.Config) (*uddi.Client, error) {
			return testFake.Client(), nil
		},
	}
	testRecorder = record.NewFakeRecorder(1024)
	testRuntime = &uddisync.Runtime{
		Client:      k8sClient,
		Scheme:      scheme.Scheme,
		Recorder:    testRecorder,
		Connections: testResolver,
		ClusterID:   TestClusterID,
	}
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
