// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package webhooks

import (
	"context"
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
)

func TestSandboxClaimAnnotator_Handle(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = extensionsv1alpha1.AddToScheme(scheme)
	decoder := admission.NewDecoder(scheme)

	annotator := &SandboxClaimAnnotator{Decoder: decoder}

	claim := &extensionsv1alpha1.SandboxClaim{}
	raw, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}

	// Create a mock request
	req := admission.Request{}
	req.Object.Raw = raw

	resp := annotator.Handle(context.Background(), req)

	if !resp.Allowed {
		t.Errorf("Expected allowed response, got: %v", resp.Result)
	}

	if len(resp.Patches) == 0 {
		t.Error("Expected patches to be returned, got none")
	}

	// Verify that the patch contains the annotation key
	found := false
	for _, p := range resp.Patches {
		if p.Operation == "add" && (p.Path == "/metadata" || p.Path == "/metadata/annotations") {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Expected patch to add annotations, but not found in %v", resp.Patches)
	}
}
