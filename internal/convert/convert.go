package convert

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/pkg/errors"
	"github.com/score-spec/score-andromeda/internal/state"
	"github.com/score-spec/score-go/framework"
	scoretypes "github.com/score-spec/score-go/types"

	// "encoding/base64"
	// "hash/fnv"

	coreV1 "k8s.io/api/core/v1"
	// "k8s.io/apimachinery/pkg/api/resource"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	servingv1 "knative.dev/serving/pkg/apis/serving/v1"
	"sigs.k8s.io/yaml"
)

func Workload(currentState *state.State, workloadName string) ([]map[string]interface{}, error) {
	spec := currentState.Workloads[workloadName].Spec
	resOutputs, err := currentState.GetResourceOutputForWorkload(workloadName)
	if err != nil {
		return nil, fmt.Errorf("failed to generate outputs: %w", err)
	}
	// Update spec.Resources with Params from currentState.Resources
	resources := maps.Clone(spec.Resources)
	for resName, res := range resources {
		resUid := framework.NewResourceUid(workloadName, resName, res.Type, res.Class, res.Id)
		resState, ok := currentState.Resources[resUid]
		if !ok {
			return nil, fmt.Errorf("workload '%s': resource '%s' (%s) is not primed", workloadName, resName, resUid)
		}
		res.Params = resState.Params
		resources[resName] = res
	}
	spec.Resources = resources

	containers := spec.Containers
	containerNames := make([]string, 0, len(containers))
	for name := range containers {
		containerNames = append(containerNames, name)
	}
	slices.Sort(containerNames)

	hasDns := false
	hasRoute := false
	if spec.Resources != nil {
		_, hasDns = spec.Resources["dns"]
		_, hasRoute = spec.Resources["route"]
	}
	hasService := spec.Service != nil
	applyClusterLocal := !hasDns && !hasRoute

	labels := map[string]string{}
	if applyClusterLocal {
		labels["networking.knative.dev/visibility"] = "cluster-local"
	}

	// rawSubFn2 := framework.BuildSubstitutionFunction(spec.Metadata, resOutputs)

	rawSubFn := buildSecretAwareSubstitutionFn(spec.Metadata, resOutputs)
	subFn := func(key string) (interface{}, error) {
		return rawSubFn(key)
	}

	portList := []coreV1.ContainerPort{}
	if hasService {
		portList = extractContainerPorts(spec.Service)
	}

	// Print spec.Resources for debugging
	fmt.Printf("[DEBUG] spec.Resources: %+v\n", spec.Resources)
	// Build a map of resourceName -> secretName for all resources
	resourceSecretNames := map[string]string{}
	for resName, res := range spec.Resources {
		var sname string
		// Always use writeConnectionSecretToRef.name if present
		if ref, ok := res.Params["writeConnectionSecretToRef"]; ok {
			if refMap, ok := ref.(map[string]interface{}); ok {
				if n, ok := refMap["name"].(string); ok && n != "" {
					sname = n
				}
			}
		}
		// fallback to connectionSecretName if above not found (for all resources)
		if sname == "" {
			if n, ok := res.Params["connectionSecretName"].(string); ok && n != "" {
				sname = n
			}
		}
		// Fallbacks for other resources
		if sname == "" {
			type nameField interface{ GetName() string }
			if nf, ok := any(res.Metadata).(nameField); ok {
				sname = nf.GetName()
			}
		}
		if sname == "" {
			if v, ok := any(res.Metadata).(struct{ Name string }); ok {
				sname = v.Name
			}
		}
		if sname == "" {
			if meta, ok := any(res.Metadata).(map[string]interface{}); ok {
				if n, ok := meta["name"].(string); ok {
					sname = n
				}
			}
		}
		if sname != "" {
			resourceSecretNames[resName] = sname
		}
	}
	// Print resourceSecretNames for debugging
	fmt.Printf("[DEBUG] resourceSecretNames: %+v\n", resourceSecretNames)
	// ...existing code...

	knativeContainers := make([]coreV1.Container, 0, len(containerNames))
	for _, name := range containerNames {
		c := containers[name]

		// envVars, err := convertContainerVariables(c.Variables, subFn, resourceSecretNames)
		envVars, err := convertContainerVariables(c.Variables, subFn, resourceSecretNames)
		if err != nil {
			return nil, fmt.Errorf("workload: %s: container: %s: variables: %w", workloadName, name, err)
		}

		container := coreV1.Container{
			Name:  name,
			Image: c.Image,
			Env:   envVars,
			Ports: portList,
		}

		converted, err := convertContainerResources(c.Resources)
		if err != nil {
			return nil, errors.Wrapf(err, "containers.%s.resources: failed to convert", name)
		}
		container.Resources = converted

		knativeContainers = append(knativeContainers, container)
	}

	ks := &servingv1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "serving.knative.dev/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: workloadName,
		},
		Spec: servingv1.ServiceSpec{
			ConfigurationSpec: servingv1.ConfigurationSpec{
				Template: servingv1.RevisionTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: labels,
					},
					Spec: servingv1.RevisionSpec{
						PodSpec: coreV1.PodSpec{
							Containers:         knativeContainers,
							ServiceAccountName: workloadName,
						},
					},
				},
			},
		},
	}

	manifest, err := convertToMap(ks)
	if err != nil {
		return nil, fmt.Errorf("failed to convert Knative Service to map: %w", err)
	}
	irsa := IRSAResource(workloadName)
	return []map[string]interface{}{irsa, manifest}, nil
}

