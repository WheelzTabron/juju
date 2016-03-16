// Copyright 2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package user

import (
	"github.com/juju/cmd"
	"github.com/juju/errors"
	"github.com/juju/names"
	"gopkg.in/macaroon.v1"

	"github.com/juju/juju/api/usermanager"
	"github.com/juju/juju/cmd/modelcmd"
	"github.com/juju/juju/juju"
	"github.com/juju/juju/jujuclient"
)

const loginDoc = `
Log in to the controller.

After logging in successfully, the client system will
be updated such that any previously recorded password
will be removed from disk, and a temporary, time-limited
credential written in its place. Once the credential
expires, you will be prompted to run "juju login" again.

Examples:
  # Log in as the current user for the controller.
  juju login

  # Log in as the user "bob".
  juju login bob

`

func NewLoginCommand() cmd.Command {
	return modelcmd.WrapController(&loginCommand{
		newLoginAPI: func(args juju.NewAPIConnectionParams) (LoginAPI, error) {
			api, err := juju.NewAPIConnection(args)
			if err != nil {
				return nil, errors.Trace(err)
			}
			return usermanager.NewClient(api), nil
		},
	})
}

// loginCommand changes the password for a user.
type loginCommand struct {
	modelcmd.ControllerCommandBase
	newLoginAPI func(juju.NewAPIConnectionParams) (LoginAPI, error)
	User        string
}

// Info implements Command.Info.
func (c *loginCommand) Info() *cmd.Info {
	return &cmd.Info{
		Name:    "login",
		Args:    "[username]",
		Purpose: "logs in to the controller",
		Doc:     loginDoc,
	}
}

// Init implements Command.Init.
func (c *loginCommand) Init(args []string) error {
	var err error
	c.User, err = cmd.ZeroOrOneArgs(args)
	if err != nil {
		return errors.Trace(err)
	}
	return nil
}

// LoginAPI provides the API methods that the login command uses.
type LoginAPI interface {
	CreateLocalLoginMacaroon(names.UserTag) (*macaroon.Macaroon, error)
	Close() error
}

// Run implements Command.Run.
func (c *loginCommand) Run(ctx *cmd.Context) error {
	var accountName string
	var userTag names.UserTag
	controllerName := c.ControllerName()
	store := c.ClientStore()
	if c.User != "" {
		if !names.IsValidUserName(c.User) {
			return errors.NotValidf("user name %q", c.User)
		}
		userTag = names.NewUserTag(c.User)
		accountName = userTag.Canonical()
	} else {
		var err error
		accountName, err = store.CurrentAccount(controllerName)
		if err != nil {
			return errors.Trace(err)
		}
		userTag = names.NewUserTag(accountName)
	}
	accountDetails, err := store.AccountByName(controllerName, accountName)
	if err != nil && !errors.IsNotFound(err) {
		return errors.Trace(err)
	}

	// Read password from the terminal, and attempt to log in using that.
	password, err := readAndConfirmPassword(ctx)
	if err != nil {
		return errors.Trace(err)
	}
	params, err := c.NewAPIConnectionParams(store, controllerName, "", "")
	if err != nil {
		return errors.Trace(err)
	}
	if accountDetails != nil {
		accountDetails.Password = password
	} else {
		accountDetails = &jujuclient.AccountDetails{
			User:     accountName,
			Password: password,
		}
	}
	params.AccountDetails = accountDetails
	api, err := c.newLoginAPI(params)
	if err != nil {
		return errors.Annotate(err, "creating API connection")
	}
	defer api.Close()

	// Create a new local login macaroon, and update the account details
	// in the client store, removing the recorded password (if any) and
	// storing the macaroon.
	macaroon, err := api.CreateLocalLoginMacaroon(userTag)
	if err != nil {
		return errors.Annotate(err, "failed to create a temporary credential")
	}
	macaroonJSON, err := macaroon.MarshalJSON()
	if err != nil {
		return errors.Annotate(err, "marshalling temporary credential to JSON")
	}
	accountDetails.Password = ""
	accountDetails.Macaroon = string(macaroonJSON)
	if err := store.UpdateAccount(controllerName, accountName, *accountDetails); err != nil {
		return errors.Annotate(err, "failed to record temporary credential")
	}
	ctx.Infof("You are now logged in to %q as %q.", controllerName, accountName)
	return nil
}
