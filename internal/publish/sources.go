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

// This file implements DESIGN.md section 4.7 pipeline step 1: turning a Route, an
// Ingress or a LoadBalancer Service into the Endpoints and Skips a DNSPublishPolicy
// should act on. EndpointsFromRoute, EndpointsFromIngress and EndpointsFromService live
// in sources_route.go, sources_ingress.go and sources_service.go; this file holds the
// logic they share: annotation parsing, target/hostname syntax, mode filtering and
// endpoint assembly.
package publish

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// ParseTargets parses a comma-separated list of targets. A value that parses as an IPv4
// address becomes an A target, an IPv6 address becomes AAAA, and anything else must be a
// syntactically valid DNS name; it becomes a CNAME target, lower-cased with a trailing dot
// added. Empty items (including an entirely empty or whitespace-only string) are ignored.
// An invalid name is an error.
func ParseTargets(csv string) ([]Target, error) {
	items := strings.Split(csv, ",")
	out := make([]Target, 0, len(items))
	for _, raw := range items {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		t, err := parseTarget(item)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// parseTarget classifies one non-empty, trimmed target value.
func parseTarget(item string) (Target, error) {
	if ip := net.ParseIP(item); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return Target{Type: TargetA, Value: ip4.String()}, nil
		}
		return Target{Type: TargetAAAA, Value: ip.String()}, nil
	}
	if !isValidHostname(item) {
		return Target{}, fmt.Errorf("%q is not an IP address or a valid DNS name", item)
	}
	return Target{Type: TargetCNAME, Value: normalizeHostname(item)}, nil
}

// normalizeHostname lower-cases h and ensures it ends in exactly one dot.
func normalizeHostname(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	h = strings.TrimSuffix(h, ".")
	return h + "."
}

// isValidHostname reports whether h is a syntactically valid DNS name once lower-cased
// and stripped of any trailing dot.
func isValidHostname(h string) bool {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	if h == "" {
		return false
	}
	return len(validation.IsDNS1123Subdomain(h)) == 0
}

// filterByMode applies DNSPublishPolicy.spec.target.mode to an already-computed target
// list: CNAME keeps only CNAME targets, A keeps only address targets, and Auto (or any
// other value, including the zero value) keeps everything.
func filterByMode(targets []Target, mode TargetMode) []Target {
	switch mode {
	case TargetModeCNAME:
		return filterTargets(targets, func(t Target) bool { return t.Type == TargetCNAME })
	case TargetModeA:
		return filterTargets(targets, func(t Target) bool { return t.Type == TargetA || t.Type == TargetAAAA })
	default:
		return targets
	}
}

func filterTargets(targets []Target, keep func(Target) bool) []Target {
	out := make([]Target, 0, len(targets))
	for _, t := range targets {
		if keep(t) {
			out = append(out, t)
		}
	}
	return out
}

// targetsFromIPsAndHostnames implements the Ingress/Service target rule shared by
// DESIGN.md 4.7: any IPs give one A/AAAA target per IP; with no IPs, the first hostname
// gives a single CNAME target; with neither, there is no target.
func targetsFromIPsAndHostnames(ips, hostnames []string) []Target {
	if len(ips) > 0 {
		targets, err := ParseTargets(strings.Join(ips, ","))
		if err != nil {
			// The load balancer status is not policy-controlled input; a value the
			// apiserver accepted as an IP always parses, so this defends against a
			// future malformed value rather than a case reachable today.
			return nil
		}
		return targets
	}
	if len(hostnames) > 0 {
		t, err := parseTarget(hostnames[0])
		if err != nil {
			return nil
		}
		return []Target{t}
	}
	return nil
}

