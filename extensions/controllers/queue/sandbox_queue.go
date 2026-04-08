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

package queue

import "k8s.io/apimachinery/pkg/types"

// SandboxKey uniquely identifies a sandbox in the queue using its namespace and name.
type SandboxKey types.NamespacedName

// SandboxQueue defines the interface for managing a high-throughput,
// lock-free queue of adoptable warm pool sandboxes.
type SandboxQueue interface {
	// Add inserts a new adoptable sandbox candidate into the queue for a specific template.
	Add(templateHash string, pod SandboxKey)

	// Get retrieves and removes a sandbox candidate from the queue.
	// Returns false if the queue is empty or does not exist.
	Get(templateHash string) (SandboxKey, bool)

	// Done marks the processing of a sandbox candidate as complete in the underlying workqueue.
	Done(templateHash string, pod SandboxKey)

	// Len returns the number of currently available sandboxes for a specific template.
	Len(templateHash string) int

	// Shutdown safely closes all underlying queues to prevent further processing.
	Shutdown()
}
