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
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	extensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
)

// +kubebuilder:webhook:path=/mutate-extensions-api-v1alpha1-sandboxclaim,mutating=true,failurePolicy=fail,groups=extensions.agents.x-k8s.io,resources=sandboxclaims,verbs=create,versions=v1alpha1,name=msandboxclaim.kb.io,admissionReviewVersions=v1

// SandboxClaimAnnotator annotates SandboxClaims with a high-precision creation timestamp.
type SandboxClaimAnnotator struct {
	Decoder admission.Decoder
}

// Handle handles admission requests.
func (a *SandboxClaimAnnotator) Handle(_ context.Context, req admission.Request) admission.Response {
	claim := &extensionsv1alpha1.SandboxClaim{}
	err := a.Decoder.Decode(req, claim)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	if claim.Annotations == nil {
		claim.Annotations = make(map[string]string)
	}

	// Only add it if not already present
	if _, ok := claim.Annotations["agent-sandbox.kubernetes.io/created-at"]; !ok {
		claim.Annotations["agent-sandbox.kubernetes.io/created-at"] = time.Now().Format(time.RFC3339Nano)
	}

	marshaledClaim, err := json.Marshal(claim)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledClaim)
}

// InjectDecoder injects the decoder.
func (a *SandboxClaimAnnotator) InjectDecoder(d admission.Decoder) error {
	a.Decoder = d
	return nil
}
