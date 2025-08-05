// Copyright 2024 Humanitec
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

package command

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	"dario.cat/mergo"
	"github.com/score-spec/score-go/framework"
	scoreloader "github.com/score-spec/score-go/loader"
	scoreschema "github.com/score-spec/score-go/schema"
	scoretypes "github.com/score-spec/score-go/types"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/score-spec/score-andromeda/internal/convert"
	"github.com/score-spec/score-andromeda/internal/provisioners"
	"github.com/score-spec/score-andromeda/internal/state"
)

const (
	generateCmdOverridesFileFlag    = "overrides-file"
	generateCmdOverridePropertyFlag = "override-property"
	generateCmdImageFlag            = "image"
	generateCmdOutputFlag           = "output"
)

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Run the conversion from score file to output manifests",
	Args:  cobra.ArbitraryArgs,
	CompletionOptions: cobra.CompletionOptions{
		HiddenDefaultCmd: true,
	},
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		sd, ok, err := state.LoadStateDirectory(".")
		if err != nil {
			return fmt.Errorf("failed to load existing state directory: %w", err)
		} else if !ok {
			return fmt.Errorf("state directory does not exist, please run \"init\" first")
		}
		currentState := &sd.State

		if len(args) != 1 && (cmd.Flags().Lookup(generateCmdOverridesFileFlag).Changed || cmd.Flags().Lookup(generateCmdOverridePropertyFlag).Changed || cmd.Flags().Lookup(generateCmdImageFlag).Changed) {
			return fmt.Errorf("cannot use --%s, --%s, or --%s when 0 or more than 1 score files are provided", generateCmdOverridePropertyFlag, generateCmdOverridesFileFlag, generateCmdImageFlag)
		}

		slices.Sort(args)
		for _, arg := range args {
			var rawWorkload map[string]interface{}
			if raw, err := os.ReadFile(arg); err != nil {
				return fmt.Errorf("failed to read input score file: %s: %w", arg, err)
			} else if err = yaml.Unmarshal(raw, &rawWorkload); err != nil {
				return fmt.Errorf("failed to decode input score file: %s: %w", arg, err)
			}

			// apply overrides

			if v, _ := cmd.Flags().GetString(generateCmdOverridesFileFlag); v != "" {
				if err := parseAndApplyOverrideFile(v, generateCmdOverridesFileFlag, rawWorkload); err != nil {
					return err
				}
			}

			// Now read, parse, and apply any override properties to the score files
			if v, _ := cmd.Flags().GetStringArray(generateCmdOverridePropertyFlag); len(v) > 0 {
				for _, overridePropertyEntry := range v {
					if rawWorkload, err = parseAndApplyOverrideProperty(overridePropertyEntry, generateCmdOverridePropertyFlag, rawWorkload); err != nil {
						return err
					}
				}
			}

			// Ensure transforms are applied (be a good citizen)
			if changes, err := scoreschema.ApplyCommonUpgradeTransforms(rawWorkload); err != nil {
				return fmt.Errorf("failed to upgrade spec: %w", err)
			} else if len(changes) > 0 {
				for _, change := range changes {
					slog.Info(fmt.Sprintf("Applying backwards compatible upgrade %s", change))
				}
			}

			var workload scoretypes.Workload
			if err = scoreschema.Validate(rawWorkload); err != nil {
				return fmt.Errorf("invalid score file: %s: %w", arg, err)
			} else if err = scoreloader.MapSpec(&workload, rawWorkload); err != nil {
				return fmt.Errorf("failed to decode input score file: %s: %w", arg, err)
			}

			// Apply image override
			for containerName, container := range workload.Containers {
				if container.Image == "." {
					if v, _ := cmd.Flags().GetString(generateCmdImageFlag); v != "" {
						container.Image = v
						slog.Info(fmt.Sprintf("Set container image for container '%s' to %s from --%s", containerName, v, generateCmdImageFlag))
						workload.Containers[containerName] = container
					} else {
						return fmt.Errorf("failed to convert '%s' because container '%s' has no image and --image was not provided: %w", arg, containerName, err)
					}
				}
			}

			if currentState, err = currentState.WithWorkload(&workload, &arg, state.WorkloadExtras{}); err != nil {
				return fmt.Errorf("failed to add score file to project: %s: %w", arg, err)
			}
			slog.Info("Added score file to project", "file", arg)
			// 🔗 Link declared resources to workload
			if workloadName, ok := workload.Metadata["name"].(string); ok {
				for declaredID := range workload.Resources {
					resUID := framework.ResourceUid(declaredID)
					if res, found := currentState.Resources[resUID]; found {
						res.SourceWorkload = workloadName
						currentState.Resources[resUID] = res
						slog.Info(fmt.Sprintf("🔗 Linked declared resource '%s' to workload '%s'", resUID, workloadName))
					}
				}
			}

			var workloadName string
			if n, ok := workload.Metadata["name"].(string); ok {
				workloadName = n
			} else {
				return fmt.Errorf("workload metadata is missing 'name' field")
			}

			declaredResources := map[string]bool{}
			if rawResources, ok := rawWorkload["resources"].(map[string]interface{}); ok {
				for resID := range rawResources {
					declaredResources[resID] = true
				}
			}

			for resUID, res := range currentState.Resources {
				if res.SourceWorkload == workloadName {
					// Compare against declared resource names, not res.Id
					found := false
					for declaredID := range declaredResources {
						if resUID == framework.ResourceUid(declaredID) {
							found = true
							break
						}
					}
					if !found {
						slog.Info(fmt.Sprintf("Pruning undeclared resource '%s' from workload '%s'", resUID, res.SourceWorkload))
						delete(currentState.Resources, resUID)
					}
				}
			}

		}

		if len(currentState.Workloads) == 0 {
			return fmt.Errorf("project is empty, please add a score file")
		}

		if currentState, err = currentState.WithPrimedResources(); err != nil {
			return fmt.Errorf("failed to prime resources: %w", err)
		}

		slog.Info("Primed resources", "#workloads", len(currentState.Workloads), "#resources", len(currentState.Resources))

		outputManifests := make([]map[string]interface{}, 0)

		if currentState, err = provisioners.ProvisionResources(currentState); err != nil {
			return fmt.Errorf("failed to provision resources: %w", err)
		}

		for workloadName := range currentState.Workloads {
			resOutputs, _ := currentState.GetResourceOutputForWorkload(workloadName)
			slog.Info(fmt.Sprintf("Resolved outputs for workload '%s': %+v", workloadName, resOutputs))
		}

		for resUID, res := range currentState.Resources {
			slog.Info(fmt.Sprintf("Resource '%s' outputs: %v", resUID, res.Outputs))
		}

		sd.State = *currentState
		if err := sd.Persist(); err != nil {
			return fmt.Errorf("failed to persist state file: %w", err)
		}
		slog.Info("Persisted state file")

		// for workloadName := range currentState.Workloads {
		// 	if manifests, err := convert.Workload(currentState, workloadName); err != nil {
		// 		return fmt.Errorf("failed to convert workloads: %w", err)
		// 	} else {
		// 		outputManifests = append(outputManifests, manifests...)
		// 	}
		// 	slog.Info(fmt.Sprintf("Wrote manifest to manifests buffer for workload '%s'", workloadName))
		// }

		// for resUid, res := range currentState.Resources {
		// 	for _, obj := range res.Extras.Manifest {
		// 		if manifestMap, ok := obj.(map[string]interface{}); ok {
		// 			outputManifests = append(outputManifests, manifestMap)
		// 			slog.Info(fmt.Sprintf("Appended manifest from resource '%s'", resUid))
		// 		} else {
		// 			slog.Warn(fmt.Sprintf("Skipping non-map manifest in resource '%s'", resUid))
		// 		}
		// 	}
		// }
		for workloadName := range currentState.Workloads {
			resOutputs, _ := currentState.GetResourceOutputForWorkload(workloadName)
			slog.Info(fmt.Sprintf("Resolved outputs for workload '%s': %+v", workloadName, resOutputs))
		}
		for workloadName := range currentState.Workloads {
			manifests, err := convert.Workload(currentState, workloadName)
			
			if err != nil {
				return fmt.Errorf("failed to convert workload '%s': %w", workloadName, err)
			}
			for _, manifestMap := range manifests {
				mSig := buildManifestSignature(manifestMap)
				outputManifests = slices.DeleteFunc(outputManifests, func(other map[string]interface{}) bool {
					return buildManifestSignature(other) == mSig
				})
				outputManifests = append(outputManifests, manifestMap)
			}
			slog.Info(fmt.Sprintf("Wrote %d manifests to buffer for workload '%s'", len(manifests), workloadName))
		}

		resIds, _ := currentState.GetSortedResourceUids()
		for _, resUid := range resIds {
			res := currentState.Resources[resUid]
			for _, obj := range res.Extras.Manifest {
				if manifestMap, ok := obj.(map[string]interface{}); ok {
					mSig := buildManifestSignature(manifestMap)
					outputManifests = slices.DeleteFunc(outputManifests, func(other map[string]interface{}) bool {
						return buildManifestSignature(other) == mSig
					})
					outputManifests = append(outputManifests, manifestMap)
					slog.Info(fmt.Sprintf("Appended manifest for resource '%s'", resUid))
				} else {
					slog.Warn(fmt.Sprintf("Skipping non-map manifest in resource '%s'", resUid))
				}
			}
		}

		out := new(bytes.Buffer)
		for _, manifest := range outputManifests {
			out.WriteString("---\n")
			_ = yaml.NewEncoder(out).Encode(manifest)
		}
		v, _ := cmd.Flags().GetString(generateCmdOutputFlag)
		if v == "" {
			return fmt.Errorf("no output file specified")
		} else if v == "-" {
			_, _ = fmt.Fprint(cmd.OutOrStdout(), out.String())
		} else if err := os.WriteFile(v+".tmp", out.Bytes(), 0644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		} else if err := os.Rename(v+".tmp", v); err != nil {
			return fmt.Errorf("failed to complete writing output file: %w", err)
		} else {
			slog.Info(fmt.Sprintf("Wrote manifests to '%s'", v))
		}
		return nil
	},
}

