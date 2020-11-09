package resource

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/weaveworks/cluster-api-provider-existinginfra/pkg/plan"
	reflections "gopkg.in/oleiade/reflections.v1"
)

// OS is a set of OS properties.
type OS struct {
	MachineID  string `structs:"MachineID"`
	SystemUUID string `structs:"SystemUUID"`

	runner plan.Runner
}

type SELinuxStatus int

const (
	SELinuxUnknown SELinuxStatus = iota
	SELinuxNotInstalled
	SELinuxInstalled
)

func (s SELinuxStatus) IsUnknown() bool {
	return s == SELinuxUnknown
}

func (s SELinuxStatus) IsNotInstalled() bool {
	return s == SELinuxNotInstalled
}

func (s SELinuxStatus) IsInstalled() bool {
	return s == SELinuxInstalled
}

type SELinuxMode int

const (
	SELinuxModeUnknown SELinuxMode = iota
	SELinuxEnforcing
	SELinuxPermissive
	SELinuxDisabled
)

func (m SELinuxMode) IsUnknown() bool {
	return m == SELinuxModeUnknown
}

func (m SELinuxMode) IsEnforcing() bool {
	return m == SELinuxEnforcing
}

func (m SELinuxMode) IsPermissive() bool {
	return m == SELinuxPermissive
}

func (m SELinuxMode) IsDisabled() bool {
	return m == SELinuxDisabled
}

func NewOS(ctx context.Context, r plan.Runner) (*OS, error) {
	osr := &OS{runner: r}
	_, err := osr.Apply(ctx, r, plan.EmptyDiff())
	if err != nil {
		return nil, err
	}
	return osr, nil
}

type GatherFactFunc func(ctx context.Context, o *OS, r plan.Runner) error

type factGatheringParams struct {
	paramName   string `structs:"pname"`
	readFileCmd string `structs:"rfcmd"`
	cmdErr      string `structs:"iderr"`
	blankErr    string `structs:"idblankerr,omitempty"`
}

func newFactGatheringParams(pname string, fnames ...string) factGatheringParams {
	return factGatheringParams{
		paramName:   pname,
		readFileCmd: readFileCommand(fnames...),
		cmdErr:      fmt.Sprintf("Could not get %s", pname),
		blankErr:    fmt.Sprintf("%s is blank", pname),
	}
}

func readFileCommand(fnames ...string) string {
	fileCmds := make([]string, len(fnames))
	for i, fname := range fnames {
		fileCmds[i] = fmt.Sprintf("cat %s", fname)
	}
	// We need to disable stderr output here, otherwise the output will be clobbered with such output
	// if the first file is not existent
	return strings.Join(fileCmds, " 2>/dev/null || ") + " 2>/dev/null"
}

var (
	machineIDParams               = newFactGatheringParams("MachineID", "/etc/machine-id", "/var/lib/dbus/machine-id")
	sysUUIDParams                 = newFactGatheringParams("SystemUUID", "/sys/class/dmi/id/product_uuid", "/etc/machine-id")
	_               plan.Resource = plan.RegisterResource(&OS{})
)

// State implements plan.Resource.
func (p *OS) State() plan.State {
	return ToState(p)
}

var gatherFuncs []GatherFactFunc = []GatherFactFunc{
	getMachineID,
	getSystemUUID,
}

func (p *OS) gatherFacts(ctx context.Context, r plan.Runner) error {
	for _, f := range gatherFuncs {
		err := f(ctx, p, r)
		if err != nil {
			log.Errorf("error: %s\n", err.Error())
			return err
		}
	}
	return nil
}

func (p *OS) query(ctx context.Context, r plan.Runner) error {
	err := p.gatherFacts(ctx, r)
	if err != nil {
		return err
	}
	return nil
}

// QueryState implements plan.Resource.
func (p *OS) QueryState(ctx context.Context, r plan.Runner) (plan.State, error) {
	err := p.query(ctx, r)
	if err != nil {
		return plan.EmptyState, err
	}
	return p.State(), nil
}

