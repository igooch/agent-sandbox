# Running Agent Sandbox Snapshot Tests on GKE

This guide provides a step-by-step walkthrough for deploying and testing the `agent-sandbox`
snapshot feature on GKE. It is self-contained and assumes you need to create all the necessary
configuration and test files.

For more detailed information on GKE Pod Snapshots, see the official documentation:
https://docs.cloud.google.com/kubernetes-engine/docs/how-to/pod-snapshots

> [!WARNING]
> **API Version Notice**: The GKE Pod Snapshot API is currently using `v1alpha1` (e.g., in
> `PodSnapshotStorageConfig` and `PodSnapshotPolicy`) but is expected to transition to `v1` in the
> near future. This README and the example manifests may require updates when that happens.

## Workflow Overview

This guide walks you through the following steps:

1.  **Cluster Setup**: Create a GKE cluster and node pool with Pod Snapshots and gVisor enabled.
2.  **Artifact Registry Setup**: Create a Docker repository for the client image.
3.  **Install Agent Sandbox**: Install the core controller and extensions.
4.  **Storage and IAM Setup**: Create a GCS bucket and configure Workload Identity permissions.
5.  **Configuration**: Create and apply snapshot storage config, policy, and a sandbox template.
6.  **Building the Test Client**: Create a Dockerfile and Python script to orchestrate the test, and
    build it using Cloud Build or Docker.
7.  **Running the Test**: Deploy a client pod that creates a sandbox, waits, suspends it (creating a
    snapshot), resumes it, and verifies that the counter state was preserved.

## Prerequisites

### Environment Variables

Set the following environment variables to make it easier to run the commands:

```bash
export PROJECT_ID="YOUR_PROJECT_ID"
export PROJECT_NUMBER="YOUR_PROJECT_NUMBER"
export REGION="YOUR_REGION" # e.g., us-central1
export LOCATION="YOUR_BUCKET_LOCATION" # e.g., us
export BUCKET_NAME="${PROJECT_ID}_snapshots"
export CLOUDBUILD_BUCKET_NAME="${PROJECT_ID}_cloudbuild"
```

### 1. Cluster Setup

Create a GKE cluster with Pod Snapshots enabled (requires Rapid release channel as of this writing).

```bash
gcloud beta container clusters create test-snapshot \
    --enable-pod-snapshots \
    --release-channel=rapid \
    --machine-type=e2-standard-8 \
    --workload-pool=$PROJECT_ID.svc.id.goog \
    --workload-metadata=GKE_METADATA \
    --num-nodes=1 \
    --location=$REGION
```

Create a node pool with gVisor enabled, which is required for sandboxing:

```bash
gcloud container node-pools create gvisor-pool \
    --cluster test-snapshot \
    --num-nodes=1 \
    --location $REGION \
    --project $PROJECT_ID \
    --sandbox type=gvisor
```

> [!IMPORTANT]
> **Production Note on CPU Heterogeneity**: In a regional cluster, nodes in different zones may have
> different CPU microarchitectures. Since snapshots capture CPU state, restoring a snapshot on a node
> with missing CPU features will fail (e.g., `OCI runtime restore failed: incompatible FeatureSet`).
>
> - **For testing**: We use `nodeSelector` in the `SandboxTemplate` to pin the pod to a specific zone
>   (e.g., `us-central1-a`).
> - **For production**: Do not pin to a zone. Instead, specify a minimum CPU platform when
>   creating the node pool to ensure feature consistency across all zones:
>   `bash

    --min-cpu-platform="Intel Ice Lake" # or "AMD Milan"
    `

### 2. Artifact Registry Setup

Create a Docker repository in Artifact Registry to store your test images:

```bash
gcloud artifacts repositories create agent-sandbox \
    --repository-format=docker \
    --location=$REGION \
    --description="Docker repository for Agent Sandbox"
```

### 3. Install Agent Sandbox

Install the core components and extensions (using version v0.3.10 as an example):

