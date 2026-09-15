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

// This file implements DESIGN.md section 4.7 HostRegistrationPolicy pipeline steps 1 and 2:
// turning a Node into the NodeView the planner needs (address, opt-out, hostname, and the
// MAC when an annotation supplies one), plus NormalizeMAC, the MAC parsing shared with the
// NodeNetworkState reader in nmstate.go. Annotation parsing mirrors internal/ipalloc/
// service.go's ViewService.
package hostreg

import (
	"fmt"
	"net"
	"net/netip"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

// NormalizeMAC parses s as an IEEE 802 MAC-48 address in colon, hyphen or dot (Cisco)
// separated form (anything net.ParseMAC accepts), or as 12 bare hex digits with no
// separator at all, and returns it in lower-case colon form, e.g. "aa:bb:cc:dd:ee:ff".
// Longer forms net.ParseMAC also accepts (EUI-64, the 20-octet InfiniBand link-layer
// address) are rejected: this operator only ever registers Ethernet MACs.
func NormalizeMAC(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty MAC address")
	}

	parseable := s
	if isBareHex12(s) {
		parseable = expandBareHex12(s)
	}

	hw, err := net.ParseMAC(parseable)
	if err != nil {
		return "", fmt.Errorf("%q is not a valid MAC address", s)
	}
	if len(hw) != 6 {
		return "", fmt.Errorf("%q is not a 6-octet MAC-48 address", s)
	}
	return strings.ToLower(hw.String()), nil
}

// isBareHex12 reports whether s is exactly 12 hex digits with no separators, the one MAC
// form net.ParseMAC does not itself understand.
func isBareHex12(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, r := range s {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}

// expandBareHex12 turns 12 bare hex digits into net.ParseMAC's colon form.
func expandBareHex12(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(s[i : i+2])
	}
	return b.String()
}

// NodeAddress returns the first status.addresses entry of the matching AddressType, or ""
// when the Node has no address of that type (or node is nil).
func NodeAddress(node *corev1.Node, t AddressType) string {
	if node == nil {
		return ""
	}
	want := corev1.NodeAddressType(t)
	for _, a := range node.Status.Addresses {
		if a.Type == want {
			return a.Address
		}
	}
	return ""
}

// ViewNode reads a Node into the NodeView the planner needs, applying the register,
// address and hostname annotations at prefix (DefaultAnnotationPrefix when prefix is
// empty), and the MAC annotation at src.Key() (the fully qualified key from
// spec.macSource.nodeAnnotation, independent of prefix). Annotations are validated in the
// fixed order register, address, mac, hostname; the first invalid one stops further
// annotation parsing and is reported in Invalid, naming that annotation. MAC/MACFrom are
// only ever set here from the annotation ("annotation"); a NodeNetworkState-sourced MAC
// ("nmstate") is filled in afterwards by the caller, which is why this function does not
// itself decide when nmstate applies. node.Annotations may be nil; reading from a nil map
// is safe in Go and never panics.
func ViewNode(node *corev1.Node, t AddressType, src MACSource, prefix string) NodeView {
	if prefix == "" {
		prefix = DefaultAnnotationPrefix
	}

	v := NodeView{
		Ref:      NodeRef{Name: node.Name, UID: node.UID},
		Address:  NodeAddress(node, t),
		Hostname: node.Name,
	}

	annotations := node.Annotations

	if raw, ok := annotations[prefix+"/"+AnnotationRegister]; ok {
		switch strings.TrimSpace(raw) {
		case "true":
			// explicit opt-in; behaves the same as the annotation being absent.
		case "false":
			v.OptOut = true
		default:
			v.Invalid = invalidNodeAnnotationMsg(prefix+"/"+AnnotationRegister, raw, `must be "true" or "false"`)
			return v
		}
	}

	if raw, ok := annotations[prefix+"/"+AnnotationAddress]; ok {
		addr, err := netip.ParseAddr(strings.TrimSpace(raw))
		if err != nil {
			v.Invalid = invalidNodeAnnotationMsg(prefix+"/"+AnnotationAddress, raw, "not a valid IPv4 or IPv6 address")
			return v
		}
		v.Address = addr.String()
	}

	if raw, ok := annotations[src.Key()]; ok {
		mac, err := NormalizeMAC(raw)
		if err != nil {
			v.Invalid = invalidNodeAnnotationMsg(src.Key(), raw, "not a valid MAC address")
			return v
		}
		v.MAC = mac
		v.MACFrom = "annotation"
	}

	if raw, ok := annotations[prefix+"/"+AnnotationHostname]; ok {
		h := strings.ToLower(strings.TrimSpace(raw))
		if errs := validation.IsDNS1123Subdomain(h); len(errs) != 0 {
			v.Invalid = invalidNodeAnnotationMsg(prefix+"/"+AnnotationHostname, raw, "not a valid DNS name")
			return v
		}
		v.Hostname = h
	}

	return v
}

// invalidNodeAnnotationMsg formats an Invalid message naming the offending annotation:
// key is already the fully qualified annotation (prefix/name, or src.Key() for the MAC
// annotation).
func invalidNodeAnnotationMsg(key, raw, reason string) string {
	return fmt.Sprintf("%s: %q %s", key, raw, reason)
}
