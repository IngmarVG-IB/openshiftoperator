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

// Package metrics exports the state of the operator's custom resources as Prometheus
// metrics (DESIGN.md section 9). A collector reads the manager's informer cache at scrape
// time, so the reconcile loops carry no metric code and a series disappears as soon as the
// object behind it does. The Universal DDI API metrics live in internal/uddi.
package metrics

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// Metric names, per DESIGN.md section 9.
const (
	metricObjects         = "uddi_operator_objects"
	metricPolicyObjects   = "uddi_operator_policy_objects"
	metricPolicySkipped   = "uddi_operator_policy_skipped"
	metricCollectorErrors = "uddi_operator_collector_errors_total"
)

// Label names shared by more than one metric.
const (
	labelKind      = "kind"
	labelNamespace = "namespace"
	labelPolicy    = "policy"
)

// scrapeTimeout bounds all the cache reads of one scrape together. The reads are in-memory
// once the informers have synced, so the bound only matters while a cache is still starting.
const scrapeTimeout = 5 * time.Second

// The values of the ready label.
const (
	readyTrue    = "true"
	readyFalse   = "false"
	readyUnknown = "unknown"
)

// readyValues fixes the order in which the ready label values are emitted.
var readyValues = []string{readyTrue, readyFalse, readyUnknown}

// unknownReason labels a skipped entry that carries no reason.
const unknownReason = "Unknown"

// observed is what the collector needs to know about one object-layer object.
type observed struct {
	namespace  string
	conditions []metav1.Condition
}

// objectKind lists one kind that carries a Ready condition.
type objectKind struct {
	name string
	list func(ctx context.Context, r client.Reader) ([]observed, error)
}

// policyState is one numeric field of a policy's status.
type policyState struct {
	name  string
	value int32
}

// policyObservation is what the collector needs to know about one policy.
type policyObservation struct {
	namespace string
	name      string
	states    []policyState
	// skipped holds the reason of every entry in status.skipped.
	skipped []string
}

// policyKind lists one policy kind.
type policyKind struct {
	name string
	list func(ctx context.Context, r client.Reader) ([]policyObservation, error)
}

// statusObject is implemented by every object-layer kind (see api/v1alpha1/accessors.go).
type statusObject interface {
	GetNamespace() string
	StatusOf() *uddiv1alpha1.ObjectStatus
}

// listOptions are applied to every list. The collector only reads, so it can skip the deep
// copy the cache would otherwise make of every object on every scrape.
var listOptions = []client.ListOption{client.UnsafeDisableDeepCopy}

// objectKinds are Connection and the nine object-layer kinds, in the order the
// uddi_operator_objects metric documents them.
var objectKinds = []objectKind{
	{"Connection", func(ctx context.Context, r client.Reader) ([]observed, error) {
		var list uddiv1alpha1.ConnectionList
		if err := r.List(ctx, &list, listOptions...); err != nil {
			return nil, err
		}
		out := make([]observed, 0, len(list.Items))
		for i := range list.Items {
			out = append(out, observed{list.Items[i].Namespace, list.Items[i].Status.Conditions})
		}
		return out, nil
	}},
	{"View", func(ctx context.Context, r client.Reader) ([]observed, error) {
		var list uddiv1alpha1.ViewList
		if err := r.List(ctx, &list, listOptions...); err != nil {
			return nil, err
		}
		return fromObjects(list.Items), nil
	}},
	{"AuthZone", func(ctx context.Context, r client.Reader) ([]observed, error) {
		var list uddiv1alpha1.AuthZoneList
		if err := r.List(ctx, &list, listOptions...); err != nil {
			return nil, err
		}
		return fromObjects(list.Items), nil
	}},
	{"Record", func(ctx context.Context, r client.Reader) ([]observed, error) {
		var list uddiv1alpha1.RecordList
		if err := r.List(ctx, &list, listOptions...); err != nil {
			return nil, err
		}
		return fromObjects(list.Items), nil
	}},
	{"IPSpace", func(ctx context.Context, r client.Reader) ([]observed, error) {
		var list uddiv1alpha1.IPSpaceList
		if err := r.List(ctx, &list, listOptions...); err != nil {
			return nil, err
		}
		return fromObjects(list.Items), nil
	}},
	{"AddressBlock", func(ctx context.Context, r client.Reader) ([]observed, error) {
		var list uddiv1alpha1.AddressBlockList
		if err := r.List(ctx, &list, listOptions...); err != nil {
			return nil, err
		}
		return fromObjects(list.Items), nil
	}},
	{"Subnet", func(ctx context.Context, r client.Reader) ([]observed, error) {
		var list uddiv1alpha1.SubnetList
		if err := r.List(ctx, &list, listOptions...); err != nil {
			return nil, err
		}
		return fromObjects(list.Items), nil
	}},
	{"Range", func(ctx context.Context, r client.Reader) ([]observed, error) {
		var list uddiv1alpha1.RangeList
		if err := r.List(ctx, &list, listOptions...); err != nil {
			return nil, err
		}
		return fromObjects(list.Items), nil
	}},
	{"Address", func(ctx context.Context, r client.Reader) ([]observed, error) {
		var list uddiv1alpha1.AddressList
		if err := r.List(ctx, &list, listOptions...); err != nil {
			return nil, err
		}
		return fromObjects(list.Items), nil
	}},
	{"FixedAddress", func(ctx context.Context, r client.Reader) ([]observed, error) {
		var list uddiv1alpha1.FixedAddressList
		if err := r.List(ctx, &list, listOptions...); err != nil {
			return nil, err
		}
		return fromObjects(list.Items), nil
	}},
}

