// Copyright 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package client

import (
	"github.com/juju/errors"

	"github.com/juju/juju/environs"
	"github.com/juju/juju/instance"
	"github.com/juju/juju/state"
)

// DestroyEnvironment destroys all services and non-manager machine
// instances in the environment.
func (c *Client) DestroyEnvironment() (err error) {
	if err = c.check.DestroyAllowed(); err != nil {
		return errors.Trace(err)
	}

	env, err := c.api.state.Environment()
	if err != nil {
		return errors.Trace(err)
	}

	if err = env.Destroy(); err != nil {
		return errors.Trace(err)
	}

	machines, err := c.api.state.AllMachines()
	if err != nil {
		return errors.Trace(err)
	}

	// We must destroy instances server-side to support JES (Juju Environment
	// Server), as there's no CLI to fall back on. In that case, we only ever
	// destroy non-state machines; we leave destroying state servers in non-
	// hosted environments to the CLI, as otherwise the API server may get cut
	// off.
	if err := destroyInstances(c.api.state, machines); err != nil {
		return errors.Trace(err)
	}

	// If this is not the state server environment, remove all documents from
	// state associated with the environment.
	if env.UUID() != env.ServerTag().Id() {
		return errors.Trace(c.api.state.RemoveAllEnvironDocs())
	}

	// Return to the caller. If it's the CLI, it will finish up
	// by calling the provider's Destroy method, which will
	// destroy the state servers, any straggler instances, and
	// other provider-specific resources.
	return nil
}

// destroyInstances directly destroys all non-manager,
// non-manual machine instances.
func destroyInstances(st *state.State, machines []*state.Machine) error {
	var ids []instance.Id
	for _, m := range machines {
		if m.IsManager() {
			continue
		}
		if _, isContainer := m.ParentId(); isContainer {
			continue
		}
		manual, err := m.IsManual()
		if manual {
			continue
		} else if err != nil {
			return err
		}
		id, err := m.InstanceId()
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	envcfg, err := st.EnvironConfig()
	if err != nil {
		return err
	}
	env, err := environs.New(envcfg)
	if err != nil {
		return err
	}
	return env.StopInstances(ids...)
}
