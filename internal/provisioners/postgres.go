package provisioners

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/score-spec/score-andromeda/internal/state"
	"github.com/score-spec/score-go/framework"
	scoretypes "github.com/score-spec/score-go/types"
)

func init() {
	register("postgres", postgresProvisioner)
}

func generateFallbackGuid() string {
	randomBytes := make([]byte, 8)
	_, _ = rand.Read(randomBytes)
	return strings.ToLower(hex.EncodeToString(randomBytes))
}

func postgresProvisioner(name string, spec scoretypes.Resource, st *state.State) ([]interface{}, error) {
	// Reconstruct the correct resource UID as in provisioning.go
	// Use spec.Type, spec.Class, spec.Id to match the UID logic
	var classPtr *string = nil
	if spec.Class != nil {
		classPtr = spec.Class
	}
	var idPtr *string = nil
	if spec.Id != nil {
		idPtr = spec.Id
	}
	resUID := framework.NewResourceUid("", name, spec.Type, classPtr, idPtr)
	res, ok := st.Resources[resUID]
	if !ok {
		res = framework.ScoreResourceState[state.ResourceExtras]{
			Id:             name,
			SourceWorkload: "", // will be set via linkage during generate
			Guid:           generateFallbackGuid(),
			State:          map[string]interface{}{},
			Outputs:        map[string]interface{}{},
			Extras:         state.ResourceExtras{Manifest: []interface{}{}},
		}
	}
	// Debug print to check SourceWorkload before manifest creation
	fmt.Printf("[DEBUG] postgresProvisioner: resUID=%s, res.Id=%s, res.SourceWorkload='%s'\n", resUID, res.Id, res.SourceWorkload)
	// Always update SourceWorkload from state (set in provisioning.go)
	if stateRes, ok := st.Resources[resUID]; ok && stateRes.SourceWorkload != "" {
		res.SourceWorkload = stateRes.SourceWorkload
	}

	// res.Id = fmt.Sprintf("postgres.default#%s", name)

	// res.SourceWorkload = "tutotrial-app" // This should be set to the actual workload name
	// if res.SourceWorkload == "" {
	// 	res.SourceWorkload = "unknown"
	// }

	if res.Guid == "" {
		res.Guid = generateFallbackGuid()
	}

	if res.State == nil {
		res.State = map[string]interface{}{}
	}
	if res.Outputs == nil {
		res.Outputs = map[string]interface{}{}
	}
	if res.Extras.Manifest == nil {
		res.Extras.Manifest = []interface{}{}
	}

	randomBytes := make([]byte, 4)
	_, _ = rand.Read(randomBytes)
	dbName := fmt.Sprintf("db-%s", strings.ToLower(hex.EncodeToString(randomBytes)))
	// guidPrefix := strings.ToLower(res.Guid[:8])
	// serviceName := fmt.Sprintf("pg-%s-%s", res.SourceWorkload, guidPrefix)
	serviceName := fmt.Sprintf("pg-%s", res.SourceWorkload)

	secretName := fmt.Sprintf("%s-db-connection-creds", serviceName)

	res.State["database"] = dbName
	res.State["service"] = serviceName

	res.Outputs["host"] = state.SecretOutput(secretName, "host")
	res.Outputs["port"] = state.SecretOutput(secretName, "port")
	res.Outputs["database"] = state.SecretOutput(secretName, "database")
	res.Outputs["username"] = state.SecretOutput(secretName, "username")
	res.Outputs["password"] = state.SecretOutput(secretName, "password")
	res.Outputs["name"] = dbName

	manifest := map[string]interface{}{
		"apiVersion": "andromeda.nn.nl/v1alpha1",
		"kind":       "SQLDatabase",
		"metadata": map[string]interface{}{
			"name": serviceName,
			"annotations": map[string]interface{}{
				"andromeda.score.dev/source-workload": res.SourceWorkload,
				"andromeda.score.dev/resource-uid":    resUID,
				"andromeda.score.dev/resource-guid":   res.Guid,
				"argocd.argoproj.io/sync-wave":        "0",
			},
			"labels": map[string]interface{}{
				"app.kubernetes.io/managed-by": "score-andromeda",
				"app.kubernetes.io/name":       serviceName,
				"app.kubernetes.io/instance":   serviceName,
			},
		},
		"spec": map[string]interface{}{
			"engine": "PostgreSQL",
			"writeConnectionSecretToRef": map[string]interface{}{
				"name": secretName,
			},
		},
	}

	res.Extras.Manifest = []interface{}{manifest}
	st.Resources[resUID] = res

	return []interface{}{manifest}, nil
}
