// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package featuretests

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/juju/names"
	gitjujutesting "github.com/juju/testing"
	gc "gopkg.in/check.v1"

	"github.com/juju/juju/agent"
	"github.com/juju/juju/api/base"
	"github.com/juju/juju/api/leadership"
	"github.com/juju/juju/api/uniter"
	leadershipapi "github.com/juju/juju/apiserver/leadership"
	"github.com/juju/juju/apiserver/params"
	agentcmd "github.com/juju/juju/cmd/jujud/agent"
	agenttesting "github.com/juju/juju/cmd/jujud/agent/testing"
	cmdutil "github.com/juju/juju/cmd/jujud/util"
	"github.com/juju/juju/mongo"
	"github.com/juju/juju/state"
	coretesting "github.com/juju/juju/testing"
	"github.com/juju/juju/testing/factory"
	"github.com/juju/juju/version"
)

type leadershipSuite struct {
	agenttesting.AgentSuite

	clientFacade base.ClientFacade
	facadeCaller base.FacadeCaller
	machineAgent *agentcmd.MachineAgent
	unitId       string
	serviceId    string
}

func (s *leadershipSuite) SetUpTest(c *gc.C) {

	s.AgentSuite.SetUpTest(c)

	file, _ := ioutil.TempFile("", "juju-run")
	defer file.Close()
	s.AgentSuite.PatchValue(&agentcmd.JujuRun, file.Name())

	if runtime.GOOS == "windows" {
		s.AgentSuite.PatchValue(&agentcmd.EnableJournaling, false)
	}

	fakeEnsureMongo := agenttesting.FakeEnsure{}
	s.AgentSuite.PatchValue(&cmdutil.EnsureMongoServer, fakeEnsureMongo.FakeEnsureMongo)

	// Create a machine to manage the environment.
	stateServer, password := s.Factory.MakeMachineReturningPassword(c, &factory.MachineParams{
		InstanceId: "id-1",
		Nonce:      agent.BootstrapNonce,
		Jobs:       []state.MachineJob{state.JobManageEnviron},
	})
	c.Assert(stateServer.PasswordValid(password), gc.Equals, true)
	c.Assert(stateServer.SetMongoPassword(password), gc.IsNil)

	// Create a machine to host some units.
	unitHostMachine := s.Factory.MakeMachine(c, &factory.MachineParams{
		Nonce:    agent.BootstrapNonce,
		Password: password,
	})

	// Create a service and an instance of that service so that we can
	// create a client.
	service := s.Factory.MakeService(c, &factory.ServiceParams{})
	s.serviceId = service.Tag().Id()

	unit := s.Factory.MakeUnit(c, &factory.UnitParams{Machine: unitHostMachine, Service: service})
	s.unitId = unit.UnitTag().Id()

	c.Assert(unit.SetPassword(password), gc.IsNil)
	unitState := s.OpenAPIAs(c, unit.Tag(), password)

	// Create components needed to construct a client.
	s.clientFacade, s.facadeCaller = base.NewClientFacade(unitState, leadershipapi.FacadeName)
	c.Assert(s.clientFacade, gc.NotNil)
	c.Assert(s.facadeCaller, gc.NotNil)

	// Tweak and write out the config file for the state server.
	writeStateAgentConfig(
		c,
		s.MongoInfo(c),
		s.DataDir(),
		stateServer.Tag(),
		s.State.EnvironTag(),
		password,
		version.Current,
	)

	// Create & start a machine agent so the tests have something to call into.
	agentConf := agentcmd.AgentConf{DataDir: s.DataDir()}
	machineAgentFactory := agentcmd.MachineAgentFactoryFn(&agentConf, &agentConf)
	s.machineAgent = machineAgentFactory(stateServer.Id())

	// See comment in createMockJujudExecutable
	if runtime.GOOS == "windows" {
		dirToRemove := createMockJujudExecutable(c, s.DataDir(), s.machineAgent.Tag().String())
		s.AddCleanup(func(*gc.C) { os.RemoveAll(dirToRemove) })
	}

	c.Log("Starting machine agent...")
	go func() {
		err := s.machineAgent.Run(coretesting.Context(c))
		c.Assert(err, gc.IsNil)
	}()
}