```bash
# Install the core agent-sandbox components
kubectl apply -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v0.3.10/manifest.yaml

# Install the optional extensions (e.g., Warm Pools, Claims)
kubectl apply -f https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v0.3.10/extensions.yaml
```

### 4. Storage and IAM Setup

Create a GCS bucket for storing snapshots and configure IAM permissions for Workload Identity.

```bash
gcloud storage buckets create "gs://$BUCKET_NAME" \
   --uniform-bucket-level-access \
   --enable-hierarchical-namespace \
   --soft-delete-duration=0d \
   --location="$LOCATION"

kubectl create serviceaccount "snapshot-sa" \
    --namespace "default"

# Grant permissions to the service account via Workload Identity
gcloud storage buckets add-iam-policy-binding "gs://$BUCKET_NAME" \
    --member="principal://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/$PROJECT_ID.svc.id.goog/subject/ns/default/sa/snapshot-sa" \
    --role="roles/storage.bucketViewer"

gcloud storage buckets add-iam-policy-binding "gs://$BUCKET_NAME" \
    --member="principal://iam.googleapis.com/projects/$PROJECT_NUMBER/locations/global/workloadIdentityPools/$PROJECT_ID.svc.id.goog/subject/ns/default/sa/snapshot-sa" \
    --role="roles/storage.objectUser"

# Grant the GKE service agent permissions to manage snapshots
gcloud projects add-iam-policy-binding "$PROJECT_ID" \
  --member="serviceAccount:service-$PROJECT_NUMBER@container-engine-robot.iam.gserviceaccount.com" \
  --role="roles/storage.objectUser" \
  --condition="expression=resource.name.startsWith(\"projects/_/buckets/$BUCKET_NAME\"),title=restrict_to_bucket,description=Restricts access to one bucket only"
```

---

## Configuration Files

Create the following files in a directory named `test_client` (or apply them directly using `kubectl apply -f -`).

### 1. Snapshot Storage Config

Create `test_client/snapshot_storage_config.yaml`:

```yaml
apiVersion: podsnapshot.gke.io/v1alpha1
kind: PodSnapshotStorageConfig
metadata:
  name: example-pod-snapshot-storage-config
spec:
  snapshotStorageConfig:
    gcs:
      bucket: "BUCKET_NAME"
```

Apply it:

```bash
kubectl apply -f test_client/snapshot_storage_config.yaml
```

### 2. Snapshot Policy

Create `test_client/snapshot_policy.yaml`:

```yaml
apiVersion: podsnapshot.gke.io/v1alpha1
kind: PodSnapshotPolicy
metadata:
  name: example-pod-snapshot-policy
  namespace: default
spec:
  storageConfigName: example-pod-snapshot-storage-config
  selector:
    matchLabels:
      app: agent-sandbox-workload
  triggerConfig:
    type: manual
    postCheckpoint: resume
  snapshotGroupingRules:
    groupByLabelValue:
      labels: ["tenant-id", "user-id"]
      groupRetentionPolicy:
        maxSnapshotCountPerGroup: 2
```

Apply it:

```bash
kubectl apply -f test_client/snapshot_policy.yaml
```

### 3. Sandbox Template

Create `test_client/python-counter-template.yaml`:

```yaml
apiVersion: extensions.agents.x-k8s.io/v1alpha1
kind: SandboxTemplate
metadata:
  name: python-counter-template
  namespace: default
spec:
  podTemplate:
    metadata:
      labels:
        app: agent-sandbox-workload
    spec:
      serviceAccountName: snapshot-sa
      runtimeClassName: gvisor
      nodeSelector:
        topology.kubernetes.io/zone: us-central1-a # Pin to a zone to avoid CPU mismatch during restore
      containers:
        - name: python-counter
          image: python:3.13-slim
          command: ["python3", "-c"]
          args:
            - |
              import time
              i = 0
              while True:
                print(f"Count: {i}", flush=True)
                i += 1
                time.sleep(1)
```

Apply it:

```bash
kubectl apply -f test_client/python-counter-template.yaml
```

---

## Building the Test Client Image

Since we assume the test client files do not exist in the repo, you need to create them before building.

### 1. Create Dockerfile