// func convertContainerVariables(vars scoretypes.ContainerVariables, sf func(string) (interface{}, error), resourceSecretNames map[string]string) ([]coreV1.EnvVar, error) {
// 	env := []coreV1.EnvVar{}
// 	for k, v := range vars {
// 		adds, err := convertContainerVariable(k, v, sf, resourceSecretNames)
// 		if err != nil {
// 			return nil, errors.Wrapf(err, "'%s': failed to convert", k)
// 		}
// 		for _, e := range adds {
// 			if !slices.ContainsFunc(env, func(existing coreV1.EnvVar) bool {
// 				return existing.Name == e.Name
// 			}) {
// 				env = append(env, e)
// 			}
// 		}
// 	}
// 	slices.SortFunc(env, func(a, b coreV1.EnvVar) int {
// 		aRef := strings.HasPrefix(a.Name, "__ref_")
// 		bRef := strings.HasPrefix(b.Name, "__ref_")
// 		if aRef && !bRef {
// 			return -1
// 		} else if bRef && !aRef {
// 			return 1
// 		}
// 		return strings.Compare(a.Name, b.Name)
// 	})
// 	return env, nil
// }

// func convertContainerVariable(key, value string, sf func(string) (interface{}, error), resourceSecretNames map[string]string) ([]coreV1.EnvVar, error) {
// 	// Enhanced substitution: if value is exactly a single ${resources.<res>.<key>}, emit direct secretKeyRef
// 	trimmed := strings.TrimSpace(value)
// 	if strings.HasPrefix(trimmed, "${resources.") && strings.HasSuffix(trimmed, "}") {
// 		placeholder := trimmed[2 : len(trimmed)-1] // remove ${ and }
// 		parts := strings.Split(placeholder, ".")
// 		if len(parts) == 3 {
// 			resName := parts[1]
// 			keyName := parts[2]
// 			if secretName, ok := resourceSecretNames[resName]; ok && secretName != "" {
// 				return []coreV1.EnvVar{{
// 					Name: key,
// 					ValueFrom: &coreV1.EnvVarSource{
// 						SecretKeyRef: &coreV1.SecretKeySelector{
// 							LocalObjectReference: coreV1.LocalObjectReference{Name: secretName},
// 							Key:                  keyName,
// 						},
// 					},
// 				}}, nil
// 			}
// 		}
// 	}

