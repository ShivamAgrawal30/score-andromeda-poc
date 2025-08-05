// // Copyright 2024 Humanitec
// //
// // Licensed under the Apache License, Version 2.0 (the "License");
// // you may not use this file except in compliance with the License.
// // You may obtain a copy of the License at
// //
// //     http://www.apache.org/licenses/LICENSE-2.0
// //
// // Unless required by applicable law or agreed to in writing, software
// // distributed under the License is distributed on an "AS IS" BASIS,
// // WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// // See the License for the specific language governing permissions and
// // limitations under the License.

// package provisioners

// import (
// 	"fmt"
// 	"maps"

// 	"github.com/score-spec/score-go/framework"

// 	"github.com/score-spec/score-andromeda/internal/state"
// )

// func ProvisionResources(currentState *state.State) (*state.State, error) {
// 	out := currentState
// 	var provisioners = map[string]func(string, scoretypes.ResourceSpec, *state.State) ([]interface{}, error){}

// 	// provision in sorted order
// 	orderedResources, err := currentState.GetSortedResourceUids()
// 	if err != nil {
// 		return nil, fmt.Errorf("failed to determine sort order for provisioning: %w", err)
// 	}

// 	out.Resources = maps.Clone(out.Resources)
// 	for _, resUid := range orderedResources {
// 		resState := out.Resources[resUid]

// 		var params map[string]interface{}
// 		if len(resState.Params) > 0 {
// 			resOutputs, err := out.GetResourceOutputForWorkload(resState.SourceWorkload)
// 			if err != nil {
// 				return nil, fmt.Errorf("%s: failed to find resource params for resource: %w", resUid, err)
// 			}
// 			sf := framework.BuildSubstitutionFunction(out.Workloads[resState.SourceWorkload].Spec.Metadata, resOutputs)
// 			rawParams, err := framework.Substitute(resState.Params, sf)
// 			if err != nil {
// 				return nil, fmt.Errorf("%s: failed to substitute params for resource: %w", resUid, err)
// 			}
// 			params = rawParams.(map[string]interface{})
// 		}
// 		resState.Params = params

// 		// ==========================================================================================
// 		// TODO: HERE IS WHERE YOU WOULD USE THE RESOURCE TYPE, CLASS, ID, AND PARAMS TO PROVISION IT
// 		// ==========================================================================================

// 		provisioner, ok := provisioners[resState.Type]
//         if !ok {
//             return nil, fmt.Errorf("%s: no provisioner registered for type '%s'", resUid, resState.Type)
//         }

//         manifests, err := provisioner(resState.Name, resState.Spec, out)
//         if err != nil {
//             return nil, fmt.Errorf("%s: provisioner failed: %w", resUid, err)
//         }

//         resState.Manifest = manifests
//         resState.Outputs = map[string]interface{}{} // you can optionally wire outputs later
//         out.Resources[resUid] = resState

// 		// resState.Outputs = map[string]interface{}{}
// 		// out.Resources[resUid] = resState
// 	}

// 	return out, nil
// }

package provisioners

import (
	"fmt"
	"maps"

	// "strings"

	"github.com/score-spec/score-go/framework"
	scoretypes "github.com/score-spec/score-go/types"

	"github.com/score-spec/score-andromeda/internal/state"
)

type provisionerFunc func(name string, resource scoretypes.Resource, st *state.State) ([]interface{}, error)

var provisioners = map[string]provisionerFunc{}

func register(kind string, fn provisionerFunc) {
	provisioners[kind] = fn
}

func ProvisionResources(currentState *state.State) (*state.State, error) {
	out := currentState

	orderedResources, err := currentState.GetSortedResourceUids()
	if err != nil {
		return nil, fmt.Errorf("failed to determine sort order for provisioning: %w", err)
	}

	out.Resources = maps.Clone(out.Resources)
	for _, resUid := range orderedResources {
		resState := out.Resources[resUid]

		// Debug print before setting SourceWorkload
		fmt.Printf("[DEBUG] Before: resUid=%s, resState.Id=%s, resState.Type=%s, SourceWorkload='%s'\n", resUid, resState.Id, resState.Type, resState.SourceWorkload)
		// Set SourceWorkload to the workload name if not already set
		// if resState.SourceWorkload == "" {
		// 	// Try to infer workload name from resUid (format: <workload>#<resource>)
		// 	parts := strings.SplitN(string(resUid), "#", 2)
		// 	if len(parts) > 0 {
		// 		resState.SourceWorkload = parts[0]
		// 	}
		// }
		// Debug print after setting SourceWorkload
		fmt.Printf("[DEBUG] After: resUid=%s, resState.Id=%s, resState.Type=%s, SourceWorkload='%s'\n", resUid, resState.Id, resState.Type, resState.SourceWorkload)

		// Resolve substitution parameters if any
		var params map[string]interface{}
		if len(resState.Params) > 0 {
			resOutputs, err := out.GetResourceOutputForWorkload(resState.SourceWorkload)
			if err != nil {
				return nil, fmt.Errorf("%s: failed to find resource params for substitution: %w", resUid, err)
			}
			sf := framework.BuildSubstitutionFunction(out.Workloads[resState.SourceWorkload].Spec.Metadata, resOutputs)
			rawParams, err := framework.Substitute(resState.Params, sf)
			if err != nil {
				return nil, fmt.Errorf("%s: failed to substitute resource params: %w", resUid, err)
			}
			params = rawParams.(map[string]interface{})
		}
		resState.Params = params

		// Reconstruct original Score resource
		scoreRes := scoretypes.Resource{
			Type:     resState.Type,
			Class:    &resState.Class,
			Id:       &resState.Id,
			Metadata: resState.Metadata,
			Params:   resState.Params,
		}

		// Provision using registered handler
		provisioner, ok := provisioners[resState.Type]
		if !ok {
			return nil, fmt.Errorf("%s: no provisioner registered for type '%s'", resUid, resState.Type)
		}

		manifests, err := provisioner(resState.Id, scoreRes, out)
		if err != nil {
			return nil, fmt.Errorf("%s: provisioner for type '%s' failed: %w", resUid, resState.Type, err)
		}

		// Store generated manifest in Extras
		resState.Outputs = map[string]interface{}{}
		resState.Extras = state.ResourceExtras{
			Manifest: manifests,
		}

		// If this is a SQLDatabase or ObjectStorage, extract the secret name and store in Params
		for _, m := range manifests {
			if manifestMap, ok := m.(map[string]interface{}); ok {
				if kind, ok := manifestMap["kind"].(string); ok && (kind == "SQLDatabase" || kind == "ObjectStorage") {
					if spec, ok := manifestMap["spec"].(map[string]interface{}); ok {
						if wcsr, ok := spec["writeConnectionSecretToRef"].(map[string]interface{}); ok {
							if secretName, ok := wcsr["name"].(string); ok && secretName != "" {
								if resState.Params == nil {
									resState.Params = map[string]interface{}{}
								}
								resState.Params["connectionSecretName"] = secretName
							}
						}
					}
				}
			}
		}

		out.Resources[resUid] = resState
	}

	return out, nil
}
