# Score Andromeda POC

This project demonstrates how to convert [Score YAML](https://score.dev/) workload definitions into Knative Service manifests, with secure environment variable wiring and resource provisioning for cloud-native applications.

## Features

- **Knative Service Generation**: Converts Score YAML to Knative Service manifests (no Kubernetes Service generated).
- **Cluster-Local Support**: Adds `serving.knative.dev/visibility: cluster-local` label if `applyClusterLocal` is set.
- **Port Mapping**: Automatically extracts and maps service ports from Score YAML to Knative container ports.
- **Secret-Aware Environment Variables**: Wires up environment variables from provisioned resources (e.g., Postgres, S3) using Kubernetes `secretKeyRef`.
- **Resource Provisioners**: Supports custom resources like `SQLDatabase` and `ObjectStorage` with secret output wiring.

## Usage

1. **Prepare your Score YAML**
   - Define your workload, ports, and resources in `score.yaml`.

2. **Run the Converter**
   - Use the CLI or call the main function in `internal/convert/workloads.go` to generate manifests:

    ```sh
    make build
    ```

    or

    ```sh
    go run ./cmd/score-andromeda/main.go -f score.yaml > manifests.yaml
    ```

3. **Review the Output**
   - The generated `manifests.yaml` will contain:
     - Knative Service manifest with correct ports and env vars
     - Resource manifests (e.g., SQLDatabase, ObjectStorage)
     - Secret references for environment variables

4. **Apply to Your Cluster**
   - Deploy the generated manifests to your Kubernetes/Knative cluster:

     ```sh
     kubectl apply -f manifests.yaml
     ```

## Example

Given a `score.yaml` with a Postgres and S3 resource, the generated Knative Service will have environment variables like:

```yaml
- name: PG_CONNECTION_STRING
  value: postgresql://$(USERNAME):$(PASSWORD)@$(HOST):$(PORT)/$(DATABASE)
- name: BUCKET_ENDPOINT
  valueFrom:
    secretKeyRef:
      name: bucket-tutorial-app-bucket-connection-creds
      key: endpoint
```

## Secret-Aware Substitution

The function `buildSecretAwareSubstitutionFn` replaces variables in environment values with references to Kubernetes secrets. For example:

- Input: `postgresql://$(USERNAME):$(PASSWORD)@$(HOST):$(PORT)/$(DATABASE)`
- Output: `postgresql://$(__ref_abc):$(__ref_xyz)@$(__ref_host):$(__ref_port)/$(__ref_db)`
  (with each `__ref_*` wired to the correct `secretKeyRef`)

## Project Structure

- `cmd/score-andromeda/main.go` — CLI entrypoint
- `internal/convert/workloads.go` — Main conversion logic
- `internal/provisioners/` — Resource provisioners (e.g., S3, Postgres)
- `manifests.yaml` — Example output
- `score.yaml` — Example input

## Requirements

- Go 1.20+
- Kubernetes cluster with Knative Serving installed

## License

MIT