func (s *leadershipSuite) TearDownTest(c *gc.C) {
	c.Log("Stopping machine agent...")
	err := s.machineAgent.Stop()
	c.Assert(err, gc.IsNil)
	os.RemoveAll(filepath.Join(s.DataDir(), "tools"))

	s.AgentSuite.TearDownTest(c)
}

func (s *leadershipSuite) TestClaimLeadership(c *gc.C) {

	client := leadership.NewClient(s.clientFacade, s.facadeCaller)
	defer func() { err := client.Close(); c.Assert(err, gc.IsNil) }()

	err := client.ClaimLeadership(s.serviceId, s.unitId, 10*time.Second)
	c.Assert(err, gc.IsNil)

	unblocked := make(chan struct{})
	go func() {
		err := client.BlockUntilLeadershipReleased(s.serviceId)
		c.Check(err, gc.IsNil)
		unblocked <- struct{}{}
	}()

	time.Sleep(coretesting.ShortWait)

	select {
	case <-time.After(15 * time.Second):
		c.Errorf("Timed out waiting for leadership to release.")
	case <-unblocked:
	}
}

func (s *leadershipSuite) TestReleaseLeadership(c *gc.C) {

	client := leadership.NewClient(s.clientFacade, s.facadeCaller)
	defer func() { err := client.Close(); c.Assert(err, gc.IsNil) }()

	err := client.ClaimLeadership(s.serviceId, s.unitId, 10*time.Second)
	c.Assert(err, gc.IsNil)

	err = client.ReleaseLeadership(s.serviceId, s.unitId)
	c.Assert(err, gc.IsNil)
}

func (s *leadershipSuite) TestUnblock(c *gc.C) {

	client := leadership.NewClient(s.clientFacade, s.facadeCaller)
	defer func() { err := client.Close(); c.Assert(err, gc.IsNil) }()

	err := client.ClaimLeadership(s.serviceId, s.unitId, 10*time.Second)
	c.Assert(err, gc.IsNil)

	unblocked := make(chan struct{})
	go func() {
		err := client.BlockUntilLeadershipReleased(s.serviceId)
		c.Check(err, gc.IsNil)
		unblocked <- struct{}{}
	}()

	time.Sleep(coretesting.ShortWait)

	err = client.ReleaseLeadership(s.serviceId, s.unitId)
	c.Assert(err, gc.IsNil)

	select {
	case <-time.After(coretesting.LongWait):
		c.Errorf("Timed out waiting for leadership to release.")
	case <-unblocked:
	}
}

type uniterLeadershipSuite struct {
	agenttesting.AgentSuite

	factory      *factory.Factory
	clientFacade base.ClientFacade
	facadeCaller base.FacadeCaller
	machineAgent *agentcmd.MachineAgent
	unitId       string
	serviceId    string
}

func (s *uniterLeadershipSuite) TestReadLeadershipSettings(c *gc.C) {

	// First, the unit must be elected leader; otherwise merges will be denied.
	leaderClient := leadership.NewClient(s.clientFacade, s.facadeCaller)
	defer func() { err := leaderClient.Close(); c.Assert(err, gc.IsNil) }()
	err := leaderClient.ClaimLeadership(s.serviceId, s.unitId, 10*time.Second)
	c.Assert(err, gc.IsNil)

	client := uniter.NewState(s.facadeCaller.RawAPICaller(), names.NewUnitTag(s.unitId))

	// Toss a few settings in.
	desiredSettings := map[string]string{
		"foo": "bar",
		"baz": "biz",
	}

	err = client.LeadershipSettings.Merge(s.serviceId, desiredSettings)
	c.Assert(err, gc.IsNil)

	settings, err := client.LeadershipSettings.Read(s.serviceId)
	c.Assert(err, gc.IsNil)
	c.Check(settings, gc.DeepEquals, desiredSettings)
}

