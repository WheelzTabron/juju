// Copyright 2012, 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package service

import (
	"github.com/juju/cmd"
	"github.com/juju/errors"

	"github.com/juju/juju/api/service"
	"github.com/juju/juju/cmd/juju/block"
	"github.com/juju/juju/cmd/modelcmd"
)

var usageExposeSummary = `
Makes a service publicly available over the network.`[1:]

var usageExposeDetails = `
Adjusts the firewall rules and any relevant security mechanisms of the
cloud to allow public access to the service.

Examples:
    juju expose wordpress

See also: 
    unexpose`[1:]

// NewExposeCommand returns a command to expose services.
func NewExposeCommand() cmd.Command {
	return modelcmd.Wrap(&exposeCommand{})
}

// exposeCommand is responsible exposing services.
type exposeCommand struct {
	modelcmd.ModelCommandBase
	ServiceName string
}

func (c *exposeCommand) Info() *cmd.Info {
	return &cmd.Info{
		Name:    "expose",
		Args:    "<service name>",
		Purpose: usageExposeSummary,
		Doc:     usageExposeDetails,
	}
}

func (c *exposeCommand) Init(args []string) error {
	if len(args) == 0 {
		return errors.New("no service name specified")
	}
	c.ServiceName = args[0]
	return cmd.CheckEmpty(args[1:])
}

type serviceExposeAPI interface {
	Close() error
	Expose(serviceName string) error
	Unexpose(serviceName string) error
}

func (c *exposeCommand) getAPI() (serviceExposeAPI, error) {
	root, err := c.NewAPIRoot()
	if err != nil {
		return nil, errors.Trace(err)
	}
	return service.NewClient(root), nil
}

// Run changes the juju-managed firewall to expose any
// ports that were also explicitly marked by units as open.
func (c *exposeCommand) Run(_ *cmd.Context) error {
	client, err := c.getAPI()
	if err != nil {
		return err
	}
	defer client.Close()
	return block.ProcessBlockedError(client.Expose(c.ServiceName), block.BlockChange)
}