// 	// Otherwise, do the original placeholder replacement for all resources
// 	resolvedStr := value
// 	var envVars []coreV1.EnvVar
// 	placeholderPrefix := "${resources."
// 	idx := 0
// 	for {
// 		start := strings.Index(resolvedStr[idx:], placeholderPrefix)
// 		if start == -1 {
// 			break
// 		}
// 		start += idx
// 		end := strings.Index(resolvedStr[start:], "}")
// 		if end == -1 {
// 			break
// 		}
// 		end += start
// 		placeholder := resolvedStr[start+2 : end] // e.g. resources.db.username
// 		parts := strings.Split(placeholder, ".")
// 		if len(parts) == 3 {
// 			resName := parts[1]
// 			keyName := parts[2]
// 			secretName := resName
// 			if s, ok := resourceSecretNames[resName]; ok && s != "" {
// 				secretName = s
// 			}
// 			refName := generateSecretRefEnvVarName(secretName, keyName)
// 			found := false
// 			for _, e := range envVars {
// 				if e.Name == refName {
// 					found = true
// 					break
// 				}
// 			}
// 			if !found {
// 				envVars = append(envVars, coreV1.EnvVar{
// 					Name: refName,
// 					ValueFrom: &coreV1.EnvVarSource{
// 						SecretKeyRef: &coreV1.SecretKeySelector{
// 							LocalObjectReference: coreV1.LocalObjectReference{Name: secretName},
// 							Key:                  keyName,
// 						},
// 					},
// 				})
// 			}
// 			resolvedStr = resolvedStr[:start] + "$(" + refName + ")" + resolvedStr[end+1:]
// 			idx = start + len("$("+refName+")")
// 		} else {
// 			idx = end + 1
// 		}
// 	}
// 	// Now call sf for any remaining substitutions (non-resource)
// 	resolved, err := sf(resolvedStr)
// 	if err != nil {
// 		return nil, errors.Wrap(err, "failed to substitute placeholders")
// 	}
// 	switch v := resolved.(type) {
// 	case string:
// 		if len(envVars) > 0 {
// 			envVars = append(envVars, coreV1.EnvVar{Name: key, Value: v})
// 			return envVars, nil
// 		}
// 		parts, refs, err := internal.DecodeSecretReferences(v)
// 		if err != nil {
// 			return nil, errors.Wrap(err, "failed to resolve secret references")
// 		}
// 		if len(refs) == 0 {
// 			return []coreV1.EnvVar{{Name: key, Value: v}}, nil
// 		}
// 		if len(refs) == 1 && parts[0] == "" && parts[1] == "" {
// 			return []coreV1.EnvVar{{Name: key, ValueFrom: &coreV1.EnvVarSource{
// 				SecretKeyRef: &coreV1.SecretKeySelector{
// 					LocalObjectReference: coreV1.LocalObjectReference{Name: refs[0].Name},
// 					Key:                  refs[0].Key,
// 				},
// 			}}}, nil
// 		}
// 		out := make([]coreV1.EnvVar, 0, 1+len(refs))
// 		for _, ref := range refs {
// 			out = append(out, coreV1.EnvVar{
// 				Name: generateSecretRefEnvVarName(ref.Name, ref.Key),
// 				ValueFrom: &coreV1.EnvVarSource{
// 					SecretKeyRef: &coreV1.SecretKeySelector{
// 						LocalObjectReference: coreV1.LocalObjectReference{Name: ref.Name},
// 						Key:                  ref.Key,
// 					},
// 				},
// 			})
// 		}
// 		sb := new(strings.Builder)
// 		for i, part := range parts {
// 			if i > 0 {
// 				sb.WriteString(fmt.Sprintf("$(%s)", generateSecretRefEnvVarName(refs[i-1].Name, refs[i-1].Key)))
// 			}
// 			sb.WriteString(part)
// 		}
// 		out = append(out, coreV1.EnvVar{Name: key, Value: sb.String()})
// 		return out, nil
// 	case map[string]interface{}:
// 		if secret, ok := v["secret"].(map[string]interface{}); ok {
// 			name, _ := secret["name"].(string)
// 			keyName, _ := secret["key"].(string)
// 			return []coreV1.EnvVar{{
// 				Name: key,
// 				ValueFrom: &coreV1.EnvVarSource{
// 					SecretKeyRef: &coreV1.SecretKeySelector{
// 						LocalObjectReference: coreV1.LocalObjectReference{Name: name},
// 						Key:                  keyName,
// 					},
// 				},
// 			}}, nil
// 		}
// 		return nil, fmt.Errorf("unsupported secret reference format for key '%s'", key)
// 	default:
// 		return nil, fmt.Errorf("unsupported value type for key '%s'", key)
// 	}
// }

