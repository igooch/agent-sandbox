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