// policyKinds are the three policy kinds. Their states and skip reasons come from status.
var policyKinds = []policyKind{
	{"DNSPublishPolicy", func(ctx context.Context, r client.Reader) ([]policyObservation, error) {
		var list uddiv1alpha1.DNSPublishPolicyList
		if err := r.List(ctx, &list, listOptions...); err != nil {
			return nil, err
		}
		out := make([]policyObservation, 0, len(list.Items))
		for i := range list.Items {
			p := &list.Items[i]
			skipped := make([]string, 0, len(p.Status.Skipped))
			for _, s := range p.Status.Skipped {
				skipped = append(skipped, s.Reason)
			}
			out = append(out, policyObservation{
				namespace: p.Namespace,
				name:      p.Name,
				states:    []policyState{{"published", p.Status.Published}, {"ready", p.Status.Ready}},
				skipped:   skipped,
			})
		}
		return out, nil
	}},
	{"IPAllocationPolicy", func(ctx context.Context, r client.Reader) ([]policyObservation, error) {
		var list uddiv1alpha1.IPAllocationPolicyList
		if err := r.List(ctx, &list, listOptions...); err != nil {
			return nil, err
		}
		out := make([]policyObservation, 0, len(list.Items))
		for i := range list.Items {
			p := &list.Items[i]
			skipped := make([]string, 0, len(p.Status.Skipped))
			for _, s := range p.Status.Skipped {
				skipped = append(skipped, s.Reason)
			}
			out = append(out, policyObservation{
				namespace: p.Namespace,
				name:      p.Name,
				states:    []policyState{{"allocated", p.Status.Allocated}, {"assigned", p.Status.Assigned}},
				skipped:   skipped,
			})
		}
		return out, nil
	}},
	{"HostRegistrationPolicy", func(ctx context.Context, r client.Reader) ([]policyObservation, error) {
		var list uddiv1alpha1.HostRegistrationPolicyList
		if err := r.List(ctx, &list, listOptions...); err != nil {
			return nil, err
		}
		out := make([]policyObservation, 0, len(list.Items))
		for i := range list.Items {
			p := &list.Items[i]
			skipped := make([]string, 0, len(p.Status.Skipped))
			for _, s := range p.Status.Skipped {
				skipped = append(skipped, s.Reason)
			}
			out = append(out, policyObservation{
				namespace: p.Namespace,
				name:      p.Name,
				states:    []policyState{{"registered", p.Status.Registered}, {"pending", p.Status.Pending}},
				skipped:   skipped,
			})
		}
		return out, nil
	}},
}

// fromObjects reads the namespace and conditions of every object in items.
func fromObjects[T any, PT interface {
	*T
	statusObject
}](items []T) []observed {
	out := make([]observed, 0, len(items))
	for i := range items {
		o := PT(&items[i])
		out = append(out, observed{namespace: o.GetNamespace(), conditions: o.StatusOf().Conditions})
	}
	return out
}

// readyIndex returns the position in readyValues of the Ready condition: true, false, or
// unknown when the condition is missing or Unknown.
func readyIndex(conditions []metav1.Condition) int {
	c := meta.FindStatusCondition(conditions, uddiv1alpha1.ConditionReady)
	if c == nil {
		return slices.Index(readyValues, readyUnknown)
	}
	switch c.Status {
	case metav1.ConditionTrue:
		return slices.Index(readyValues, readyTrue)
	case metav1.ConditionFalse:
		return slices.Index(readyValues, readyFalse)
	default:
		return slices.Index(readyValues, readyUnknown)
	}
}

// collector implements prometheus.Collector over a client.Reader, normally the manager's
// cache.
type collector struct {
	reader  client.Reader
	timeout time.Duration

	objects       *prometheus.Desc
	policyObjects *prometheus.Desc
	policySkipped *prometheus.Desc
	// errors is the only stateful metric: it counts failed lists per kind across scrapes.
	errors *prometheus.CounterVec
}

// NewCollector returns a collector that exports uddi_operator_objects,
// uddi_operator_policy_objects, uddi_operator_policy_skipped and
// uddi_operator_collector_errors_total from r. Register it on controller-runtime's
// metrics.Registry with the manager's cache as r.
//
// Each scrape lists every kind afresh, so a series disappears when its objects do. A list
// that fails, for instance because the cache has not started yet, only drops that kind's
// series and increments uddi_operator_collector_errors_total{kind}.
func NewCollector(r client.Reader) prometheus.Collector {
	return newCollector(r, scrapeTimeout)
}

