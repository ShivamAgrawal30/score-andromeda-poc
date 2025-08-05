package provisioners

import (
	"github.com/score-spec/score-andromeda/internal/state"
	scoretypes "github.com/score-spec/score-go/types"
)

func init() {
	register("dns", noopProvisioner)
	register("route", noopProvisioner)
}

func noopProvisioner(name string, spec scoretypes.Resource, st *state.State) ([]interface{}, error) {
	return nil, nil // No manifests returned; it's a pass-through
}
