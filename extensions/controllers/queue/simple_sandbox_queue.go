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

import (
	"fmt"
	"sync"

	"k8s.io/client-go/util/workqueue"
)

type SimpleSandboxQueue struct {
	// sync.Map completely eliminates the global Mutex lock, allowing
	// hundreds of worker threads to lookup their queues concurrently.
	queues sync.Map
}

// TODO(vicentefb): Implement queue cleanup mechanism.
// We should remove the queue from the sync.Map when the corresponding
// SandboxWarmPool for a given template is deleted to prevent memory leaks.
type synchronizedQueue struct {
	getMu sync.Mutex
	queue workqueue.TypedInterface[SandboxKey]
}

func NewSimplePodQueue() *SimpleSandboxQueue {
	return &SimpleSandboxQueue{}
}

func (s *SimpleSandboxQueue) Add(templateHash string, pod SandboxKey) {
	// Only Add() is allowed to instantiate a new queue
	s.getOrCreateQueue(templateHash).queue.Add(pod)
}

func (s *SimpleSandboxQueue) Get(templateHash string) (SandboxKey, bool) {
	sQueue := s.getQueueIfExists(templateHash)
	if sQueue == nil {
		return SandboxKey{}, false
	}

	// The localized lock is still required to safely perform a non-blocking TryGet
	// without deadlocking the thread, but contention is now isolated per-template.
	sQueue.getMu.Lock()
	defer sQueue.getMu.Unlock()

	if sQueue.queue.Len() == 0 {
		return SandboxKey{}, false
	}
	pod, shutdown := sQueue.queue.Get()
	if shutdown {
		return SandboxKey{}, false
	}
	return pod, true
}

func (s *SimpleSandboxQueue) Done(templateHash string, pod SandboxKey) {
	if sQueue := s.getQueueIfExists(templateHash); sQueue != nil {
		sQueue.queue.Done(pod)
	}
}

func (s *SimpleSandboxQueue) Len(templateHash string) int {
	if sQueue := s.getQueueIfExists(templateHash); sQueue != nil {
		return sQueue.queue.Len()
	}
	return 0
}

func (s *SimpleSandboxQueue) Shutdown() {
	s.queues.Range(func(_, value any) bool {
		value.(*synchronizedQueue).queue.ShutDown()
		return true
	})
}

// getQueueIfExists returns the queue if it exists, or nil if it does not.
func (s *SimpleSandboxQueue) getQueueIfExists(templateHash string) *synchronizedQueue {
	if q, ok := s.queues.Load(templateHash); ok {
		return q.(*synchronizedQueue)
	}
	return nil
}

// getOrCreateQueue guarantees a queue is returned, creating it safely if missing.
func (s *SimpleSandboxQueue) getOrCreateQueue(templateHash string) *synchronizedQueue {
	// 1. FAST PATH: Lock-free read
	if q, ok := s.queues.Load(templateHash); ok {
		return q.(*synchronizedQueue)
	}

	// 2. SLOW PATH: Initialize if it doesn't exist.
	config := workqueue.TypedQueueConfig[SandboxKey]{
		Name: fmt.Sprintf("Pod Queue: %v", templateHash),
	}
	newQueue := &synchronizedQueue{
		queue: workqueue.NewTypedWithConfig(config),
	}

	actual, _ := s.queues.LoadOrStore(templateHash, newQueue)
	return actual.(*synchronizedQueue)
}