func newCollector(r client.Reader, timeout time.Duration) *collector {
	c := &collector{
		reader:  r,
		timeout: timeout,
		objects: prometheus.NewDesc(metricObjects,
			"Number of objects per kind, namespace and Ready condition value (true, false or unknown).",
			[]string{labelKind, labelNamespace, "ready"}, nil),
		policyObjects: prometheus.NewDesc(metricPolicyObjects,
			"Counts from a policy's status: published and ready, allocated and assigned, or registered and pending.",
			[]string{labelKind, labelNamespace, labelPolicy, "state"}, nil),
		policySkipped: prometheus.NewDesc(metricPolicySkipped,
			"Number of status.skipped entries of a policy per reason.",
			[]string{labelKind, labelNamespace, labelPolicy, "reason"}, nil),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: metricCollectorErrors,
			Help: "Number of scrapes in which listing a kind from the cache failed.",
		}, []string{labelKind}),
	}
	// Start every series at zero so the first failure is visible to rate() and increase().
	for _, k := range objectKinds {
		c.errors.WithLabelValues(k.name)
	}
	for _, k := range policyKinds {
		c.errors.WithLabelValues(k.name)
	}
	return c
}

// Describe implements prometheus.Collector.
func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.objects
	ch <- c.policyObjects
	ch <- c.policySkipped
	c.errors.Describe(ch)
}

// task lists one kind and turns what it found into metrics.
type task struct {
	kind string
	run  func(ctx context.Context) ([]prometheus.Metric, error)
}

// result is the outcome of one task.
type result struct {
	metrics []prometheus.Metric
	err     error
}

func (c *collector) tasks() []task {
	tasks := make([]task, 0, len(objectKinds)+len(policyKinds))
	for _, k := range objectKinds {
		tasks = append(tasks, task{kind: k.name, run: func(ctx context.Context) ([]prometheus.Metric, error) {
			obs, err := k.list(ctx, c.reader)
			if err != nil {
				return nil, err
			}
			return c.objectMetrics(k.name, obs), nil
		}})
	}
	for _, k := range policyKinds {
		tasks = append(tasks, task{kind: k.name, run: func(ctx context.Context) ([]prometheus.Metric, error) {
			obs, err := k.list(ctx, c.reader)
			if err != nil {
				return nil, err
			}
			return c.policyMetrics(k.name, obs), nil
		}})
	}
	return tasks
}

// Collect implements prometheus.Collector. The kinds are listed concurrently under one
// shared deadline, so one slow or failing kind neither delays nor drops the others.
func (c *collector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	tasks := c.tasks()
	results := make([]result, len(tasks))
	var wg sync.WaitGroup
	for i := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].metrics, results[i].err = tasks[i].run(ctx)
		}()
	}
	wg.Wait()

	for i, res := range results {
		if res.err != nil {
			c.errors.WithLabelValues(tasks[i].kind).Inc()
			logf.Log.WithName("metrics").V(1).Info("unable to list objects for metrics, skipping the kind",
				"kind", tasks[i].kind, "error", res.err.Error())
			continue
		}
		for _, m := range res.metrics {
			ch <- m
		}
	}
	c.errors.Collect(ch)
}

// objectMetrics counts obs per namespace and ready value. Every namespace that holds at
// least one object gets a series for each of the three ready values, so a count that drops
// to zero is reported as zero rather than as a gap.
func (c *collector) objectMetrics(kind string, obs []observed) []prometheus.Metric {
	counts := make(map[string][]float64)
	for _, o := range obs {
		perReady, ok := counts[o.namespace]
		if !ok {
			perReady = make([]float64, len(readyValues))
			counts[o.namespace] = perReady
		}
		perReady[readyIndex(o.conditions)]++
	}

	out := make([]prometheus.Metric, 0, len(counts)*len(readyValues))
	for _, namespace := range slices.Sorted(maps.Keys(counts)) {
		for i, ready := range readyValues {
			out = append(out, prometheus.MustNewConstMetric(
				c.objects, prometheus.GaugeValue, counts[namespace][i], kind, namespace, ready))
		}
	}
	return out
}

// policyMetrics emits every state of every policy, and the number of skipped entries per
// reason. A policy without skipped entries has no skipped series.
func (c *collector) policyMetrics(kind string, obs []policyObservation) []prometheus.Metric {
	var out []prometheus.Metric
	for _, p := range obs {
		for _, s := range p.states {
			out = append(out, prometheus.MustNewConstMetric(
				c.policyObjects, prometheus.GaugeValue, float64(s.value), kind, p.namespace, p.name, s.name))
		}

		reasons := make(map[string]float64)
		for _, reason := range p.skipped {
			if reason == "" {
				reason = unknownReason
			}
			reasons[reason]++
		}
		for _, reason := range slices.Sorted(maps.Keys(reasons)) {
			out = append(out, prometheus.MustNewConstMetric(
				c.policySkipped, prometheus.GaugeValue, reasons[reason], kind, p.namespace, p.name, reason))
		}
	}
	return out
}
