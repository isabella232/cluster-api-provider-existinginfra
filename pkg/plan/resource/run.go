package resource

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	"github.com/weaveworks/cluster-api-provider-existinginfra/pkg/plan"
)

// Run is a resource running a script (which can be just a single command). Run
// doesn't realise any state, Apply will always run the given script.
type Run struct {
	Base

	Script       fmt.Stringer  `structs:"script"`
	UndoScript   fmt.Stringer  `structs:"undoScript,omitempty"`
	UndoResource plan.Resource `structs:"undoResource,omitempty"`
	Output       *string       // for later resources to use
}

var _ plan.Resource = plan.RegisterResource(&Run{})

// State implements plan.Resource.
func (r *Run) State() plan.State {
	return ToState(r)
}

// Apply implements plan.Resource.
func (r *Run) Apply(ctx context.Context, runner plan.Runner, diff plan.Diff) (bool, error) {
	str, err := runner.RunCommand(ctx, r.Script.String(), nil)
	if r.Output != nil {
		*r.Output = str
	}
	if err != nil {
		return false, errors.Wrap(err, str)
	}
	return true, nil
}

// Undo implements plan.Resource.
func (r *Run) Undo(ctx context.Context, runner plan.Runner, current plan.State) error {
	if r.UndoScript == nil {
		if r.UndoResource == nil {
			return nil
		} else {
			return r.UndoResource.Undo(ctx, runner, plan.EmptyState)
		}
		return r.UndoResource.Undo(ctx, runner, plan.EmptyState)
	}
	_, err := runner.RunCommand(ctx, r.UndoScript.String(), nil)
	return err
}
