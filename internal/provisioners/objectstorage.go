package provisioners

import (
	"fmt"

	"github.com/score-spec/score-andromeda/internal/state"
	"github.com/score-spec/score-go/framework"
	scoretypes "github.com/score-spec/score-go/types"
)

func init() {
	register("s3", s3Provisioner)
}

func s3Provisioner(name string, spec scoretypes.Resource, st *state.State) ([]interface{}, error) {
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
			SourceWorkload: "",
			Guid:           generateFallbackGuid(),
			State:          map[string]interface{}{},
			Outputs:        map[string]interface{}{},
			Extras:         state.ResourceExtras{Manifest: []interface{}{}},
		}
	}
	// Always update SourceWorkload from state (set in provisioning.go)
	if stateRes, ok := st.Resources[resUID]; ok && stateRes.SourceWorkload != "" {
		res.SourceWorkload = stateRes.SourceWorkload
	}

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

	bucketName := fmt.Sprintf("bucket-%s", res.SourceWorkload)
	res.State["bucket"] = bucketName

	secretName := fmt.Sprintf("%s-bucket-connection-creds", bucketName)

	res.Outputs["bucket"] = state.SecretOutput(secretName, "bucket")
	res.Outputs["endpoint"] = state.SecretOutput(secretName, "endpoint")
	res.Outputs["region"] = state.SecretOutput(secretName, "region")

	manifest := map[string]interface{}{
		"apiVersion": "andromeda.nn.nl/v1alpha1",
		"kind":       "ObjectStorage",
		"metadata": map[string]interface{}{
			"name": bucketName,
			"annotations": map[string]interface{}{
				"k8s.score.dev/source-workload": res.SourceWorkload,
				"k8s.score.dev/resource-uid":    resUID,
				"k8s.score.dev/resource-guid":   res.Guid,
				"argocd.argoproj.io/sync-wave":  "0",
			},
			"labels": map[string]interface{}{
				"app.kubernetes.io/managed-by": "score-k8s",
				"app.kubernetes.io/name":       bucketName,
				"app.kubernetes.io/instance":   bucketName,
			},
		},
		"spec": map[string]interface{}{
			"access": map[string]interface{}{
				"irsas": []string{res.SourceWorkload},
			},
			"writeConnectionSecretToRef": map[string]interface{}{
				"name": secretName,
			},
		},
	}

	res.Extras.Manifest = []interface{}{manifest}
	st.Resources[resUID] = res

	return []interface{}{manifest}, nil
}
