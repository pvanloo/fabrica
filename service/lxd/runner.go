package lxd

import (
	"bytes"
	"encoding/base64"
	"fmt"
	lxd "github.com/lxc/lxd/client"
	"github.com/lxc/lxd/shared/api"
	"github.com/ogra1/fabrica/datastore"
	"github.com/ogra1/fabrica/service"
	"github.com/ogra1/fabrica/service/system"
	"github.com/ogra1/fabrica/service/writecloser"
	"io"
	"log"
	"os"
	"path"
	"strings"
)

var containerEnv = map[string]string{
	"FLASH_KERNEL_SKIP":           "true",
	"DEBIAN_FRONTEND":             "noninteractive",
	"TERM":                        "xterm",
	"SNAPCRAFT_BUILD_ENVIRONMENT": "host",
}

var containerCmd = [][]string{
	{"apt", "update"},
	{"apt", "-y", "upgrade"},
	{"apt", "-y", "install", "build-essential"},
	{"apt", "-y", "clean"},
	{"snap", "install", "snapcraft", "--classic"},
}

// runner services to run one build in LXD
type runner struct {
	BuildID    string
	Datastore  datastore.Datastore
	SystemSrv  system.Srv
	Connection lxd.InstanceServer
}

// newRunner creates a new LXD runner
func newRunner(buildID string, ds datastore.Datastore, sysSrv system.Srv, c lxd.InstanceServer) *runner {
	return &runner{
		BuildID:    buildID,
		Datastore:  ds,
		SystemSrv:  sysSrv,
		Connection: c,
	}
}

// runBuild launches an LXD container to start the build
func (run *runner) runBuild(name, repo, branch, keyID, distro string) error {
	log.Println("Run build:", name, repo, distro)
	log.Println("Creating and starting container")
	run.Datastore.BuildLogCreate(run.BuildID, "milestone: Creating and starting container")

	// Check if we're in debug mode (retaining the container on error)
	debug := run.SystemSrv.SnapCtlGetBool("debug")

	// Generate the container name and store it in the database
	cname := containerName(name)
	run.Datastore.BuildUpdateContainer(run.BuildID, cname)

	// Create and start the LXD
	run.Datastore.BuildLogCreate(run.BuildID, "milestone: Create container "+cname)
	if err := run.createAndStartContainer(cname, distro); err != nil {
		log.Println("Error creating/starting container:", err)
		run.Datastore.BuildLogCreate(run.BuildID, err.Error())
		return err
	}

	// Set up the database writer for the logs
	dbWC := writecloser.NewDBWriteCloser(run.BuildID, run.Datastore)

	// Wait for the network to be running
	run.Datastore.BuildLogCreate(run.BuildID, "milestone: Waiting for the network")
	run.waitForNetwork(cname)
	run.Datastore.BuildLogCreate(run.BuildID, "milestone: Network is ready")

	// Copy the ssh_config file to the container as .ssh/config
	// This has defaults for ssh to handle network issues with accessing various git vendors
	if err := run.setSSHConfig(cname, dbWC); err != nil {
		log.Println("Error creating ssh config:", err)
		run.Datastore.BuildLogCreate(run.BuildID, err.Error())
		return err
	}

	// Create the ssh private key file in the container, if needed
	passwordNeeded, err := run.setSSHKey(cname, keyID)
	if err != nil {
		log.Println("Error creating ssh key:", err)
		run.Datastore.BuildLogCreate(run.BuildID, err.Error())
		return err
	}

	// Create script to clone the repo
	if err := run.cloneRepoScript(cname, repo, branch, passwordNeeded); err != nil {
		run.Datastore.BuildLogCreate(run.BuildID, err.Error())
		return err
	}

	// Install the pre-requisites in the container and clone the repo
	// The 'clone' script handles the ssh key and any password
	commands := containerCmd
	commands = append(containerCmd, []string{"/root/clone"})

	run.Datastore.BuildLogCreate(run.BuildID, "milestone: Install dependencies")
	for _, cmd := range commands {
		run.Datastore.BuildLogCreate(run.BuildID, "milestone: "+strings.Join(cmd, " "))
		if err := run.runInContainer(cname, cmd, "", dbWC); err != nil {
			log.Println("Command error:", cmd, err)
			run.Datastore.BuildLogCreate(run.BuildID, err.Error())
			if !debug {
				run.deleteContainer(cname)
			}
			return err
		}
	}

	// Set up the download writer for the snap build
	dwnWC := writecloser.NewDownloadWriteCloser(run.BuildID, run.Datastore)

	// Run the build using snapcraft
	cmd := []string{"snapcraft"}
	run.Datastore.BuildLogCreate(run.BuildID, "milestone: Build snap")
	if err := run.runInContainer(cname, cmd, "/root/"+name, dwnWC); err != nil {
		log.Println("Command error:", cmd, err)
		run.Datastore.BuildLogCreate(run.BuildID, err.Error())
		if !debug {
			run.deleteContainer(cname)
		}
		return err
	}

	// Download the file from the container
	run.Datastore.BuildLogCreate(run.BuildID, fmt.Sprintf("milestone: Download file %s", dwnWC.Filename()))
	downloadPath, err := run.copyFile(cname, name, dwnWC.Filename())
	if err != nil {
		log.Println("Copy error:", cmd, err)
		run.Datastore.BuildLogCreate(run.BuildID, err.Error())
		if !debug {
			run.deleteContainer(cname)
		}
		return err
	}
	run.Datastore.BuildUpdateDownload(run.BuildID, downloadPath)

	// Remove the container on successful completion
	return run.deleteContainer(cname)
}