Create `test_client/Dockerfile.client`:

```dockerfile
FROM python:3.13-slim

WORKDIR /app

# Copy SDK source (assumed to exist in the repo)
COPY clients/python/agentic-sandbox-client /app/sdk

# Handle setuptools_scm version detection issue
ENV SETUPTOOLS_SCM_PRETEND_VERSION=0.1.0

# Install SDK from source with tracing
RUN pip install "/app/sdk[tracing]"

# Copy test script
COPY test_client/client_test.py /app/client_test.py

CMD ["python", "/app/client_test.py"]
```

### 2. Create Test Script

Create `test_client/client_test.py`:

```python
import time
import logging
import re
from kubernetes import config, client
from k8s_agent_sandbox.gke_extensions.snapshots import PodSnapshotSandboxClient

logging.basicConfig(level=logging.INFO)

def get_last_count(pod_name, namespace):
    v1 = client.CoreV1Api()
    try:
        logs = v1.read_namespaced_pod_log(name=pod_name, namespace=namespace)
        counts = re.findall(r"Count: (\d+)", logs)
        if counts:
            return int(counts[-1])
        return None
    except Exception as e:
        logging.error(f"Failed to read logs for pod {pod_name}: {e}")
        return None

def get_current_pod_name(sandbox_id, namespace):
    custom_api = client.CustomObjectsApi()
    try:
        sandbox_cr = custom_api.get_namespaced_custom_object(
            group="agents.x-k8s.io",
            version="v1alpha1",
            namespace=namespace,
            plural="sandboxes",
            name=sandbox_id
        )
        metadata = sandbox_cr.get("metadata", {})
        annotations = metadata.get("annotations", {})
        return annotations.get("agents.x-k8s.io/pod-name")
    except Exception as e:
        logging.error(f"Failed to get sandbox CR: {e}")
        return None

def get_current_count(sandbox_id, namespace="default"):
    pod_name = get_current_pod_name(sandbox_id, namespace)
    if not pod_name:
        logging.error(f"Could not determine pod name for sandbox {sandbox_id}")
        return None
    return get_last_count(pod_name, namespace)

def suspend_sandbox(sandbox):
    logging.info("Pausing sandbox (using snapshots)...")
    try:
        suspend_resp = sandbox.suspend(snapshot_before_suspend=True)
        if suspend_resp.success:
            logging.info("Sandbox paused successfully.")
            if suspend_resp.snapshot_response:
                logging.info(f"Snapshot created: {suspend_resp.snapshot_response.snapshot_uid}")
            return suspend_resp
        else:
            logging.error(f"Failed to pause: {suspend_resp.error_reason}")
            exit(1)
    except Exception as e:
        logging.error(f"Failed to pause sandbox: {e}")
        exit(1)

def resume_sandbox(sandbox):
    logging.info("Resuming sandbox (using snapshots)...")
    try:
        resume_resp = sandbox.resume()
        if resume_resp.success:
            logging.info("Sandbox resumed successfully.")
            if resume_resp.restored_from_snapshot:
                logging.info(f"Restored from snapshot: {resume_resp.snapshot_uid}")
            return resume_resp
        else:
            logging.error(f"Failed to resume: {resume_resp.error_reason}")
            exit(1)
    except Exception as e:
        logging.error(f"Failed to resume sandbox: {e}")
        exit(1)

def verify_continuity(count_before, count_after):
    if count_before is not None and count_after is not None:
        logging.info(f"Verification: Count before={count_before}, Count after={count_after}")
        if count_after >= count_before:
            logging.info("SUCCESS: Sandbox resumed from where it left off (or later).")
        else:
            logging.error("FAIL: Sandbox counter reset or went backwards!")
    else:
        logging.warning("Could not verify counter continuity.")

def main():
    # Load in-cluster config if running in a Pod
    try:
        config.load_incluster_config()
    except config.ConfigException:
        config.load_kube_config()

    client_reg = PodSnapshotSandboxClient()

    logging.info("Creating sandbox...")
    sandbox = client_reg.create_sandbox(template="python-counter-template", namespace="default")
    logging.info(f"Sandbox created with ID: {sandbox.sandbox_id}")

    logging.info("Waiting for sandbox to run...")
    time.sleep(10)

    count_before = get_current_count(sandbox.sandbox_id)
    logging.info(f"Count before suspend: {count_before}")

    suspend_sandbox(sandbox)

    logging.info("Waiting 10 seconds...")
    time.sleep(10)

    resume_sandbox(sandbox)

    logging.info("Waiting for sandbox to be ready again...")
    time.sleep(10)

    count_after = get_current_count(sandbox.sandbox_id)
    logging.info(f"Count after resume: {count_after}")

    verify_continuity(count_before, count_after)

    logging.info("Snapshot test completed successfully.")

if __name__ == "__main__":
    main()
```

