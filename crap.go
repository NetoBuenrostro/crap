package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	ConfigurationFile = "crap.json"
	Version           = "0.5"
)

var (
	crapify = flag.Bool("crapify", false, "create a new configuration file")
	version = flag.Bool("version", false, "print version and exit")
)

func main() {
	flag.Parse()

	if *version {
		fmt.Println(Version)
		os.Exit(0)
	}

	// Create a new config file if required
	if *crapify {
		conf := NewSampleConfiguration()
		b, err := json.MarshalIndent(conf, "", "  ")
		if err != nil {
			panic(err)
		}
		if err := ioutil.WriteFile(ConfigurationFile, b, 0644); err != nil {
			panic(err)
		}
		os.Exit(0)
	}

	// Parse config
	b, err := ioutil.ReadFile(ConfigurationFile)
	if err != nil {
		fmt.Println("Could not open configuration file (crap.json):", err)
		fmt.Println("Hint: pass --crapify to create a new configuration file")
		os.Exit(1)
	}
	var conf Configuration
	if err := json.Unmarshal(b, &conf); err != nil {
		panic(err)
	}

	// Select environment
	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("Specify an environment")
		os.Exit(1)
	}
	var env *Environment
	for _, e := range conf.Environments {
		if e.Name == args[0] {
			env = &e
			break
		}
	}
	if env == nil {
		fmt.Println("Environment", args[0], "not found")
		os.Exit(1)
	}

	// Validate config
	if len(env.Servers) == 0 {
		fmt.Println("No server(s) in environment configuration")
		os.Exit(1)
	}
	if len(conf.AssetBuildCommands) == 0 && len(conf.AppBuildCommands) == 0 {
		fmt.Println("No asset_build_commands or app_build_commands found in environment configuration")
		os.Exit(1)
	}
	if len(env.DeployDir) == 0 {
		fmt.Println("deploydir must be filled in!")
		os.Exit(1)
	}
	if len(conf.CrapVersion) == 0 {
		fmt.Println("Your crap.json file is unversioned - please add crap_version to your crap.json")
		os.Exit(1)
	}
	if conf.CrapVersion != Version {
		fmt.Println("Your crap.json requires crap", conf.CrapVersion, "but this crap is", Version, "- please upgrade!")
		os.Exit(1)
	}

	// Kick of SSH ControlMaster in the background
	controlMasterStarted := make(chan *Server, len(env.Servers))
	startControlMaster := func(server *Server) {
		cmd := fmt.Sprintf("ssh -nNf -o ControlMaster=yes -o ControlPath='%s' -p %s %s", server.ControlPath(), server.Port, server.Host())
		runCmd(exec.Command("sh", "-c", cmd))
		controlMasterStarted <- server
	}
	for _, server := range env.Servers {
		go startControlMaster(&server)
	}
	defer func() {
		controlMasterStopped := make(chan *Server, len(env.Servers))
		stopControlMaster := func(server *Server) {
			cmd := fmt.Sprintf("ssh -O exit -o ControlPath='%s' -p %s %s", server.ControlPath(), server.Port, server.Host())
			runCmd(exec.Command("sh", "-c", cmd))
			controlMasterStopped <- server
		}
		for _, server := range env.Servers {
			go stopControlMaster(&server)
		}
		for _ = range env.Servers {
			<-controlMasterStopped
		}
	}()

	// Start building in the background
	buildOne := func(buildCommand string, ready chan bool) {
		runCmd(exec.Command("sh", "-c", buildCommand))
		ready <- true
	}
	buildAll := func(buildCommands []string) chan bool {
		ready := make(chan bool, len(buildCommands))
		for _, buildCommand := range buildCommands {
			go buildOne(buildCommand, ready)
		}
		return ready
	}
	appBuildReady := buildAll(conf.AppBuildCommands)
	assetBuildReady := buildAll(conf.AssetBuildCommands)

	// Collect local git repo info
	repoAddress := ""
	deployBranch := ""
	if conf.BuiltAppDir == "" {
		b = runCmd(exec.Command("git", "config", "--get", "remote.origin.url"))
		repoAddress = strings.Split(string(b), "\n")[0]
		b = runCmd(exec.Command("git", "branch"))
		for _, branch := range strings.Split(string(b), "\n") {
			s := strings.TrimSpace(branch)
			if strings.HasPrefix(s, "* ") {
				deployBranch = s[2:]
			}
		}
	}

	// Construct release dir
	releaseBasePath := filepath.Join(env.DeployDir, "releases")
	releaseDir := filepath.Join(releaseBasePath, time.Now().Format("20060102150405"))

	// Prepare servers
	serverPrepared := make(chan *Server, len(env.Servers))
	go func() {
		sha1 := ""
		if conf.BuiltAppDir == "" {
			b = runCmd(exec.Command("git", "ls-remote", repoAddress, deployBranch))
			sha1 = strings.Split(string(b), "\t")[0]
		}

		// Run a bunch of commands on the remote server. Set up shared dir, symlinks etc
		var buffer bytes.Buffer

		cmd := fmt.Sprintf("if [ ! -d %s/shared/log ]; then mkdir -p %s/shared/log; fi", env.DeployDir, env.DeployDir)
		buffer.WriteString(cmd)

		if conf.BuiltAppDir == "" {
			cmd := fmt.Sprintf("&& if [ -d %s/shared/cached-copy ]; then cd %s/shared/cached-copy && git fetch -q origin && git fetch --tags -q origin && git reset -q --hard %s && git clean -q -d -x -f; else git clone -q --depth 1 %s %s/shared/cached-copy && cd %s/shared/cached-copy && git checkout -q -b deploy %s; fi",
				env.DeployDir, env.DeployDir, sha1, repoAddress, env.DeployDir, env.DeployDir, sha1)
			buffer.WriteString(cmd)
		}

		cmd = fmt.Sprintf(" && if [ ! -d %s ]; then mkdir -p %s; fi", releaseDir, releaseDir)
		buffer.WriteString(cmd)

		if conf.BuiltAppDir == "" {
			cmd = fmt.Sprintf(" && cp -RPp %s/shared/cached-copy %s && (echo %s > %s/REVISION)",
				env.DeployDir, releaseDir, sha1, releaseDir)
			buffer.WriteString(cmd)
		}

		cmd = fmt.Sprintf(" && chmod -R g+w %s", releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && (rm -rf %s/public/system || true) && mkdir -p %s/public/", releaseDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && ln -s %s/shared/system %s/public/system", env.DeployDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && rm -rf %s/log", releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && ln -s %s/shared/log %s/log", env.DeployDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && rm -rf %s/tmp/pids && mkdir -p %s/tmp/", releaseDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && ln -s %s/shared/pids %s/tmp/pids", env.DeployDir, releaseDir)
		buffer.WriteString(cmd)

		if len(conf.AssetBuildCommands) > 0 {
			cmd = fmt.Sprintf(" && rm -rf %s/public/assets && mkdir -p %s/public && mkdir -p %s/shared/assets && ln -s %s/shared/assets %s/public/assets",
				releaseDir, releaseDir, env.DeployDir, env.DeployDir, releaseDir)
			buffer.WriteString(cmd)
		}

		prepareServer := func(server *Server) {
			runCmd(exec.Command("ssh", "-p", server.Port, "-o", fmt.Sprintf("ControlPath='%s'", server.ControlPath()), "-l", server.User, server.Ip, buffer.String()))
			serverPrepared <- server
		}
		for _, server := range env.Servers {
			go prepareServer(&server)
		}
	}()

	// Block until servers are prepared
	// FIXME: servers should not block each other
	for _ = range env.Servers {
		<-serverPrepared
	}

	// Rsync assets in the background
	assetsRsynced := make(chan *Server, len(env.Servers))
	go func() {
		// Block until all assets built
		for _ = range conf.AssetBuildCommands {
			<-assetBuildReady
		}
		rsyncAssets := func(server *Server) {
			if len(conf.AssetBuildCommands) > 0 {
				cmd := fmt.Sprintf("rsync -e 'ssh -p %s -o ControlPath=\"%s\"' --recursive --times --compress --human-readable public/assets %s:%s/shared",
					server.Port, server.ControlPath(), server.Host(), env.DeployDir)
				runCmd(exec.Command("/bin/sh", "-c", cmd))
			}
			assetsRsynced <- server
		}
		for _, server := range env.Servers {
			go rsyncAssets(&server)
		}
	}()

	// Upload app in the background
	appUploaded := make(chan *Server, len(env.Servers))
	go func() {
		for _ = range conf.AppBuildCommands {
			<-appBuildReady
		}
		if conf.BuiltAppDir != "" {
			cmd := fmt.Sprintf("pbzip2 -9 -f %s", filepath.Join(conf.BuiltAppDir, "*"))
			runCmd(exec.Command("/bin/sh", "-c", cmd))
		}
		uploadCompressedApp := func(server *Server) {
			if conf.BuiltAppDir != "" {
				packedFiles := filepath.Join(conf.BuiltAppDir, "*.bz2")
				cmd := fmt.Sprintf("scp -o ControlPath=\"%s\" %s %s:%s", server.ControlPath(), packedFiles, server.Host(), releaseDir)
				runCmd(exec.Command("/bin/sh", "-c", cmd))
			}
			appUploaded <- server
		}
		for _, server := range env.Servers {
			go uploadCompressedApp(&server)
		}
	}()

	// If remote commands are finished and assets are synced, replace symlink and restart server
	var finalize bytes.Buffer

	symlink := filepath.Join(env.DeployDir, "current")
	finalize.WriteString(fmt.Sprintf("rm -f %s", symlink))
	finalize.WriteString(fmt.Sprintf(" && ln -s %s %s", releaseDir, symlink))

	if conf.BuiltAppDir != "" {
		cmd := fmt.Sprintf(" && pbzip2 -d %s", filepath.Join(releaseDir, "*.bz2"))
		finalize.WriteString(cmd)
	}

	if len(env.RestartCommand) > 0 {
		cmd := fmt.Sprintf(" && %s", env.RestartCommand)
		finalize.WriteString(cmd)
	}

	serverFinalized := make(chan *Server, len(env.Servers))
	finalizeServer := func(server *Server) {
		<-appUploaded
		<-assetsRsynced
		runCmd(exec.Command("ssh", "-p", server.Port, "-o", fmt.Sprintf("ControlPath='%s'", server.ControlPath()), "-l", server.User, server.Ip, finalize.String()))
		serverFinalized <- server
	}
	for _, server := range env.Servers {
		go finalizeServer(&server)
	}
	for _ = range env.Servers {
		<-serverFinalized
	}
}

func runCmd(cmd *exec.Cmd) []byte {
	cmdStart := time.Now()
	args := strings.Join(cmd.Args, " ")
	b, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Println()
		fmt.Println()
		fmt.Println("Error:", err)
		fmt.Println("Command:", args)
		fmt.Println("Output:", string(b))
		os.Exit(1)
	}
	fmt.Println(args, "in", time.Since(cmdStart))
	return b
}
