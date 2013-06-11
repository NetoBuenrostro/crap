package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/brettbuddin/victor"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	ConfigurationFile = "crap.json"
	Version           = "0.9"
)

var (
	crapify = flag.Bool("crapify", false, "create a new configuration file")
	version = flag.Bool("version", false, "print version and exit")
	verbose = flag.Bool("verbose", false, "verbose logging")
)

func main() {
	deployStart := time.Now()

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
	if len(env.Servers) != 1 {
		fmt.Println("No server(s) in environment configuration. Please specify one!")
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

	runtime.GOMAXPROCS(runtime.NumCPU())
	if *verbose {
		log.Println("GOMAXPROCS", runtime.GOMAXPROCS(0))
	}

	cmd := "if [ ! -d out ]; then mkdir -p out; fi"
	run("Create out dir", exec.Command("/bin/sh", "-c", cmd))

	server := env.Servers[0]

	// Kick of SSH ControlMaster in the background
	controlMasterStarted := make(chan bool)
	go func() {
		cmd := fmt.Sprintf("ssh -nNf -o \"ControlMaster=yes\" -o \"ControlPath=%s\" -p %s %s", server.Socket(), server.Port, server.Host())
		run("Start SSH ControlMaster", exec.Command("sh", "-c", cmd))
		controlMasterStarted <- true
	}()

	defer func() {
		controlMasterStopped := make(chan bool)
		go func() {
			cmd := fmt.Sprintf("ssh -O exit -S '%s' -p %s %s", server.Socket(), server.Port, server.Host())
			run("Stop SSH ControlMaster", exec.Command("sh", "-c", cmd))
			controlMasterStopped <- true
		}()

		<-controlMasterStopped
	}()

	// Start building in the background
	executeBuildCommand := func(buildCommand string, ready chan bool) {
		run("Build command: "+buildCommand, exec.Command("sh", "-c", buildCommand))
		ready <- true
	}
	buildAll := func(buildCommands []string) chan bool {
		ready := make(chan bool, len(buildCommands))
		for _, buildCommand := range buildCommands {
			go executeBuildCommand(buildCommand, ready)
		}
		return ready
	}
	appBuildReady := buildAll(conf.AppBuildCommands)
	assetBuildReady := buildAll(conf.AssetBuildCommands)

	// Construct release dir
	releaseBasePath := filepath.Join(env.DeployDir, "releases")
	releaseDir := filepath.Join(releaseBasePath, time.Now().Format("20060102150405"))

	// Prepare servers
	serverPrepared := make(chan bool)
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

		run("Prepare server", exec.Command("ssh", "-p", server.Port, "-S", server.Socket(), "-l", server.User, server.Ip, buffer.String()))
		serverPrepared <- true
	}()

	<-controlMasterStarted
	<-serverPrepared

	// Rsync assets in the background
	assetsRsynced := make(chan bool)
	go func() {
		for _ = range conf.AssetBuildCommands {
			<-assetBuildReady
		}
		if len(conf.AssetBuildCommands) > 0 {
			cmd := fmt.Sprintf("rsync -e 'ssh -p %s -S \"%s\"' --recursive --times --compress --human-readable public/assets %s:%s/shared",
				server.Port, server.Socket(), server.Host(), env.DeployDir)
			run("Rsync assets", exec.Command("/bin/sh", "-c", cmd))
		}
		assetsRsynced <- true
	}()

	// Build app
	appBuilt := make(chan bool)
	go func() {
		for _ = range conf.AppBuildCommands {
			<-appBuildReady
		}
		appBuilt <- true
	}()

	// Rsync app files to sever
	appUploaded := make(chan bool)
	go func() {
		<-appBuilt
		cmd := fmt.Sprintf("rsync -e 'ssh -p %s -S \"%s\"' --recursive --times --compress --human-readable dist %s:%s/shared",
			server.Port, server.Socket(), server.Host(), env.DeployDir)
		run("Rsync app files", exec.Command("/bin/sh", "-c", cmd))
		appUploaded <- true
	}()

	<-appUploaded
	<-assetsRsynced

	// Finalize deploy
	appFinalized := make(chan bool)
	go func() {
		var finalize bytes.Buffer

		// Copy app files into release dir
		cmd = fmt.Sprintf("cp -r %s %s", filepath.Join(env.DeployDir, "shared", "dist", "*"), releaseDir)
		finalize.WriteString(cmd)

		// Replace symlink
		symlink := filepath.Join(env.DeployDir, "current")
		finalize.WriteString(fmt.Sprintf(" && rm -f %s", symlink))
		finalize.WriteString(fmt.Sprintf(" && ln -s %s %s", releaseDir, symlink))

		if len(env.RestartCommand) > 0 {
			cmd := fmt.Sprintf(" && (%s)", env.RestartCommand)
			finalize.WriteString(cmd)
		}

		run("Finalize server", exec.Command("ssh", "-p", server.Port, "-S", fmt.Sprintf("'%s'", server.Socket()), "-l", server.User, server.Ip, finalize.String()))

		appFinalized <- true
	}()

	<-appFinalized

	if len(env.AfterDeployCommand) > 0 {
		run("After deploy hook", exec.Command("sh", "-c", env.AfterDeployCommand))
	}

	deployDuration := time.Since(deployStart)
	log.Println("App deployed to", env.Name, "in", deployDuration)

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
					currentUser.Username, filepath.Base(pwd), env.Name, deployDuration))
			}
		}
	}
}

func run(label string, cmd *exec.Cmd) {
	cmdStart := time.Now()
	if *verbose {
		args := strings.Join(cmd.Args, " ")
		log.Println(label, "args:", args)
	}
	if err := cmd.Run(); err != nil {
		args := strings.Join(cmd.Args, " ")
		log.Println("Error!", label, args, err.Error())
		os.Exit(1)
	}
	since := time.Since(cmdStart)
	log.Println(label, "("+since.String()+")")
}