// setSSHKey sets up the ssh key in the
func (run *runner) setSSHKey(cname, keyID string) (bool, error) {
	if keyID == "" {
		return false, nil
	}

	// Get the ssh key
	key, err := run.Datastore.KeysGet(keyID)
	if err != nil {
		log.Println("Error fetching ssh key:", err)
		return false, err
	}

	// Decode the base64-encoded data
	data, err := base64.StdEncoding.DecodeString(key.Data)
	if err != nil {
		log.Println("Error decoding ssh key:", err)
		return false, err
	}

	// Add the ssh key to the container
	if err := run.Connection.CreateContainerFile(cname, "/root/.ssh/id_rsa", lxd.ContainerFileArgs{
		Content: bytes.NewReader(data),
		Mode:    0600,
	}); err != nil {
		return false, fmt.Errorf("error copying ssh key: %v", err)
	}

	// Don't need ssh-agent when there is no password on the ssh key, but need a dummy script
	if key.Password == "" {
		return false, nil
	}

	// Write the ssh-key password to a file
	if err := run.Connection.CreateContainerFile(cname, "/root/.password", lxd.ContainerFileArgs{
		Content: strings.NewReader(fmt.Sprintf("echo \"%s\"", key.Password)),
		Mode:    0700,
	}); err != nil {
		return true, fmt.Errorf("error saving ssh key password: %v", err)
	}

	return true, nil
}

// setSSHConfig copies the ssh config file to handle ssh defaults for git vendors
func (run *runner) setSSHConfig(cname string, stdOutErr io.WriteCloser) error {
	// Create the /root/.ssh directory
	if err := run.runInContainer(cname, []string{"mkdir", "-p", "/root/.ssh"}, "", stdOutErr); err != nil {
		return fmt.Errorf("error creating .ssh: %v", err)
	}
	if err := run.runInContainer(cname, []string{"chmod", "700", "/root/.ssh"}, "", stdOutErr); err != nil {
		return fmt.Errorf("error setting .ssh permissions: %v", err)
	}

	// Get our own config file (provided by the snapcraft layout)
	f, err := os.Open("/etc/ssh/ssh_config")
	if err != nil {
		return fmt.Errorf("error reading /etc/ssh/ssh_config: %v", err)
	}
	defer f.Close()

	// Add the ssh config file to the container
	return run.Connection.CreateContainerFile(cname, "/root/.ssh/config", lxd.ContainerFileArgs{
		Content: f,
		Mode:    0644,
	})
}