// mergeHostnames dedupes and sorts base (a source's own hostnames) together with extra
// (hostnames added by the hostname annotation), both already normalized.
func mergeHostnames(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, list := range [][]string{base, extra} {
		for _, h := range list {
			if h == "" {
				continue
			}
			if _, ok := seen[h]; ok {
				continue
			}
			seen[h] = struct{}{}
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out
}

// buildEndpoints turns one shared target list into an Endpoint per hostname, or a
// NoTarget Skip per hostname when targets is empty. hostnames must already be
// normalized, deduped and sorted.
func buildEndpoints(src SourceRef, hostnames []string, targets []Target, ttl *int32) ([]Endpoint, []Skip) {
	if len(hostnames) == 0 {
		return nil, nil
	}
	if len(targets) == 0 {
		skips := make([]Skip, 0, len(hostnames))
		for _, h := range hostnames {
			skips = append(skips, Skip{
				Source:   src,
				Hostname: h,
				Reason:   SkipNoTarget,
				Message:  "no target could be derived for the configured mode",
			})
		}
		return nil, skips
	}
	eps := make([]Endpoint, 0, len(hostnames))
	for _, h := range hostnames {
		eps = append(eps, Endpoint{Source: src, Hostname: h, Targets: targets, TTL: ttl})
	}
	return eps, nil
}

// parsedAnnotations is the validated result of reading the uddi.infoblox.com/* (or
// SourceOptions.AnnotationPrefix) annotations common to every source kind.
type parsedAnnotations struct {
	// hostnames are extra hostnames from the hostname annotation: normalized, but not
	// yet merged with the source's own hostnames.
	hostnames []string
	// ttl is from the ttl annotation; nil means the annotation was absent.
	ttl *int32
	// targets and hasTargets come from the target annotation: hasTargets is true when
	// the annotation key was present at all (even if it parsed to zero targets), and
	// means the computed targets must be replaced with targets, empty or not.
	targets    []Target
	hasTargets bool
}

// parseAnnotations reads and validates the publish, hostname, ttl and target
// annotations. A non-nil Skip means the whole source is Excluded or the annotations are
// malformed (InvalidAnnotation); the caller returns it as the source's only Skip without
// doing any further work.
func parseAnnotations(o SourceOptions, annotations map[string]string, src SourceRef) (parsedAnnotations, *Skip) {
	prefix := o.Prefix()
	var out parsedAnnotations

	if v, ok := annotations[prefix+"/"+AnnotationPublish]; ok {
		publish, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			return out, invalidAnnotation(src, prefix, AnnotationPublish, err)
		}
		if !publish {
			return out, &Skip{Source: src, Reason: SkipExcluded, Message: fmt.Sprintf("%s/%s is false", prefix, AnnotationPublish)}
		}
	}

	if v, ok := annotations[prefix+"/"+AnnotationHostname]; ok {
		hostnames, err := parseHostnamesAnnotation(v)
		if err != nil {
			return out, invalidAnnotation(src, prefix, AnnotationHostname, err)
		}
		out.hostnames = hostnames
	}

	if v, ok := annotations[prefix+"/"+AnnotationTTL]; ok {
		ttl, err := parseTTLAnnotation(v)
		if err != nil {
			return out, invalidAnnotation(src, prefix, AnnotationTTL, err)
		}
		out.ttl = ttl
	}

	if v, ok := annotations[prefix+"/"+AnnotationTarget]; ok {
		targets, err := ParseTargets(v)
		if err != nil {
			return out, invalidAnnotation(src, prefix, AnnotationTarget, err)
		}
		out.targets = targets
		out.hasTargets = true
	}

	return out, nil
}

func invalidAnnotation(src SourceRef, prefix, key string, err error) *Skip {
	return &Skip{
		Source:  src,
		Reason:  SkipInvalidAnnotation,
		Message: fmt.Sprintf("%s/%s: %v", prefix, key, err),
	}
}

// parseHostnamesAnnotation parses the comma-separated hostname annotation value into
// normalized hostnames. Empty items are ignored; an invalid name is an error.
func parseHostnamesAnnotation(csv string) ([]string, error) {
	items := strings.Split(csv, ",")
	out := make([]string, 0, len(items))
	for _, raw := range items {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		if !isValidHostname(item) {
			return nil, fmt.Errorf("%q is not a valid DNS name", item)
		}
		out = append(out, normalizeHostname(item))
	}
	return out, nil
}

// parseTTLAnnotation parses the ttl annotation, which must be a positive integer number
// of seconds that fits in an int32 (the field TTL points at).
func parseTTLAnnotation(v string) (*int32, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 32)
	if err != nil {
		return nil, fmt.Errorf("%q is not an integer", v)
	}
	if n <= 0 {
		return nil, fmt.Errorf("%q is not a positive integer", v)
	}
	ttl := int32(n)
	return &ttl, nil
}