// func generateSecretRefEnvVarName(secretName, key string) string {
// 	h := fnv.New128()
// 	_, _ = h.Write([]byte(secretName))
// 	_, _ = h.Write([]byte(key))
// 	return fmt.Sprintf("__ref_%s", strings.NewReplacer("_", "0", "-", "0").Replace(base64.RawURLEncoding.EncodeToString(h.Sum(nil))))
// }

func extractContainerPorts(service *scoretypes.WorkloadService) []coreV1.ContainerPort {
	ports := []coreV1.ContainerPort{}
	if service == nil {
		return ports
	}
	for _, port := range service.Ports {
		proto := coreV1.ProtocolTCP
		if port.Protocol != nil && *port.Protocol != "" {
			proto = coreV1.Protocol(strings.ToUpper(string(*port.Protocol)))
		}
		target := port.Port
		if port.TargetPort != nil && *port.TargetPort > 0 {
			target = *port.TargetPort
		}
		ports = append(ports, coreV1.ContainerPort{
			ContainerPort: int32(target),
			Protocol:      proto,
		})
	}
	slices.SortFunc(ports, func(a, b coreV1.ContainerPort) int {
		return strings.Compare(a.Name, b.Name)
	})
	return ports
}

func convertToMap(obj interface{}) (map[string]interface{}, error) {
	yml, err := yaml.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if err := yaml.Unmarshal(yml, &result); err != nil {
		return nil, err
	}
	deleteNestedField(result, "metadata", "creationTimestamp")
	deleteNestedField(result, "spec", "template", "metadata", "creationTimestamp")
	delete(result, "status")
	return result, nil
}

func deleteNestedField(m map[string]interface{}, keys ...string) {
	if len(keys) == 0 {
		return
	}
	for i := 0; i < len(keys)-1; i++ {
		next, ok := m[keys[i]].(map[string]interface{})
		if !ok {
			return
		}
		m = next
	}
	delete(m, keys[len(keys)-1])
}

func buildSecretAwareSubstitutionFn(
	metadata map[string]interface{},
	resOutputs map[string]framework.OutputLookupFunc,
) func(string) (interface{}, error) {
	return func(path string) (interface{}, error) {
		// If not a valid placeholder, just return as-is
		if !strings.HasPrefix(path, "resources.") {
			return path, nil
		}
		parts := strings.Split(path, ".")
		if len(parts) < 3 || parts[0] != "resources" {
			return path, nil // instead of error, just return the string
		}
		resName := parts[1]
		key := parts[2]

		lookupFn, ok := resOutputs[resName]
		if !ok {
			return nil, fmt.Errorf("resource '%s' not found", resName)
		}

		value, err := lookupFn(key)
		if err != nil {
			return nil, fmt.Errorf("key '%s' not found in resource '%s': %w", key, resName, err)
		}

		// ✅ Return raw value (string OR map)
		return value, nil
	}
}