func (s *uniterLeadershipSuite) TestMergeLeadershipSettings(c *gc.C) {

	// First, the unit must be elected leader; otherwise merges will be denied.
	leaderClient := leadership.NewClient(s.clientFacade, s.facadeCaller)
	defer func() { err := leaderClient.Close(); c.Assert(err, gc.IsNil) }()
	err := leaderClient.ClaimLeadership(s.serviceId, s.unitId, 10*time.Second)
	c.Assert(err, gc.IsNil)

	client := uniter.NewState(s.facadeCaller.RawAPICaller(), names.NewUnitTag(s.unitId))

	// Grab what settings exist.
	settings, err := client.LeadershipSettings.Read(s.serviceId)
	c.Assert(err, gc.IsNil)
	// Double check that it's empty so that we don't pass the test by
	// happenstance.
	c.Assert(settings, gc.HasLen, 0)

	// Toss a few settings in.
	settings["foo"] = "bar"
	settings["baz"] = "biz"

	err = client.LeadershipSettings.Merge(s.serviceId, settings)
	c.Assert(err, gc.IsNil)

	settings, err = client.LeadershipSettings.Read(s.serviceId)
	c.Assert(err, gc.IsNil)
	c.Check(settings["foo"], gc.Equals, "bar")
	c.Check(settings["baz"], gc.Equals, "biz")
}

func (s *uniterLeadershipSuite) TestSettingsChangeNotifier(c *gc.C) {

	// First, the unit must be elected leader; otherwise merges will be denied.
	leadershipClient := leadership.NewClient(s.clientFacade, s.facadeCaller)
	defer func() { err := leadershipClient.Close(); c.Assert(err, gc.IsNil) }()
	err := leadershipClient.ClaimLeadership(s.serviceId, s.unitId, 10*time.Second)
	c.Assert(err, gc.IsNil)

	client := uniter.NewState(s.facadeCaller.RawAPICaller(), names.NewUnitTag(s.unitId))

	// Listen for changes
	readyForChanges := make(chan struct{})
	sawChanges := make(chan struct{})
	go func() {
		watcher, err := client.LeadershipSettings.WatchLeadershipSettings(s.serviceId)
		c.Assert(err, gc.IsNil)

		// Ignore the initial event
		<-watcher.Changes()
		readyForChanges <- struct{}{}

		if change, ok := <-watcher.Changes(); ok {
			sawChanges <- change
		} else {
			c.Fatalf("watcher failed to send a change: %s", watcher.Err())
		}
	}()

	select {
	case <-readyForChanges:
	case <-time.After(coretesting.ShortWait):
		c.Fatalf("timed out")
	}

	c.Log("Writing changes...")
	err = client.LeadershipSettings.Merge(s.serviceId, map[string]string{"foo": "bar"})
	c.Assert(err, gc.IsNil)

	c.Log("Waiting to see that watcher saw changes...")
	notifyAsserter := coretesting.NotifyAsserterC{C: c, Chan: sawChanges}
	notifyAsserter.AssertOneReceive()

	settings, err := client.LeadershipSettings.Read(s.serviceId)
	c.Assert(err, gc.IsNil)

	c.Check(settings["foo"], gc.Equals, "bar")
}

