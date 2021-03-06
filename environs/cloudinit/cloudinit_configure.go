// Copyright 2012, 2013, 2014 Canonical Ltd.
// Copyright 2014 Cloudbase Solutions SRL
// Licensed under the AGPLv3, see LICENCE file for details.

package cloudinit

import (
	"fmt"

	"github.com/juju/errors"
	"github.com/juju/names"

	"github.com/juju/juju/agent"
	"github.com/juju/juju/cloudinit"
	"github.com/juju/juju/version"
)

type UserdataConfig interface {
	// Configure is a convenience function that updates the cloudinit.Config
	// with appropriate configuration. It will run ConfigureBasic() and
	// ConfigureJuju()
	Configure() error
	// ConfigureBasic updates the provided cloudinit.Config with
	// basic configuration to initialise an OS image.
	ConfigureBasic() error
	// ConfigureJuju updates the provided cloudinit.Config with configuration
	// to initialise a Juju machine agent.
	ConfigureJuju() error
	// Render renders the cloudinit/cloudbase-init userdata needed to initialize
	// the juju agent
	Render() ([]byte, error)
}

func NewUserdataConfig(mcfg *MachineConfig, conf *cloudinit.Config) (UserdataConfig, error) {
	// TODO(ericsnow) bug #1426217
	// Protect mcfg and conf better.
	operatingSystem, err := version.GetOSFromSeries(mcfg.Series)
	if err != nil {
		return nil, err
	}

	base := baseConfigure{
		tag:  names.NewMachineTag(mcfg.MachineId),
		mcfg: mcfg,
		conf: conf,
		os:   operatingSystem,
	}

	switch operatingSystem {
	case version.Ubuntu:
		return &ubuntuConfigure{base}, nil
	case version.Windows:
		return &windowsConfigure{base}, nil
	default:
		return nil, errors.NotSupportedf("OS %s", mcfg.Series)
	}
}

type baseConfigure struct {
	tag  names.Tag
	mcfg *MachineConfig
	conf *cloudinit.Config
	os   version.OSType
}

func (c *baseConfigure) Render() ([]byte, error) {
	return c.conf.Render()
}

// addAgentInfo adds agent-required information to the agent's directory
// and returns the agent directory name.
func (c *baseConfigure) addAgentInfo() (agent.Config, error) {
	acfg, err := c.mcfg.agentConfig(c.tag, c.mcfg.Tools.Version.Number)
	if err != nil {
		return nil, errors.Trace(err)
	}
	acfg.SetValue(agent.AgentServiceName, c.mcfg.MachineAgentServiceName)
	cmds, err := acfg.WriteCommands(c.conf.ShellRenderer)
	if err != nil {
		return nil, errors.Annotate(err, "failed to write commands")
	}
	c.conf.AddScripts(cmds...)
	return acfg, nil
}

func (c *baseConfigure) addMachineAgentToBoot() error {
	svc, err := c.mcfg.initService(c.conf.ShellRenderer)
	if err != nil {
		return errors.Trace(err)
	}

	// Make the agent run via a symbolic link to the actual tools
	// directory, so it can upgrade itself without needing to change
	// the init script.
	toolsDir := c.mcfg.toolsDir(c.conf.ShellRenderer)
	c.conf.AddScripts(c.toolsSymlinkCommand(toolsDir))

	name := c.tag.String()
	cmds, err := svc.InstallCommands()
	if err != nil {
		return errors.Annotatef(err, "cannot make cloud-init init script for the %s agent", name)
	}
	startCmds, err := svc.StartCommands()
	if err != nil {
		return errors.Annotatef(err, "cannot make cloud-init init script for the %s agent", name)
	}
	cmds = append(cmds, startCmds...)

	svcName := c.mcfg.MachineAgentServiceName
	c.conf.AddRunCmd(cloudinit.LogProgressCmd("Starting Juju machine agent (%s)", svcName))
	c.conf.AddScripts(cmds...)
	return nil
}

// TODO(ericsnow) toolsSymlinkCommand should just be replaced with a
// call to shell.Renderer.Symlink.

func (c *baseConfigure) toolsSymlinkCommand(toolsDir string) string {
	switch c.os {
	case version.Windows:
		return fmt.Sprintf(
			`cmd.exe /C mklink /D %s %v`,
			c.conf.ShellRenderer.FromSlash(toolsDir),
			c.mcfg.Tools.Version,
		)
	default:
		// TODO(dfc) ln -nfs, so it doesn't fail if for some reason that
		// the target already exists.
		return fmt.Sprintf(
			"ln -s %v %s",
			c.mcfg.Tools.Version,
			shquote(toolsDir),
		)
	}
}
