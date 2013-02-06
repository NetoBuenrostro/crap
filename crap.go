package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/brettbuddin/victor"
	"github.com/koyachi/go-term-ansicolor/ansicolor"
	"io/ioutil"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	ConfigurationFile = "crap.json"
	Version           = "0.7"
)

var (
	crapify = flag.Bool("crapify", false, "create a new configuration file")
	version = flag.Bool("version", false, "print version and exit")
)

func main() {
	start := time.Now()

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
		cmd := fmt.Sprintf("ssh -nNf -o \"ControlMaster=yes\" -o \"ControlPath=%s\" -p %s %s", server.Socket(), server.Port, server.Host())
		runCmdReturningNothing(exec.Command("sh", "-c", cmd))
		controlMasterStarted <- server
	}
	for _, server := range env.Servers {
		go startControlMaster(&server)
	}

	// FIXME: at this point, we don't really know if control master was set up or not
	defer func() {
		controlMasterStopped := make(chan *Server, len(env.Servers))
		stopControlMaster := func(server *Server) {
			cmd := fmt.Sprintf("ssh -O exit -S '%s' -p %s %s", server.Socket(), server.Port, server.Host())
			runCmdReturningNothing(exec.Command("sh", "-c", cmd))
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
		runCmdReturningNothing(exec.Command("sh", "-c", buildCommand))
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

	// Construct release dir
	releaseBasePath := filepath.Join(env.DeployDir, "releases")
	releaseDir := filepath.Join(releaseBasePath, time.Now().Format("20060102150405"))

	// Prepare servers
	serverPrepared := make(chan *Server, len(env.Servers))
	go func() {
		// Run a bunch of commands on the remote server. Set up shared dir, symlinks etc
		var buffer bytes.Buffer

		cmd := fmt.Sprintf("if [ ! -d %s/shared/log ]; then mkdir -p %s/shared/log; fi", env.DeployDir, env.DeployDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && if [ ! -d %s ]; then mkdir -p %s; fi", releaseDir, releaseDir)
		buffer.WriteString(cmd)

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

		// FIXME: servers should not block each other
		for _ = range env.Servers {
			<-controlMasterStarted
		}

		prepareServer := func(server *Server) {
			runCmdReturningNothing(exec.Command("ssh", "-p", server.Port, "-S", server.Socket(), "-l", server.User, server.Ip, buffer.String()))
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
				cmd := fmt.Sprintf("rsync -e 'ssh -p %s -S \"%s\"' --recursive --times --compress --human-readable public/assets %s:%s/shared",
					server.Port, server.Socket(), server.Host(), env.DeployDir)
				runCmdReturningNothing(exec.Command("/bin/sh", "-c", cmd))
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
		cmd := "if [ ! -d out ]; then mkdir -p out; fi"
		runCmdReturningNothing(exec.Command("/bin/sh", "-c", cmd))
		cmd = fmt.Sprintf("tar --use-compress-program=pbzip2 -cf out/dist.tar.bz2 %s", conf.BuiltAppDir)
		runCmdReturningNothing(exec.Command("/bin/sh", "-c", cmd))
		uploadCompressedApp := func(server *Server) {
			cmd := fmt.Sprintf("scp -o 'ControlPath=%s' out/dist.tar.bz2 %s:%s/shared/.", server.Socket(), server.Host(), env.DeployDir)
			runCmdReturningNothing(exec.Command("/bin/sh", "-c", cmd))
			appUploaded <- server
		}
		for _, server := range env.Servers {
			go uploadCompressedApp(&server)
		}
	}()

	// If remote commands are finished and assets are synced, replace symlink and restart server
	var finalize bytes.Buffer

	cmd := fmt.Sprintf("pbzip2 -f --keep -d %s", filepath.Join(env.DeployDir, "shared", "dist.tar.bz2"))
	finalize.WriteString(cmd)

	cmd = fmt.Sprintf(" && rm -rf %s", filepath.Join(env.DeployDir, "shared", "dist"))
	finalize.WriteString(cmd)

	cmd = fmt.Sprintf(" && tar xf %s --directory=%s", filepath.Join(env.DeployDir, "shared", "dist.tar"), filepath.Join(env.DeployDir, "shared"))
	finalize.WriteString(cmd)

	cmd = fmt.Sprintf(" && cp -r %s %s", filepath.Join(env.DeployDir, "shared", "dist", "*"), releaseDir)
	finalize.WriteString(cmd)

	symlink := filepath.Join(env.DeployDir, "current")
	finalize.WriteString(fmt.Sprintf(" && rm -f %s", symlink))
	finalize.WriteString(fmt.Sprintf(" && ln -s %s %s", releaseDir, symlink))

	if len(env.RestartCommand) > 0 {
		cmd := fmt.Sprintf(" && (%s)", env.RestartCommand)
		finalize.WriteString(cmd)
	}

	serverFinalized := make(chan *Server, len(env.Servers))
	finalizeServer := func(server *Server) {
		<-appUploaded
		<-assetsRsynced
		runCmdReturningNothing(exec.Command("ssh", "-p", server.Port, "-S", fmt.Sprintf("'%s'", server.Socket()), "-l", server.User, server.Ip, finalize.String()))
		serverFinalized <- server
	}
	for _, server := range env.Servers {
		go finalizeServer(&server)
	}
	for _ = range env.Servers {
		<-serverFinalized
	}

	if len(env.AfterDeployCommand) > 0 {
		runCmdReturningNothing(exec.Command("sh", "-c", env.AfterDeployCommand))
	}

	if conf.Campfire.Account != "" && conf.Campfire.Token != "" && conf.Campfire.Rooms != "" {
		if currentUser, err := user.Current(); err != nil {
			panic(err)
		} else if pwd, err := os.Getwd(); err != nil {
			panic(err)
		} else {
			rooms := make([]int, 0)
			for _, s := range strings.Split(conf.Campfire.Rooms, ",") {
				if id, err := strconv.Atoi(s); err != nil {
					panic(err)
				} else {
					rooms = append(rooms, id)
				}
			}
			r := victor.NewCampfire("victor", conf.Campfire.Account, conf.Campfire.Token, rooms)
			for _, id := range rooms {
				r.Client().Room(id).Say(fmt.Sprintf("%s deployed %s to %s in %v",
					currentUser.Username, filepath.Base(pwd), env.Name, time.Since(start)))
			}
		}
	}
}

func runCmdReturningNothing(cmd *exec.Cmd) {
	cmdStart := time.Now()
	args := strings.Join(cmd.Args, " ")
	if err := cmd.Run(); err != nil {
		fmt.Println(ansicolor.Red(args), ansicolor.Bold(err.Error()))
		os.Exit(1)
	}
	fmt.Println(ansicolor.Green(args), ansicolor.Bold(time.Since(cmdStart).String()))
}
