package convert

import (
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"

	"github.com/pkg/errors"
	"github.com/score-spec/score-andromeda/internal"
	scoretypes "github.com/score-spec/score-go/types"
	coreV1 "k8s.io/api/core/v1"
)

func generateSecretRefEnvVarName(secretName, key string) string {
	h := fnv.New128()
	_, _ = h.Write([]byte(secretName))
	_, _ = h.Write([]byte(key))
	return fmt.Sprintf("__ref_%s", strings.NewReplacer("_", "0", "-", "0").Replace(base64.RawURLEncoding.EncodeToString(h.Sum(nil))))
}

func convertContainerVariables(vars scoretypes.ContainerVariables, sf func(string) (interface{}, error), resourceSecretNames map[string]string) ([]coreV1.EnvVar, error) {
	env := []coreV1.EnvVar{}
	for k, v := range vars {
		adds, err := convertContainerVariable(k, v, sf, resourceSecretNames)
		if err != nil {
			return nil, errors.Wrapf(err, "'%s': failed to convert", k)
		}
		for _, e := range adds {
			if !slices.ContainsFunc(env, func(existing coreV1.EnvVar) bool {
				return existing.Name == e.Name
			}) {
				env = append(env, e)
			}
		}
	}
	slices.SortFunc(env, func(a, b coreV1.EnvVar) int {
		aRef := strings.HasPrefix(a.Name, "__ref_")
		bRef := strings.HasPrefix(b.Name, "__ref_")
		if aRef && !bRef {
			return -1
		} else if bRef && !aRef {
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return env, nil
}

func convertContainerVariable(key, value string, sf func(string) (interface{}, error), resourceSecretNames map[string]string) ([]coreV1.EnvVar, error) {
	// Enhanced substitution: if value is exactly a single ${resources.<res>.<key>}, emit direct secretKeyRef
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "${resources.") && strings.HasSuffix(trimmed, "}") {
		placeholder := trimmed[2 : len(trimmed)-1] // remove ${ and }
		parts := strings.Split(placeholder, ".")
		if len(parts) == 3 {
			resName := parts[1]
			keyName := parts[2]
			if secretName, ok := resourceSecretNames[resName]; ok && secretName != "" {
				return []coreV1.EnvVar{{
					Name: key,
					ValueFrom: &coreV1.EnvVarSource{
						SecretKeyRef: &coreV1.SecretKeySelector{
							LocalObjectReference: coreV1.LocalObjectReference{Name: secretName},
							Key:                  keyName,
						},
					},
				}}, nil
			}
		}
	}

	// Otherwise, do the original placeholder replacement for all resources
	resolvedStr := value
	var envVars []coreV1.EnvVar
	placeholderPrefix := "${resources."
	idx := 0
	for {
		start := strings.Index(resolvedStr[idx:], placeholderPrefix)
		if start == -1 {
			break
		}
		start += idx
		end := strings.Index(resolvedStr[start:], "}")
		if end == -1 {
			break
		}
		end += start
		placeholder := resolvedStr[start+2 : end] // e.g. resources.db.username
		parts := strings.Split(placeholder, ".")
		if len(parts) == 3 {
			resName := parts[1]
			keyName := parts[2]
			secretName := resName
			if s, ok := resourceSecretNames[resName]; ok && s != "" {
				secretName = s
			}
			refName := generateSecretRefEnvVarName(secretName, keyName)
			found := false
			for _, e := range envVars {
				if e.Name == refName {
					found = true
					break
				}
			}
			if !found {
				envVars = append(envVars, coreV1.EnvVar{
					Name: refName,
					ValueFrom: &coreV1.EnvVarSource{
						SecretKeyRef: &coreV1.SecretKeySelector{
							LocalObjectReference: coreV1.LocalObjectReference{Name: secretName},
							Key:                  keyName,
						},
					},
				})
			}
			resolvedStr = resolvedStr[:start] + "$(" + refName + ")" + resolvedStr[end+1:]
			idx = start + len("$("+refName+")")
		} else {
			idx = end + 1
		}
	}
	// Now call sf for any remaining substitutions (non-resource)
	resolved, err := sf(resolvedStr)
	if err != nil {
		return nil, errors.Wrap(err, "failed to substitute placeholders")
	}
	switch v := resolved.(type) {
	case string:
		if len(envVars) > 0 {
			envVars = append(envVars, coreV1.EnvVar{Name: key, Value: v})
			return envVars, nil
		}
		parts, refs, err := internal.DecodeSecretReferences(v)
		if err != nil {
			return nil, errors.Wrap(err, "failed to resolve secret references")
		}
		if len(refs) == 0 {
			return []coreV1.EnvVar{{Name: key, Value: v}}, nil
		}
		if len(refs) == 1 && parts[0] == "" && parts[1] == "" {
			return []coreV1.EnvVar{{Name: key, ValueFrom: &coreV1.EnvVarSource{
				SecretKeyRef: &coreV1.SecretKeySelector{
					LocalObjectReference: coreV1.LocalObjectReference{Name: refs[0].Name},
					Key:                  refs[0].Key,
				},
			}}}, nil
		}
		out := make([]coreV1.EnvVar, 0, 1+len(refs))
		for _, ref := range refs {
			out = append(out, coreV1.EnvVar{
				Name: generateSecretRefEnvVarName(ref.Name, ref.Key),
				ValueFrom: &coreV1.EnvVarSource{
					SecretKeyRef: &coreV1.SecretKeySelector{
						LocalObjectReference: coreV1.LocalObjectReference{Name: ref.Name},
						Key:                  ref.Key,
					},
				},
			})
		}
		sb := new(strings.Builder)
		for i, part := range parts {
			if i > 0 {
				sb.WriteString(fmt.Sprintf("$(%s)", generateSecretRefEnvVarName(refs[i-1].Name, refs[i-1].Key)))
			}
			sb.WriteString(part)
		}
		out = append(out, coreV1.EnvVar{Name: key, Value: sb.String()})
		return out, nil
	case map[string]interface{}:
		if secret, ok := v["secret"].(map[string]interface{}); ok {
			name, _ := secret["name"].(string)
			keyName, _ := secret["key"].(string)
			return []coreV1.EnvVar{{
				Name: key,
				ValueFrom: &coreV1.EnvVarSource{
					SecretKeyRef: &coreV1.SecretKeySelector{
						LocalObjectReference: coreV1.LocalObjectReference{Name: name},
						Key:                  keyName,
					},
				},
			}}, nil
		}
		return nil, fmt.Errorf("unsupported secret reference format for key '%s'", key)
	default:
		return nil, fmt.Errorf("unsupported value type for key '%s'", key)
	}
}
