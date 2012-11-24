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

type (
	Server struct {
		Port string `json:"port"`
		User string `json:"user"`
		Ip   string `json:"ip"`
	}
	Environment struct {
		Name           string   `json:"name"`
		Servers        []Server `json:"servers"`
		RestartCommand string   `json:"restart_command"`
	}
	Configuration struct {
		Environments       []Environment `json:"environments"`
		DeployDir          string        `json:"deploydir"`
		BuiltAppDir        string        `json:"built_app_dir"`
		AppBuildCommands   []string      `json:"app_build_commands"`
		AssetBuildCommands []string      `json:"asset_build107_commands"`
	}
)

// FIXME: add test cases

const (
	ConfigurationFile = "crap.json"
	Version           = "0.2"
)

var (
	crapify = flag.Bool("crapify", false, "create an new configuration file")
	version = flag.Bool("version", false, "print version and exit")
)

func main() {
	start := time.Now()

	flag.Parse()

	// Print version and exit
	if *version {
		fmt.Printf("%v\n", Version)
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
		fmt.Printf("Could not open configuration file (crap.json): %v\n", err)
		fmt.Printf("Hint: pass --crapify to create a new configuration file\n")
		os.Exit(1)
	}
	var conf Configuration
	if err := json.Unmarshal(b, &conf); err != nil {
		panic(err)
	}

	// Select environment
	args := flag.Args()
	if len(args) == 0 {
		fmt.Printf("Specify an environment\n")
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
		fmt.Printf("Environment '%s' not found\n", args[0])
		os.Exit(1)
	}

	// Validate config
	if len(env.Servers) == 0 {
		fmt.Printf("No server(s) to environment configuration\n")
		os.Exit(1)
	}
	if len(conf.AssetBuildCommands) == 0 && len(conf.AppBuildCommands) == 0 {
		fmt.Printf("No asset_build_commands or app_build_commands found in environment configuration\n")
		os.Exit(1)
	}

	// Kick of SSH ControlMaster in the background
	controlMasterStarted := make(chan bool, len(env.Servers))
	startControlMaster := func(server Server) {
		cmd := fmt.Sprintf("ssh -nNf -o ControlMaster=yes -o ControlPath='%s' -p %s %s", server.ControlPath(), server.Port, server.Host())
		runCmd(exec.Command("sh", "-c", cmd))
		controlMasterStarted <- true
	}
	for _, server := range env.Servers {
		go startControlMaster(server)
	}
	defer func() {
		controlMasterStopped := make(chan bool, len(env.Servers))
		stopControlMaster := func(server Server) {
			cmd := fmt.Sprintf("ssh -O exit -o ControlPath='%s' -p %s %s", server.ControlPath(), server.Port, server.Host())
			runCmd(exec.Command("sh", "-c", cmd))
			controlMasterStopped <- true
		}
		for _, server := range env.Servers {
			go stopControlMaster(server)
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
	releaseBasePath := filepath.Join(conf.DeployDir, "releases")
	releaseDir := filepath.Join(releaseBasePath, time.Now().Format("20060102150405"))

	// Prepare servers
	serverPrepared := make(chan bool, len(env.Servers))
	go func() {
		sha1 := ""
		if conf.BuiltAppDir == "" {
			b = runCmd(exec.Command("git", "ls-remote", repoAddress, deployBranch))
			sha1 = strings.Split(string(b), "\t")[0]
		}

		// Run a bunch of commands on the remote server. Set up shared dir, symlinks etc
		var buffer bytes.Buffer

		cmd := fmt.Sprintf("if [ ! -d %s/shared/log ]; then mkdir -p %s/shared/log; fi", conf.DeployDir, conf.DeployDir)
		buffer.WriteString(cmd)

		if conf.BuiltAppDir == "" {
			cmd := fmt.Sprintf("&& if [ -d %s/shared/cached-copy ]; then cd %s/shared/cached-copy && git fetch -q origin && git fetch --tags -q origin && git reset -q --hard %s && git clean -q -d -x -f; else git clone -q --depth 1 %s %s/shared/cached-copy && cd %s/shared/cached-copy && git checkout -q -b deploy %s; fi",
				conf.DeployDir, conf.DeployDir, sha1, repoAddress, conf.DeployDir, conf.DeployDir, sha1)
			buffer.WriteString(cmd)
		}

		cmd = fmt.Sprintf(" && if [ ! -d %s ]; then mkdir -p %s; fi", releaseDir, releaseDir)
		buffer.WriteString(cmd)

		if conf.BuiltAppDir == "" {
			cmd = fmt.Sprintf(" && cp -RPp %s/shared/cached-copy %s && (echo %s > %s/REVISION)",
				conf.DeployDir, releaseDir, sha1, releaseDir)
			buffer.WriteString(cmd)
		}

		cmd = fmt.Sprintf(" && chmod -R g+w %s", releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && (rm -rf %s/public/system || true) && mkdir -p %s/public/", releaseDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && ln -s %s/shared/system %s/public/system", conf.DeployDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && rm -rf %s/log", releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && ln -s %s/shared/log %s/log", conf.DeployDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && rm -rf %s/tmp/pids && mkdir -p %s/tmp/", releaseDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && ln -s %s/shared/pids %s/tmp/pids", conf.DeployDir, releaseDir)
		buffer.WriteString(cmd)

		cmd = fmt.Sprintf(" && rm -rf %s/public/assets && mkdir -p %s/public && mkdir -p %s/shared/assets && ln -s %s/shared/assets %s/public/assets",
			releaseDir, releaseDir, conf.DeployDir, conf.DeployDir, releaseDir)
		buffer.WriteString(cmd)

		prepareServer := func(server Server) {
			runCmd(exec.Command("ssh", "-p", server.Port, "-o", fmt.Sprintf("ControlPath='%s'", server.ControlPath()), "-l", server.User, server.Ip, buffer.String()))
			serverPrepared <- true
		}
		for _, server := range env.Servers {
			go prepareServer(server)
		}
	}()

	// Block until servers are prepared
	// FIXME: servers should not block each other
	for _ = range env.Servers {
		<-serverPrepared
	}

	// Rsync assets in the background
	assetsRsynced := make(chan bool, len(env.Servers))
	go func() {
		// Block until all assets built
		for _ = range conf.AssetBuildCommands {
			<-assetBuildReady
		}
		rsyncAssets := func(server Server) {
			cmd := fmt.Sprintf("rsync -e 'ssh -p %s -o ControlPath=\"%s\"' --recursive --times --compress --human-readable public/assets %s:%s/shared",
				server.Port, server.ControlPath(), server.Host(), conf.DeployDir)
			runCmd(exec.Command("/bin/sh", "-c", cmd))
			assetsRsynced <- true
		}
		for _, server := range env.Servers {
			go rsyncAssets(server)
		}
	}()

	// Rsync app in the background
	appRsynced := make(chan bool, len(env.Servers))
	go func() {
		// Block until app is built
		for _ = range conf.AppBuildCommands {
			<-appBuildReady
		}
		rsyncApp := func(server Server) {
			if conf.BuiltAppDir != "" {
				cmd := fmt.Sprintf("rsync -e 'ssh -p %s -o ControlPath=\"%s\"' --recursive --times --compress --human-readable %s %s:%s",
					server.Port, server.ControlPath(), conf.BuiltAppDir, server.Host(), releaseDir)
				runCmd(exec.Command("/bin/sh", "-c", cmd))
			}
			appRsynced <- true
		}
		for _, server := range env.Servers {
			go rsyncApp(server)
		}
	}()

	// If remote commands are finished and assets are synced, replace symlink and restart server
	var finalize bytes.Buffer

	cmd := fmt.Sprintf("rm -f %s/current && ln -s %s %s/current", conf.DeployDir, releaseDir, conf.DeployDir)
	finalize.WriteString(cmd)

	cmd = fmt.Sprintf(" && %s", env.RestartCommand)
	finalize.WriteString(cmd)

	serverFinalized := make(chan bool, len(env.Servers))
	finalizeServer := func(server Server) {
		<-appRsynced
		<-assetsRsynced
		runCmd(exec.Command("ssh", "-p", server.Port, "-o", fmt.Sprintf("ControlPath='%s'", server.ControlPath()), "-l", server.User, server.Ip, finalize.String()))
		fmt.Printf("** App deployed to %s:%s (%v)\n", server.Host(), releaseDir, time.Since(start))
		serverFinalized <- true
	}
	for _, server := range env.Servers {
		go finalizeServer(server)
	}
	for _ = range env.Servers {
		<-serverFinalized
		fmt.Printf("All done.\n")
	}
}

func runCmd(cmd *exec.Cmd) []byte {
	cmdStart := time.Now()
	args := strings.Join(cmd.Args, " ")
	b, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("\n\n")
		fmt.Printf("Error: %v\n", err)
		fmt.Printf("Command: %v\n", args)
		fmt.Printf("Output: %v\n\n", string(b))
		os.Exit(1)
	}
	fmt.Printf("%s (%v)\n", args, time.Since(cmdStart))
	return b
}

func NewSampleConfiguration() *Configuration {
	return &Configuration{
		Environments: []Environment{
			Environment{
				Name: "staging",
				Servers: []Server{
					Server{Port: "22", User: "deployment", Ip: "127.0.0.1"},
					Server{Port: "22", User: "deployment", Ip: "localhost"}},
				RestartCommand: "(sudo stop myapp_staging || true) && sudo start myapp_staging"},
			Environment{
				Name: "production",
				Servers: []Server{
					Server{Port: "22", User: "deployment", Ip: "www.myapp.com"}},
				RestartCommand: "(sudo stop myapp_productioin || true) && sudo start myapp_production"}},
		AppBuildCommands:   []string{"make linux64bit"},
		AssetBuildCommands: []string{"make css_assets_gzip", "make js_assets_gzip"},
		BuiltAppDir:        "out/myapp",
		DeployDir:          "/var/www/myapp",
	}
}

func (server *Server) Host() string {
	return fmt.Sprintf("%s@%s", server.User, server.Ip)
}

func (server *Server) ControlPath() string {
	return fmt.Sprintf("%s:%s", server.Host(), server.Port)
}