func (s *uniterLeadershipSuite) SetUpTest(c *gc.C) {

	s.AgentSuite.SetUpTest(c)

	file, _ := ioutil.TempFile("", "juju-run")
	defer file.Close()
	s.AgentSuite.PatchValue(&agentcmd.JujuRun, file.Name())

	if runtime.GOOS == "windows" {
		s.AgentSuite.PatchValue(&agentcmd.EnableJournaling, false)
	}

	fakeEnsureMongo := agenttesting.FakeEnsure{}
	s.AgentSuite.PatchValue(&cmdutil.EnsureMongoServer, fakeEnsureMongo.FakeEnsureMongo)

	s.factory = factory.NewFactory(s.State)

	// Create a machine to manage the environment, and set all
	// passwords to something known.
	stateServer, password := s.factory.MakeMachineReturningPassword(c, &factory.MachineParams{
		InstanceId: "id-1",
		Nonce:      agent.BootstrapNonce,
		Jobs:       []state.MachineJob{state.JobManageEnviron},
	})
	c.Assert(stateServer.PasswordValid(password), gc.Equals, true)
	c.Assert(stateServer.SetMongoPassword(password), gc.IsNil)

	// Create a machine to host some units.
	unitHostMachine := s.factory.MakeMachine(c, &factory.MachineParams{
		Nonce:    agent.BootstrapNonce,
		Password: password,
	})

	// Create a service and an instance of that service so that we can
	// create a client.
	service := s.factory.MakeService(c, &factory.ServiceParams{})
	s.serviceId = service.Tag().Id()

	unit := s.factory.MakeUnit(c, &factory.UnitParams{Machine: unitHostMachine, Service: service})
	s.unitId = unit.UnitTag().Id()

	c.Assert(unit.SetPassword(password), gc.IsNil)
	unitState := s.OpenAPIAs(c, unit.Tag(), password)

	// Create components needed to construct a client.
	s.clientFacade, s.facadeCaller = base.NewClientFacade(unitState, leadershipapi.FacadeName)
	c.Assert(s.clientFacade, gc.NotNil)
	c.Assert(s.facadeCaller, gc.NotNil)

	// Tweak and write out the config file for the state server.
	writeStateAgentConfig(
		c,
		s.MongoInfo(c),
		s.DataDir(),
		names.NewMachineTag(stateServer.Id()),
		s.State.EnvironTag(),
		password,
		version.Current,
	)

	// Create & start a machine agent so the tests have something to call into.
	agentConf := agentcmd.AgentConf{DataDir: s.DataDir()}
	machineAgentFactory := agentcmd.MachineAgentFactoryFn(&agentConf, &agentConf)
	s.machineAgent = machineAgentFactory(stateServer.Id())

	// See comment in createMockJujudExecutable
	if runtime.GOOS == "windows" {
		dirToRemove := createMockJujudExecutable(c, s.DataDir(), s.machineAgent.Tag().String())
		s.AddCleanup(func(*gc.C) { os.RemoveAll(dirToRemove) })
	}

	c.Log("Starting machine agent...")
	go func() {
		err := s.machineAgent.Run(coretesting.Context(c))
		c.Assert(err, gc.IsNil)
	}()
}

// When a machine agent is ran it creates a symlink to the jujud executable.
// Since we cannot create symlinks to a non-existent file on windows,
// we place a dummy executable in the expected location.
func createMockJujudExecutable(c *gc.C, dir, tag string) string {
	toolsDir := filepath.Join(dir, "tools")
	err := os.MkdirAll(filepath.Join(toolsDir, tag), 0755)
	c.Assert(err, gc.IsNil)
	err = ioutil.WriteFile(filepath.Join(toolsDir, tag, "jujud.exe"),
		[]byte("echo 1"), 0777)
	c.Assert(err, gc.IsNil)
	return toolsDir
}

func (s *uniterLeadershipSuite) TearDownTest(c *gc.C) {
	c.Log("Stopping machine agent...")
	err := s.machineAgent.Stop()
	c.Assert(err, gc.IsNil)

	s.AgentSuite.TearDownTest(c)
}

func writeStateAgentConfig(
	c *gc.C,
	stateInfo *mongo.MongoInfo,
	dataDir string,
	tag names.Tag,
	environTag names.EnvironTag,
	password string,
	vers version.Binary,
) agent.ConfigSetterWriter {

	port := gitjujutesting.FindTCPPort()
	apiAddr := []string{fmt.Sprintf("localhost:%d", port)}
	conf, err := agent.NewStateMachineConfig(
		agent.AgentConfigParams{
			DataDir:           dataDir,
			Tag:               tag,
			Environment:       environTag,
			UpgradedToVersion: vers.Number,
			Password:          password,
			Nonce:             agent.BootstrapNonce,
			StateAddresses:    stateInfo.Addrs,
			APIAddresses:      apiAddr,
			CACert:            stateInfo.CACert,
		},
		params.StateServingInfo{
			Cert:         coretesting.ServerCert,
			PrivateKey:   coretesting.ServerKey,
			CAPrivateKey: coretesting.CAKey,
			StatePort:    gitjujutesting.MgoServer.Port(),
			APIPort:      port,
		})
	c.Assert(err, gc.IsNil)
	conf.SetPassword(password)
	c.Assert(conf.Write(), gc.IsNil)
	return conf
}
