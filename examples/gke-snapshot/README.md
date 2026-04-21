# Running Agent Sandbox In-Cluster Snapshot Tests on GKE

This guide provides a step-by-step walkthrough for deploying and testing the `agent-sandbox`
snapshot feature on GKE. It is self-contained and assumes you need to create all the necessary
configuration and test files. In this setup, the client code runs in a pod inside the cluster
simulating a tenant's agent orchestrator.

For more detailed information on GKE Pod Snapshots, see the official documentation:
https://docs.cloud.google.com/kubernetes-engine/docs/how-to/pod-snapshots

> [!WARNING]
> **API Version Notice**: The GKE Pod Snapshot API is currently using `v1alpha1` (e.g., in
> `PodSnapshotStorageConfig` and `PodSnapshotPolicy`) but is expected to transition to `v1` in the
> near future. This README and the example manifests may require updates when that happens.

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

The following files are available in this directory.

### 1. Snapshot Storage Config

File: [`snapshot_storage_config.yaml`](./snapshot_storage_config.yaml)

Update the bucket name with your project ID and apply it:

```bash
# Replace PROJECT_ID with your actual project ID
sed -i "s/PROJECT_ID/$PROJECT_ID/g" snapshot_storage_config.yaml

kubectl apply -f snapshot_storage_config.yaml
```

### 2. Snapshot Policy

File: [`snapshot_policy.yaml`](./snapshot_policy.yaml)

Apply it:

```bash
kubectl apply -f snapshot_policy.yaml
```

### 3. Sandbox Template

File: [`python-counter-template.yaml`](./python-counter-template.yaml)

Apply it:

```bash
kubectl apply -f python-counter-template.yaml
```

---

## Building the Test Client Image

### 1. Dockerfile

File: [`Dockerfile.client`](./Dockerfile.client)

### 2. Test Script

File: [`client_test.py`](./client_test.py)

### 3. Cloud Build Config

File: [`cloudbuild.yaml`](./cloudbuild.yaml)

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

# Run build
gcloud builds submit --config cloudbuild.yaml
```

#### Using Docker (Alternative to Cloud Build)

```bash
# One-time configuration for artifact registry
gcloud auth configure-docker $REGION-docker.pkg.dev

# Build and push the client image
docker build -t $REGION-docker.pkg.dev/$PROJECT_ID/agent-sandbox/sandbox-client:latest -f Dockerfile.client .
docker push $REGION-docker.pkg.dev/$PROJECT_ID/agent-sandbox/sandbox-client:latest
```

---

## Running the Test

### 1. Service Account and RBAC

File: [`client_sa.yaml`](./client_sa.yaml)

Apply it:

```bash
kubectl apply -f client_sa.yaml
```

### 2. Create Client Pod

File: [`client_pod.yaml`](./client_pod.yaml)

Update the image path with your region and project ID, and apply it:

```bash
# Replace REGION and PROJECT_ID with your actual values
sed -i "s/REGION/$REGION/g" client_pod.yaml
sed -i "s/PROJECT_ID/$PROJECT_ID/g" client_pod.yaml

kubectl apply -f client_pod.yaml
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
