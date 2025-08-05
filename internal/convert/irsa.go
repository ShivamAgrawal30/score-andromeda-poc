package convert

func IRSAResource(appName string) map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": "andromeda.nn.nl/v1alpha1",
		"kind":       "IRSA",
		"metadata": map[string]interface{}{
			"annotations": map[string]interface{}{
				"argocd.argoproj.io/sync-wave": "-1",
			},
			"name": appName,
		},
		"spec": map[string]interface{}{
			"deletionPolicy": "Delete",
		},
	}
}