// Apply implements plan.Resource.
func (p *OS) Apply(ctx context.Context, r plan.Runner, _ plan.Diff) (bool, error) {
	err := p.query(ctx, r)
	if err != nil {
		return false, err
	}
	return false, nil
}

func (p *OS) Undo(ctx context.Context, r plan.Runner, current plan.State) error {
	return nil
}

func (p *OS) HasCommand(ctx context.Context, cmd string) (bool, error) {
	// http://stackoverflow.com/questions/592620/how-to-check-if-a-program-exists-from-a-bash-script
	_, err := p.runner.RunCommand(ctx, fmt.Sprintf("command -v -- %q >/dev/null 2>&1", cmd), nil)
	if err == nil {
		// Command found.
		return true, nil
	}

	if _, ok := err.(*plan.RunError); ok {
		// Non-zero exit code. It means: Command not found.
		return false, nil
	}

	// Runtime error.
	return false, err
}

func (p *OS) GetSELinuxStatus(ctx context.Context) (SELinuxStatus, SELinuxMode, error) {
	const cmd = "selinuxenabled"

	if hasCmd, err := p.HasCommand(ctx, cmd); err != nil {
		// Inconclusive.
		return SELinuxUnknown, SELinuxModeUnknown, err
	} else if !hasCmd {
		// No SELinux tools installed.
		return SELinuxNotInstalled, SELinuxModeUnknown, nil
	}

	if _, err := p.runner.RunCommand(ctx, cmd, nil); err == nil {
		// SELinux not disabled (that is, enforcing or permissive).
		// return SELinuxEnforcing, nil
		if permissive, err := p.IsSELinuxMode(ctx, "permissive"); err == nil && permissive {
			return SELinuxInstalled, SELinuxPermissive, nil
		} else if enforcing, err := p.IsSELinuxMode(ctx, "enforcing"); err == nil && enforcing {
			return SELinuxInstalled, SELinuxEnforcing, nil
		} else {
			return SELinuxInstalled, SELinuxModeUnknown, err
		}
	} else if err, ok := err.(*plan.RunError); ok && err.ExitCode == 1 {
		// SELinux disabled.
		return SELinuxInstalled, SELinuxDisabled, nil
	} else {
		// Inconclusive.
		return SELinuxInstalled, SELinuxModeUnknown, err
	}
}

func (p *OS) IsSELinuxMode(ctx context.Context, mode string) (bool, error) {
	if _, err := p.runner.RunCommand(ctx, "sestatus | grep 'Current mode' | grep "+mode, nil); err == nil {
		return true, nil
	} else if err, ok := err.(*plan.RunError); ok && err.ExitCode == 1 {
		// selinux not in the permissive mode
		return false, nil
	} else {
		return false, err
	}
}

func (p *OS) IsOSInContainerVM(ctx context.Context) (bool, error) {
	output, err := p.runner.RunCommand(ctx, "cat /proc/1/environ", nil)
	return strings.Contains(output, "container=docker"), err
}

func getMachineID(ctx context.Context, p *OS, r plan.Runner) error {
	return p.getValueFromFileContents(ctx, machineIDParams, r)
}

func getSystemUUID(ctx context.Context, p *OS, r plan.Runner) error {
	return p.getValueFromFileContents(ctx, sysUUIDParams, r)
}

func (p *OS) getValueFromFileContents(ctx context.Context, fgparams factGatheringParams, r plan.Runner) error {
	cmd := fgparams.readFileCmd
	output, err := r.RunCommand(ctx, cmd, nil)
	if err != nil {
		return errors.New(fgparams.cmdErr)
	}
	param := strings.TrimSpace(output)
	if len(param) == 0 {
		return errors.New(fgparams.blankErr)
	}
	return reflections.SetField(p, fgparams.paramName, param)
}
