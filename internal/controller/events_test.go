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
	"strings"
	"testing"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"

	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
)

func TestRuntimeEventfFormatsAndCutsTheNote(t *testing.T) {
	rec := events.NewFakeRecorder(8)
	rt := &uddisync.Runtime{Recorder: rec}
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "ns"}}
	next := func() string {
		t.Helper()
		select {
		case e := <-rec.Events:
			return e
		default:
			t.Fatal("no event was recorded")
			return ""
		}
	}

	// The fake keeps "<type> <reason> <note>", the same shape the legacy fake recorder had,
	// and a percent sign inside an argument is not read as a format verb.
	rt.Eventf(obj, nil, corev1.EventTypeNormal, uddisync.EventCreated, uddisync.ActionCreate,
		"created %s %s", "View", "100% sure")
	if got, want := next(), "Normal Created created View 100% sure"; got != want {
		t.Errorf("event = %q, want %q", got, want)
	}

	// events.k8s.io/v1 rejects a note over 1024 bytes, so a long Universal DDI error is cut
	// to the limit, on a character boundary.
	rt.Eventf(obj, nil, corev1.EventTypeWarning, uddisync.EventAPIError, uddisync.ActionReconcile,
		"%s", strings.Repeat("é", 800))
	note := strings.TrimPrefix(next(), "Warning APIError ")
	if len(note) != 1023 || !strings.HasSuffix(note, "...") || !utf8.ValidString(note) {
		t.Errorf("cut note has %d bytes, valid utf-8 %v, suffix ok %v",
			len(note), utf8.ValidString(note), strings.HasSuffix(note, "..."))
	}

	rt.Eventf(obj, nil, corev1.EventTypeWarning, uddisync.EventAPIError, uddisync.ActionReconcile,
		"%s", strings.Repeat("x", 3000))
	if note := strings.TrimPrefix(next(), "Warning APIError "); len(note) != 1024 || !strings.HasSuffix(note, "...") {
		t.Errorf("cut ascii note has %d bytes", len(note))
	}
}

func TestEventsAreOptional(t *testing.T) {
	obj := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "ns"}}

	// A controller built without a Runtime, or a Runtime without a recorder, records
	// nothing and does not panic.
	var none *uddisync.Runtime
	none.Eventf(obj, nil, corev1.EventTypeNormal, uddisync.EventCreated, uddisync.ActionCreate, "created")
	(&uddisync.Runtime{}).Eventf(obj, nil, corev1.EventTypeNormal, uddisync.EventCreated, uddisync.ActionCreate, "created")
	uddisync.Emit(nil, obj, nil, corev1.EventTypeNormal, uddisync.EventCreated, uddisync.ActionCreate, "created")
}