// cloneRepoScript creates a script to clone the repo, using ssh-agent to handle keys that need a passphrase
func (run *runner) cloneRepoScript(cname, repo, branch string, passwordNeeded bool) error {
	var clone string
	if passwordNeeded {
		clone = strings.Join(
			[]string{"#!/bin/sh",
				"eval `ssh-agent`",
				"export DISPLAY=0",
				"export SSH_ASKPASS=/root/.password",
				"cat /root/.ssh/id_rsa | ssh-add -",
				"git clone -b " + branch + " --progress " + repo,
			}, "\n")
	} else {
		clone = strings.Join(
			[]string{"#!/bin/sh",
				"git clone -b " + branch + " --progress " + repo,
			}, "\n")
	}

	if err := run.Connection.CreateContainerFile(cname, "/root/clone", lxd.ContainerFileArgs{
		Content: strings.NewReader(clone),
		Mode:    0700,
	}); err != nil {
		return fmt.Errorf("error saving agent script: %v", err)
	}
	return nil
}

func (run *runner) deleteContainer(cname string) error {
	run.Datastore.BuildLogCreate(run.BuildID, fmt.Sprintf("milestone: Removing container %s", cname))
	if err := stopAndDeleteContainer(run.Connection, cname); err != nil {
		run.Datastore.BuildLogCreate(run.BuildID, err.Error())
		return err
	}
	return nil
}

func (run *runner) createAndStartContainer(cname, distro string) error {
	// Container creation request
	req := api.ContainersPost{
		Name: cname,
		Source: api.ContainerSource{
			Type:  "image",
			Alias: "fabrica-" + distro,
		},
	}

	// Get LXD to create the container (background operation)
	op, err := run.Connection.CreateContainer(req)
	if err != nil {
		return err
	}

	// Wait for the operation to complete
	err = op.Wait()
	if err != nil {
		return err
	}

	// Get LXD to start the container (background operation)
	reqState := api.ContainerStatePut{
		Action:  "start",
		Timeout: -1,
	}

	op, err = run.Connection.UpdateContainerState(cname, reqState, "")
	if err != nil {
		return err
	}

	// Wait for the operation to complete
	return op.Wait()
}

func (run *runner) waitForNetwork(cname string) {
	// Set up the writer to check for a message
	wc := writecloser.NewFlagWriteCloser("PING")

	// Run a command in the container
	log.Println("Waiting for network...")
	cmd := []string{"ping", "-c1", "8.8.8.8"}
	for {
		_ = run.runInContainer(cname, cmd, "", wc)
		if wc.Found() {
			break
		}
	}
}

func (run *runner) runInContainer(cname string, command []string, cwd string, stdOutErr io.WriteCloser) error {
	useDir := "/root"
	if cwd != "" {
		useDir = cwd
	}

	// Setup the exec request
	req := api.ContainerExecPost{
		Environment: containerEnv,
		Command:     command,
		WaitForWS:   true,
		Cwd:         useDir,
	}

	// Setup the exec arguments (fds)
	args := lxd.ContainerExecArgs{
		Stdout: stdOutErr,
		Stderr: stdOutErr,
	}

	// Get the current state
	op, err := run.Connection.ExecContainer(cname, req, &args)
	if err != nil {
		return err
	}

	// Wait for it to complete
	return op.Wait()
}
func (run *runner) copyFile(cname, name, filePath string) (string, error) {
	// Generate the source path
	inFile := path.Join("/root", name, filePath)

	// Generate the destination path
	p := service.GetPath(run.BuildID)
	_ = os.MkdirAll(p, os.ModePerm)
	destFile := path.Join(p, path.Base(filePath))
	outFile, err := os.Create(destFile)
	if err != nil {
		return "", fmt.Errorf("error creating snap file: %v", err)
	}

	// Get the snap file from the container
	log.Println("Copy file from:", inFile)
	content, _, err := run.Connection.GetContainerFile(cname, inFile)
	if err != nil {
		return "", fmt.Errorf("error fetching snap file: %v", err)
	}
	defer content.Close()

	// Copy the file
	_, err = io.Copy(outFile, content)
	return destFile, err
}

// stopAndDeleteContainer stops and removes the container
func stopAndDeleteContainer(c lxd.InstanceServer, cname string) error {
	// Get LXD to start the container (background operation)
	reqState := api.ContainerStatePut{
		Action:  "stop",
		Timeout: -1,
	}

	op, err := c.UpdateContainerState(cname, reqState, "")
	if err != nil {
		return err
	}

	// Wait for the operation to complete
	if err := op.Wait(); err != nil {
		return err
	}

	// Delete the container, but don't wait
	_, err = c.DeleteContainer(cname)
	return err
}