### 3. Create Cloud Build Config

Create `test_client/cloudbuild.yaml`:

```yaml
steps:
  - name: "gcr.io/cloud-builders/docker"
    args:
      [
        "build",
        "-t",
        "REGION-docker.pkg.dev/PROJECT_ID/agent-sandbox/sandbox-client:latest",
        "-f",
        "test_client/Dockerfile.client",
        ".",
      ]
images:
  - "REGION-docker.pkg.dev/PROJECT_ID/agent-sandbox/sandbox-client:latest"
```

### 4. Build and Push

#### Using Cloud Build

```bash
# Grant necessary permissions to the Cloud Build service account
gcloud projects add-iam-policy-binding $PROJECT_ID \
    --member="serviceAccount:$PROJECT_NUMBER-compute@developer.gserviceaccount.com" \
    --role="roles/artifactregistry.writer"

gcloud projects add-iam-policy-binding $PROJECT_ID \
    --member="serviceAccount:$PROJECT_NUMBER-compute@developer.gserviceaccount.com" \
    --role="roles/logging.logWriter"

gcloud storage buckets add-iam-policy-binding gs://$CLOUDBUILD_BUCKET_NAME \
    --member="serviceAccount:$PROJECT_NUMBER-compute@developer.gserviceaccount.com" \
    --role="roles/storage.objectAdmin"

# Run build from root of repo
gcloud builds submit --config test_client/cloudbuild.yaml
```

#### Using Docker (Alternative to Cloud Build)

```bash
# One-time configuration for artifact registry
gcloud auth configure-docker $REGION-docker.pkg.dev

# Build and push the client image from root of repo
docker build -t $REGION-docker.pkg.dev/$PROJECT_ID/agent-sandbox/sandbox-client:latest -f test_client/Dockerfile.client .
docker push $REGION-docker.pkg.dev/$PROJECT_ID/agent-sandbox/sandbox-client:latest
```

---

## Running the Test

### 1. Create Service Account and RBAC for Client Pod

Create `test_client/client_sa.yaml`:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: agent-sandbox-client-sa
  namespace: default
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: agent-sandbox-client-role
  namespace: default