func parseAndApplyOverrideFile(entry string, flagName string, spec map[string]interface{}) error {
	if raw, err := os.ReadFile(entry); err != nil {
		return fmt.Errorf("--%s '%s' is invalid, failed to read file: %w", flagName, entry, err)
	} else {
		slog.Info(fmt.Sprintf("Applying overrides from %s to workload", entry))
		var out map[string]interface{}
		if err := yaml.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("--%s '%s' is invalid: failed to decode yaml: %w", flagName, entry, err)
		} else if err := mergo.Merge(&spec, out, mergo.WithOverride); err != nil {
			return fmt.Errorf("--%s '%s' failed to apply: %w", flagName, entry, err)
		}
	}
	return nil
}

func parseAndApplyOverrideProperty(entry string, flagName string, spec map[string]interface{}) (map[string]interface{}, error) {
	parts := strings.SplitN(entry, "=", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("--%s '%s' is invalid, expected a =-separated path and value", flagName, entry)
	}
	if parts[1] == "" {
		slog.Info(fmt.Sprintf("Overriding '%s' in workload", parts[0]))
		after, err := framework.OverridePathInMap(spec, framework.ParseDotPathParts(parts[0]), true, nil)
		if err != nil {
			return nil, fmt.Errorf("--%s '%s' could not be applied: %w", flagName, entry, err)
		}
		return after, nil
	} else {
		var value interface{}
		if err := yaml.Unmarshal([]byte(parts[1]), &value); err != nil {
			return nil, fmt.Errorf("--%s '%s' is invalid, failed to unmarshal value as json: %w", flagName, entry, err)
		}
		slog.Info(fmt.Sprintf("Overriding '%s' in workload", parts[0]))
		after, err := framework.OverridePathInMap(spec, framework.ParseDotPathParts(parts[0]), false, value)
		if err != nil {
			return nil, fmt.Errorf("--%s '%s' could not be applied: %w", flagName, entry, err)
		}
		return after, nil
	}
}

func buildManifestSignature(n map[string]interface{}) string {
	apiVersion, _ := n["apiVersion"].(string)
	kind, _ := n["kind"].(string)
	metadata, _ := n["metadata"].(map[string]interface{})
	namespace, _ := metadata["namespace"].(string)
	name, _ := metadata["name"].(string)
	return fmt.Sprintf("%s/%s/%s/%s", apiVersion, kind, namespace, name)
}

func init() {
	generateCmd.Flags().StringP(generateCmdOutputFlag, "o", "manifests.yaml", "The output manifests file to write the manifests to")
	generateCmd.Flags().String(generateCmdOverridesFileFlag, "", "An optional file of Score overrides to merge in")
	generateCmd.Flags().StringArray(generateCmdOverridePropertyFlag, []string{}, "An optional set of path=key overrides to set or remove")
	generateCmd.Flags().String(generateCmdImageFlag, "", "An optional container image to use for any container with image == '.'")
	rootCmd.AddCommand(generateCmd)
}
