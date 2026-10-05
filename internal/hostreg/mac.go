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

// ResolveMAC completes a NodeView's MAC with the MAC chain of DESIGN.md section 4.7 step 3,
// in this order: the Node annotation (already applied by ViewNode, so a view that carries a
// MAC is returned unchanged), then NodeNetworkState, then Metal3.
//
// nns is the status.currentState of the Node's NodeNetworkState and hw the BareMetalHost
// inventory; either may be nil, meaning that source was not read (the Node has no
// NodeNetworkState, the policy lists no BareMetalHosts, or the API is absent). A source is
// consulted only when its MACSource switch says so (UseNMState, UseMetal3), so a caller
// that skipped the read need not also clear the flag. NodeNetworkState takes the
// interface named by src.NMStateInterface, or the one carrying view.Address; Metal3 takes
// the NIC carrying view.Address, or the bootMACAddress of the host whose inspected
// hostname is the Node name. The view's MACFrom records which source answered.
func ResolveMAC(view NodeView, src MACSource, nns map[string]interface{}, hw Metal3Hardware) NodeView {
	if view.MAC != "" {
		return view
	}
	if src.UseNMState() && nns != nil {
		if mac, _, ok := MACFromNodeNetworkState(nns, src.NMStateInterface, view.Address); ok {
			view.MAC, view.MACFrom = mac, MACFromNMState
			return view
		}
	}
	if src.UseMetal3() && hw != nil {
		if mac, _, ok := hw.MACForNode(view.Address, view.Ref.Name); ok {
			view.MAC, view.MACFrom = mac, MACFromMetal3
			return view
		}
	}
	return view
}