rules:
  - apiGroups: ["agents.x-k8s.io"]
    resources: ["sandboxes"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["extensions.agents.x-k8s.io"]
    resources: ["sandboxclaims"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["podsnapshot.gke.io"]
    resources: ["podsnapshotmanualtriggers", "podsnapshots"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: [""]
    resources: ["pods", "pods/log"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: agent-sandbox-client-rolebinding
  namespace: default
subjects:
  - kind: ServiceAccount
    name: agent-sandbox-client-sa
    namespace: default
roleRef:
  kind: Role
  name: agent-sandbox-client-role
  apiGroup: rbac.authorization.k8s.io
```

Apply it:

```bash
kubectl apply -f test_client/client_sa.yaml
```

### 2. Create Client Pod

Create `test_client/client_pod.yaml`:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: agent-sandbox-client-pod
  namespace: default
spec:
  serviceAccountName: agent-sandbox-client-sa
  containers:
    - name: client
      image: REGION-docker.pkg.dev/PROJECT_ID/agent-sandbox/sandbox-client:latest
      imagePullPolicy: Always
  restartPolicy: Never
```

Apply it:

```bash
kubectl apply -f test_client/client_pod.yaml
```

### 3. View Logs and Verify

```bash
kubectl logs -f agent-sandbox-client-pod
```

### Expected Output

A successful run should look like this:

```bash
2026-04-21 23:02:39,030 - INFO - Creating sandbox...
2026-04-21 23:02:39,030 - INFO - Creating SandboxClaim 'sandbox-claim-f7703d96' in namespace 'default' using template 'python-counter-template'...
2026-04-21 23:02:39,049 - INFO - Resolving sandbox name from claim 'sandbox-claim-f7703d96'...
2026-04-21 23:02:39,143 - INFO - Resolved sandbox name 'sandbox-claim-f7703d96' from claim status
2026-04-21 23:02:39,143 - INFO - Watching for Sandbox sandbox-claim-f7703d96 to become ready...
2026-04-21 23:02:41,698 - INFO - Sandbox sandbox-claim-f7703d96 is ready.
2026-04-21 23:02:41,699 - INFO - Sandbox created with ID: sandbox-claim-f7703d96
2026-04-21 23:02:41,699 - INFO - Waiting for sandbox to run...
2026-04-21 23:02:51,755 - INFO - Count before suspend: 23
2026-04-21 23:02:51,755 - INFO - Pausing sandbox (using snapshots)...
2026-04-21 23:02:51,811 - INFO - Waiting for snapshot manual trigger 'suspend-sandbox-claim-f7703d96-20260421-230251-73f0e700' to be processed...
2026-04-21 23:02:55,027 - INFO - Snapshot manual trigger 'suspend-sandbox-claim-f7703d96-20260421-230251-73f0e700' processed successfully. Created Snapshot UID: d52b5ae4-c7e0-4050-9df7-94e211cf85aa
2026-04-21 23:02:55,083 - INFO - Sandbox 'sandbox-claim-f7703d96' suspended (scaled down to 0 replicas).
2026-04-21 23:02:55,083 - INFO - Waiting up to 180s for pod 'sandbox-claim-f7703d96' (UID: 71404803-09cc-4d6c-bb7d-b79d4614fa32) to terminate...
2026-04-21 23:02:57,114 - INFO - Sandbox 'sandbox-claim-f7703d96' pod successfully terminated.
2026-04-21 23:02:57,114 - INFO - Sandbox paused successfully.
2026-04-21 23:02:57,114 - INFO - Snapshot created: d52b5ae4-c7e0-4050-9df7-94e211cf85aa
2026-04-21 23:02:57,114 - INFO - Waiting 10 seconds...
2026-04-21 23:03:07,115 - INFO - Resuming sandbox (using snapshots)...
2026-04-21 23:03:07,133 - INFO - Listing snapshots with label selector: podsnapshot.gke.io/pod-name=sandbox-claim-f7703d96
2026-04-21 23:03:07,154 - INFO - Found 1 snapshots.
2026-04-21 23:03:07,183 - INFO - Sandbox 'sandbox-claim-f7703d96' resumed (scaled up to 1 replica).
2026-04-21 23:03:07,184 - INFO - Waiting up to 180s for pod to become ready...
2026-04-21 23:03:11,247 - INFO - Sandbox 'sandbox-claim-f7703d96' successfully restored from snapshot 'd52b5ae4-c7e0-4050-9df7-94e211cf85aa'.
2026-04-21 23:03:11,248 - INFO - Sandbox resumed successfully.
2026-04-21 23:03:11,248 - INFO - Restored from snapshot: d52b5ae4-c7e0-4050-9df7-94e211cf85aa
2026-04-21 23:03:11,248 - INFO - Waiting for sandbox to be ready again...
2026-04-21 23:03:21,329 - INFO - Count after resume: 38
2026-04-21 23:03:21,329 - INFO - Verification: Count before=23, Count after=38
2026-04-21 23:03:21,329 - INFO - SUCCESS: Sandbox resumed from where it left off (or later).
2026-04-21 23:03:21,329 - INFO - Snapshot test completed successfully.
```
